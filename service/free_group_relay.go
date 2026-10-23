package service

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const freeRuntimeKey = "free_group_runtime"

type freeRelayRuntime struct {
	lease       *FreeConcurrencyLease
	reservation *FreeRateReservation
	cancel      context.CancelFunc
	stopRelease func() bool
	original    context.Context
	actual      atomic.Int64
	haveActual  atomic.Bool
	finished    atomic.Bool
	abuse       *FreeAbuseContext
	userID      int
}

// BeginFreeGroupRelay is called after protocol validation and token counting,
// immediately before relay. It never reads pricing or traditional group ratios.
func BeginFreeGroupRelay(c *gin.Context, info *relaycommon.RelayInfo, promptTokens int, meta *types.TokenCountMeta) error {
	if !IsFreeGroupRequest(c) {
		return nil
	}
	if value, ok := c.Get(freeRuntimeKey); ok && value != nil {
		return nil
	}
	if !common.GetContextKeyBool(c, constant.ContextKeyFreeGroup) {
		return ErrFreeGroupForbidden
	}
	if err := CheckFreeGroupAccess(info.UserId); err != nil {
		return err
	}
	if err := CheckFreeGroupCooldown(c, info.UserId); err != nil {
		return err
	}
	if err := FreeGroupModelCheck(c, info.OriginModelName); err != nil {
		return err
	}
	snapshot := GetFreeGroupConfig()
	config := snapshot.Config
	path := c.Request.URL.Path
	if !freeCanSetOutputBudget(info.Request) && freeRequiresTokenMeter(config, info.OriginModelName) && (strings.Contains(path, "/images/") || strings.Contains(path, "/audio/") || strings.Contains(path, "/videos")) {
		return &FreeGroupRejection{Rule: "request.token_meter", Status: 400, Recovery: "use_metered_text_protocol_or_disable_token_rules"}
	}
	if err := FreeGroupRequestValidation(c); err != nil {
		return err
	}
	if config.Request.Streaming.Enabled && !config.Request.Streaming.Allowed && info.IsStream {
		return &FreeGroupRejection{Rule: "request.streaming", Current: 1, Status: 400, Recovery: "disable_stream"}
	}
	if config.Request.PromptTokens.Enabled && int64(promptTokens) > config.Request.PromptTokens.Limit {
		return &FreeGroupRejection{Rule: "request.prompt_tokens", Current: float64(promptTokens), Limit: float64(config.Request.PromptTokens.Limit), Status: 400, Recovery: "smaller_request"}
	}
	if config.Request.Multimodal.Enabled && !config.Request.Multimodal.Allowed && meta != nil && len(meta.Files) > 0 {
		return &FreeGroupRejection{Rule: "request.multimodal", Current: 1, Status: 400, Recovery: "text_request"}
	}
	output, err := FreeGroupOutputLimit(c)
	if err != nil {
		return err
	}
	multiplicity, err := freeOutputMultiplicity(c, info.Request)
	if err != nil {
		return err
	}
	if promptTokens < 0 || int64(promptTokens) > math.MaxInt32 || output > (math.MaxInt32-int64(promptTokens))/multiplicity {
		return &FreeGroupRejection{Rule: "request.token_budget", Limit: math.MaxInt32, Status: 400, Recovery: "smaller_request"}
	}
	totalOutput := output * multiplicity
	if config.Request.OutputInputRatio.Enabled && float64(totalOutput) > float64(promptTokens)*config.Request.OutputInputRatio.Limit {
		return &FreeGroupRejection{Rule: "request.output_input_ratio", Current: float64(totalOutput) / float64(max(1, promptTokens)), Limit: config.Request.OutputInputRatio.Limit, Status: 400, Recovery: "smaller_request"}
	}
	estimate := int64(promptTokens) + totalOutput
	prompt := ""
	if meta != nil {
		prompt = meta.CombineText
	}
	fingerprint := c.GetHeader("X-Free-Fingerprint")
	if fingerprint == "" && config.AccessMode == model.FreeGroupAccessModeCondition && config.Admission.FingerprintAccounts.Enabled {
		return &FreeGroupRejection{Rule: "admission.fingerprint_required", Status: 400, Recovery: "provide_fingerprint"}
	}
	if fingerprint != "" {
		fingerprint = freeKeyPart(fingerprint)
	}
	abuse := &FreeAbuseContext{UserID: info.UserId, IP: FreeGroupClientIP(c), Fingerprint: fingerprint, PromptHash: freeKeyPart(prompt), PromptTokens: promptTokens, OutputMultiplicity: multiplicity, Version: snapshot.Version}
	elastic := false
	maximum := min(int64(math.MaxInt32), int64(promptTokens)+constant.MaxRelayOutputTokens*multiplicity)
	if output == 0 && freeCanSetOutputBudget(info.Request) {
		_, rules, err := freeRateRules(config, info.UserId, info.OriginModelName, estimate, time.Now())
		if err != nil {
			return err
		}
		for _, rule := range rules {
			elastic = elastic || (rule.Tokens && !rule.Unlimited)
		}
		if config.Request.OutputInputRatio.Enabled {
			maximum = min(maximum, int64(promptTokens)+int64(common.QuotaFromFloat(float64(promptTokens)*config.Request.OutputInputRatio.Limit)))
			elastic = true
		}
	}
	reservation, err := reserveFreeGroupUsage(c.Request.Context(), config, info.UserId, info.OriginModelName, estimate, elastic, maximum, abuse)
	if err != nil {
		return err
	}
	if elastic {
		if err := freeSetOutputBudget(c, info.Request, (reservation.Reserved-int64(promptTokens))/multiplicity); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = reservation.Cancel(cleanupCtx)
			return err
		}
	}
	queueID := c.GetHeader("X-Free-Queue-ID")
	if queueID == "" {
		queueID = uuid.NewString()
	}
	c.Header("X-Free-Queue-ID", queueID)
	lease, err := acquireFreeConcurrency(c.Request.Context(), FreeConcurrencyRequest{UserID: info.UserId, TokenID: info.TokenId, IP: FreeGroupClientIP(c), Model: info.OriginModelName, Queue: true, QueueID: queueID}, config, snapshot.Version)
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if settleErr := reservation.Cancel(cleanupCtx); settleErr != nil {
			common.SysError("Free reservation refund failed before relay")
		}
		return err
	}
	lease.ReservationKey = reservation.Keys[len(reservation.Keys)-1]
	original := c.Request.Context()
	var ctx context.Context
	var cancel context.CancelFunc
	if config.Request.TimeoutSeconds.Enabled {
		ctx, cancel = context.WithTimeout(original, time.Duration(config.Request.TimeoutSeconds.Limit)*time.Second)
	} else {
		ctx, cancel = context.WithCancel(original)
	}
	runtime := &freeRelayRuntime{lease: lease, reservation: reservation, cancel: cancel, original: original, abuse: abuse, userID: info.UserId}
	c.Set(freeRuntimeKey, runtime)
	common.SetContextKey(c, constant.ContextKeyFreeGroupFailed, false)
	common.SetContextKey(c, constant.ContextKeyFreeGroupAdmitted, true)
	c.Request = c.Request.WithContext(ctx)
	runtime.stopRelease = context.AfterFunc(ctx, func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		lease.Release(cleanupCtx)
	})
	if lease.ReservationKey != "" {
		go func() {
			interval := time.Hour
			if lease.TTL > 0 {
				interval = lease.TTL
			}
			if lease.TTL > 0 && lease.Renew.Enabled {
				interval = time.Duration(lease.Renew.HeartbeatSeconds) * time.Second
			}
			timer := time.NewTicker(interval)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
					if lease.TTL > 0 && !lease.Renew.Enabled {
						cancel()
						return
					}
					renewCtx, stop := context.WithTimeout(ctx, min(interval, 2*time.Second))
					err := lease.RenewLease(renewCtx)
					stop()
					if err != nil {
						cancel()
						return
					}
				}
			}
		}()
	}
	return nil
}

// Finish always runs via defer, including panic and downstream errors. The
// context callback releases on cancellation even if an upstream ignores it.
func FinishFreeGroupRelay(c *gin.Context) {
	recovered := recover()
	defer func() {
		if recovered != nil {
			panic(recovered)
		}
	}()
	value, ok := c.Get(freeRuntimeKey)
	if !ok {
		return
	}
	runtime, ok := value.(*freeRelayRuntime)
	if !ok || runtime == nil || runtime.finished.Swap(true) {
		return
	}
	requestErr := c.Request.Context().Err()
	runtime.stopRelease()
	runtime.cancel()
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runtime.lease.Release(cleanupCtx)
	actual := runtime.reservation.Reserved
	if runtime.haveActual.Load() {
		actual = runtime.actual.Load()
	}
	// If the upstream omits usage or disconnects, retain the reservation rather
	// than refunding unverifiable output. No paid account balance is involved.
	if err := runtime.reservation.Settle(cleanupCtx, actual); err != nil {
		common.SysError("Free usage settlement failed; reservation retained")
	}
	outcome := "completed"
	failed := recovered != nil || c.Writer.Status() >= 400 || common.GetContextKeyBool(c, constant.ContextKeyFreeGroupFailed)
	if failed {
		outcome = "failed"
	}
	if requestErr != nil {
		outcome = "cancelled"
	}
	if err := RecordFreeGroupTrace(c, runtime.abuse, actual, outcome); err != nil {
		freeComponentErrors.WithLabelValues("audit").Inc()
		common.SysError("Free request audit failed")
	}
	config := GetFreeGroupConfig().Config
	if config.AntiAbuse.FailureRate.Enabled {
		if err := recordFreeFailure(cleanupCtx, runtime.userID, config.AntiAbuse.FailureRate.WindowSeconds, failed || requestErr != nil); err != nil {
			freeComponentErrors.WithLabelValues("failure_record").Inc()
		}
	}
	c.Request = c.Request.WithContext(runtime.original)
	c.Set(freeRuntimeKey, nil)
	c.Set("free_group_cooldown_version", int64(0))
	common.SetContextKey(c, constant.ContextKeyFreeGroupAdmitted, false)
}

func RecordFreeGroupUsage(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage) {
	if !IsFreeGroupRequest(c) || usage == nil {
		return
	}
	value, ok := c.Get(freeRuntimeKey)
	if !ok {
		return
	}
	runtime, ok := value.(*freeRelayRuntime)
	if !ok || runtime == nil {
		return
	}
	tokens := int64(max(0, usage.PromptTokens)) + int64(max(0, usage.CompletionTokens))
	if usageSemanticFromUsage(info, usage) == "anthropic" {
		tokens += int64(max(0, usage.PromptTokensDetails.CachedTokens))
		tokens += int64(max(0, usage.PromptTokensDetails.CachedCreationTokens))
	}
	runtime.actual.Store(min(tokens, int64(math.MaxInt32)))
	runtime.haveActual.Store(true)
}

func FreeRelayAPIError(err error) *types.NewAPIError {
	status, _ := FreeGroupRequestError(err)
	var rejection *FreeGroupRejection
	code := types.ErrorCode("free_group_unavailable")
	if errors.As(err, &rejection) {
		code = types.ErrorCode("free_group_" + rejection.Rule)
	}
	if errors.Is(err, ErrFreeGroupDisabled) {
		code = "free_group_disabled"
	}
	apiError := types.NewErrorWithStatusCode(err, code, status, types.ErrOptionWithSkipRetry())
	if rejection != nil {
		metadata, marshalErr := common.Marshal(rejection)
		if marshalErr == nil {
			apiError = types.WithOpenAIError(types.OpenAIError{Message: err.Error(), Type: "free_group_error", Code: code, Metadata: metadata}, status, types.ErrOptionWithSkipRetry())
			apiError.Err = err
		}
	}
	return apiError
}

// Ensure a reserved Free identifier can never silently fall through billing.
func FreeGroupBillingBypass(c *gin.Context, info *relaycommon.RelayInfo) bool {
	return info != nil && info.UsingGroup == model.FreeGroupName && common.GetContextKeyBool(c, constant.ContextKeyFreeGroup) && common.GetContextKeyBool(c, constant.ContextKeyFreeGroupAdmitted)
}
