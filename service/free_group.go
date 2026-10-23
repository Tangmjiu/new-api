package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const freeGroupNamespace = "new-api:{free-group}:v1"

var (
	freeConfigMu    sync.RWMutex
	freeConfig      model.FreeGroupConfig
	freeVersion     int64
	freeLoaded      atomic.Bool
	freeSchemaReady atomic.Bool
)

var (
	ErrFreeGroupDisabled                 = errors.New("free group is not enabled")
	ErrFreeGroupForbidden                = errors.New("free group access is not allowed")
	ErrFreeGroupModelForbidden           = errors.New("model is not allowed by the free group model whitelist")
	ErrFreeGroupCapacity                 = errors.New("free group concurrency limit reached")
	ErrFreeGroupQueueFull                = errors.New("free group queue is full")
	ErrFreeGroupQueueTimeout             = errors.New("free group queue wait timed out")
	ErrFreeGroupRedis                    = errors.New("free group concurrency service is unavailable")
	ErrFreeGroupRiskConfirmationRequired = errors.New("free group high-risk confirmation is required")
)

type FreeGroupConfigSnapshot struct {
	Config  model.FreeGroupConfig
	Version int64
	Ready   bool
}

func FreeGroupName() string { return model.FreeGroupName }

func InitFreeGroupConfig() error {
	// Feature schema failures must not stop ordinary groups from starting.
	if common.IsMasterNode {
		if err := model.DB.AutoMigrate(&model.FreeGroupConfigRecord{}, &model.FreeGroupConfigVersion{}, &model.FreeGroupMember{}, &model.FreeGroupAudit{}); err != nil {
			return err
		}
		if err := model.InitializeFreeGroupConfig(); err != nil {
			return err
		}
	}
	if err := RefreshFreeGroupConfig(); err != nil {
		return err
	}
	freeSchemaReady.Store(true)
	return nil
}

func RefreshFreeGroupConfig() (err error) {
	defer func() {
		if err != nil {
			freeLoaded.Store(false)
		}
	}()
	record, err := model.GetFreeGroupConfigRecord()
	if err != nil {
		freeLoaded.Store(false)
		return err
	}
	config := model.DefaultFreeGroupConfig()
	if err := common.Unmarshal([]byte(record.Config), &config); err != nil {
		return fmt.Errorf("decode free group configuration: %w", err)
	}
	if err := ValidateFreeGroupConfig(config); err != nil {
		return err
	}
	if config.Enabled && (common.RDB == nil || !common.RedisEnabled) {
		return ErrFreeGroupRedis
	}
	if common.RDB != nil && common.RedisEnabled {
		syncCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, syncErr := common.RDB.Eval(syncCtx, `local v=tonumber(redis.call('HGET',KEYS[1],'version')) or 0;if v<=tonumber(ARGV[1]) then redis.call('HSET',KEYS[1],'version',ARGV[1],'enabled',ARGV[2]) end;return 1`, []string{freeGroupNamespace + ":config"}, record.Version, strconv.FormatBool(config.Enabled)).Result()
		cancel()
		if syncErr != nil {
			return ErrFreeGroupRedis
		}
	}
	freeConfigMu.Lock()
	if record.Version < freeVersion && freeLoaded.Load() {
		freeConfigMu.Unlock()
		return nil
	}
	freeConfig = config
	freeVersion = record.Version
	freeConfigMu.Unlock()
	freeLoaded.Store(true)
	return nil
}

func StartFreeGroupConfigSync(ctx context.Context, frequency time.Duration) {
	if frequency <= 0 {
		frequency = time.Minute
	}
	var lastInit, lastCleanup time.Time
	refresh := func() {
		if !freeSchemaReady.Load() {
			if time.Since(lastInit) < 30*time.Second {
				return
			}
			lastInit = time.Now()
			if err := InitFreeGroupConfig(); err != nil {
				common.SysLog(common.LogText("initialize free group configuration failed: %v", err))
				return
			}
		}
		if err := RefreshFreeGroupConfig(); err != nil {
			common.SysLog(common.LogText("refresh free group configuration failed: %v", err))
			return
		}
		if config := GetFreeGroupConfig().Config; config.AntiAbuse.RequestRetention.Enabled && time.Since(lastCleanup) >= time.Hour {
			lastCleanup = time.Now()
			cutoff := time.Now().AddDate(0, 0, -config.AntiAbuse.RequestRetention.RetentionDays).Unix()
			if err := model.DB.Where("rule_name = ? AND created_at < ?", "request_trace", cutoff).Delete(&model.FreeGroupAudit{}).Error; err != nil {
				common.SysLog(common.LogText("clean up free group request traces failed: %v", err))
			}
		}
	}
	if common.RDB != nil && common.RedisEnabled {
		go func() {
			pubsub := common.RDB.Subscribe(ctx, freeGroupNamespace+":config:changed")
			defer pubsub.Close()
			messages := pubsub.Channel()
			ticker := time.NewTicker(frequency)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case _, ok := <-messages:
					if !ok {
						return
					}
					refresh()
				case <-ticker.C:
					refresh()
				}
			}
		}()
		return
	}
	go func() {
		ticker := time.NewTicker(frequency)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refresh()
			}
		}
	}()
}

func GetFreeGroupConfig() FreeGroupConfigSnapshot {

	freeConfigMu.RLock()
	defer freeConfigMu.RUnlock()
	return FreeGroupConfigSnapshot{Config: freeConfig, Version: freeVersion, Ready: freeLoaded.Load()}
}

func ValidateFreeGroupConfig(config model.FreeGroupConfig) error {
	if err := validateFreeRuleNumbers(reflect.ValueOf(config), "free_group"); err != nil {
		return err
	}
	if _, err := time.LoadLocation(config.Rate.Timezone); err != nil {
		return errors.New("invalid Free quota timezone")
	}
	if len(config.Models.Models) > 10000 || len(config.Blacklist.UserIDs) > 100000 {
		return errors.New("Free configuration list is too large")
	}
	if config.SchemaVersion != 1 {
		return errors.New("free group schema_version must be positive")
	}
	switch config.AccessMode {
	case model.FreeGroupAccessModeAll, model.FreeGroupAccessModeCondition, model.FreeGroupAccessModeApply, model.FreeGroupAccessModeInvite:
	default:
		return fmt.Errorf("unsupported free group access_mode %q", config.AccessMode)
	}
	if config.Blacklist.Recovery != "" && config.Blacklist.Recovery != "auto" && config.Blacklist.Recovery != "manual" {
		return errors.New("free group blacklist recovery must be auto or manual")
	}
	if config.Models.OutsidePolicy != model.FreeGroupOutsideModelReject && config.Models.OutsidePolicy != model.FreeGroupOutsideModelAllow {
		return errors.New("free group outside model policy must be reject or allow")
	}
	if config.Concurrency.LeaseTTLSeconds < 1 || config.Concurrency.LeaseTTLSeconds > 24*60*60 {
		return errors.New("free group concurrency lease TTL must be between 1 and 86400 seconds")
	}
	if config.Concurrency.LeaseTTLEnabled && config.Concurrency.Renew.Enabled && (config.Concurrency.Renew.HeartbeatSeconds <= 0 || config.Concurrency.Renew.HeartbeatSeconds >= config.Concurrency.LeaseTTLSeconds) {
		return errors.New("free group concurrency renew heartbeat must be positive")
	}
	if config.Queue.PollMillis < 50 || config.Queue.PollMillis > 5000 {
		return errors.New("Free queue poll interval must be 50..5000 milliseconds")
	}
	if config.Queue.Enabled && (config.Queue.MaxLength <= 0 || config.Queue.MaxLength > 10000 || config.Queue.MaxWaitSeconds <= 0 || config.Queue.MaxWaitSeconds > 3600) {
		return errors.New("free group queue length and wait timeout must be positive when enabled")
	}
	if config.Concurrency.UserShare.Enabled && (config.Concurrency.Global.Limit <= 0 || config.Concurrency.UserShare.Percent <= 0 || config.Concurrency.UserShare.Percent > 100) {
		return errors.New("free group user share must be between 0 and 100 percent")
	}
	if config.Request.MaxTokens.Enabled && (config.Request.MaxTokens.Limit <= 0 || config.Request.MaxTokens.Limit > 2147483647) {
		return errors.New("free group max_tokens limit must be positive when enabled")
	}
	if config.Request.MaxTokens.Enabled && config.Request.MaxTokens.Limit > constant.MaxRelayOutputTokens {
		return errors.New("Free output token limit exceeds the protocol safety bound")
	}
	if config.Rate.IdleCredit.Enabled && (config.Rate.IdleCredit.AccrualPerHour < 1 || config.Rate.IdleCredit.Maximum < 1) {
		return errors.New("Free idle credit accrual and maximum must be positive")
	}
	if config.Request.Parameters.TemperatureMin > config.Request.Parameters.TemperatureMax {
		return errors.New("invalid temperature bounds")
	}
	for _, r := range config.Request.Parameters.Ranges {
		if r.Min > r.Max {
			return errors.New("invalid Free parameter range")
		}
	}
	if config.Admission.Captcha.Enabled && config.Admission.Captcha.Method != "turnstile" {
		return errors.New("Free CAPTCHA currently requires turnstile")
	}
	for _, binding := range config.Admission.Bindings.Required {
		if !slices.Contains([]string{"email", "github", "discord", "oidc", "wechat", "telegram", "linuxdo", "oauth"}, binding) {
			return errors.New("unknown Free binding requirement")
		}
	}
	for name, rule := range map[string]model.FreeIntRule{"body_size_kb": config.Request.BodySize, "prompt_tokens": config.Request.PromptTokens, "timeout_seconds": config.Request.TimeoutSeconds, "cooldown_on_limit": config.Penalty.CooldownOnLimit, "cooldown_minutes": config.Penalty.CooldownMinutes} {
		if rule.Enabled && rule.Limit < 1 {
			return fmt.Errorf("%s must be positive when enabled", name)
		}
	}
	if config.Request.OutputInputRatio.Enabled && config.Request.OutputInputRatio.Limit <= 0 {
		return errors.New("output/input ratio must be positive")
	}
	for _, rule := range []model.FreeAbuseRule{config.AntiAbuse.IPActiveAccounts, config.AntiAbuse.FingerprintActiveAccounts, config.AntiAbuse.IPAggregateRate, config.AntiAbuse.PromptHashRepeat, config.AntiAbuse.MachineInterval, config.AntiAbuse.NightHighFrequency, config.AntiAbuse.ShortPrompt, config.AntiAbuse.FailureRate} {
		if rule.Enabled && (rule.Threshold <= 0 || rule.WindowSeconds < 1) {
			return errors.New("enabled Free abuse rules require a positive threshold and window")
		}
		if rule.Action != "" && !slices.Contains([]string{"observe", "alert", "slowdown", "cooldown", "remove"}, rule.Action) {
			return errors.New("unsupported Free abuse action")
		}
	}
	if config.Penalty.CooldownMinutes.Enabled && config.Penalty.MaximumMinutes < 1 {
		return errors.New("maximum cooldown must be positive")
	}
	if config.Penalty.Exponential && config.Penalty.Multiplier < 1 {
		return errors.New("cooldown multiplier must be at least one")
	}
	if config.AntiAbuse.RequestRetention.Enabled && config.AntiAbuse.RequestRetention.RetentionDays < 1 {
		return errors.New("Free audit retention must be positive")
	}
	if config.Request.MaxTokens.Action != "" && config.Request.MaxTokens.Action != "truncate" && config.Request.MaxTokens.Action != "reject" {
		return errors.New("free group max_tokens action must be truncate or reject")
	}
	for name, dimension := range map[string]model.FreeConcurrencyDimension{
		"user": config.Concurrency.User, "token": config.Concurrency.Token, "ip": config.Concurrency.IP,
		"model": config.Concurrency.Model, "global": config.Concurrency.Global,
	} {
		if dimension.Enabled && (dimension.Limit <= 0 || dimension.Limit > 1000000) {
			return fmt.Errorf("free group %s concurrency limit must be positive when enabled", name)
		}
		if dimension.Enabled && dimension.Strategy == "queue" && (dimension.MaxWaitSeconds < 1 || dimension.MaxWaitSeconds > 3600 || dimension.MaxQueueLength < 1 || dimension.MaxQueueLength > 10000) {
			return fmt.Errorf("invalid %s queue bounds", name)
		}
		if dimension.Strategy != "" && dimension.Strategy != model.FreeGroupQueueStrategyReject && dimension.Strategy != model.FreeGroupQueueStrategyQueue {
			return fmt.Errorf("free group %s concurrency strategy is invalid", name)
		}
	}
	share := config.Concurrency.UserShare
	if share.Strategy != "reject" && share.Strategy != "queue" {
		return errors.New("Invalid Free user-share strategy")
	}
	if share.Enabled && share.Strategy == "queue" && (share.MaxWaitSeconds < 1 || share.MaxWaitSeconds > 3600 || share.MaxQueueLength < 1 || share.MaxQueueLength > 10000) {
		return errors.New("Invalid Free user-share queue bounds")
	}
	for _, action := range []model.FreeActionConfig{config.AntiAbuse.Actions.Observe, config.AntiAbuse.Actions.Notify, config.AntiAbuse.Actions.Slowdown, config.AntiAbuse.Actions.Cooldown, config.AntiAbuse.Actions.Remove} {
		if action.Enabled && (action.Threshold < 1 || action.WindowSeconds < 1) {
			return errors.New("Enabled Free actions require a positive threshold and window")
		}
	}
	for name, rule := range map[string]model.FreeBurstRule{
		"rpm": config.Rate.RPM, "tpm": config.Rate.TPM,
	} {
		if rule.Enabled && (rule.Limit <= 0 || rule.Burst <= 0) {
			return fmt.Errorf("free group %s limit and burst must be positive when enabled", name)
		}
	}
	for name, rule := range map[string]model.FreeWindowRule{
		"hourly_requests": config.Rate.HourlyRequests, "hourly_tokens": config.Rate.HourlyTokens,
		"daily_requests": config.Rate.DailyRequests, "daily_tokens": config.Rate.DailyTokens,
		"monthly_requests": config.Rate.MonthlyRequests, "monthly_tokens": config.Rate.MonthlyTokens,
	} {
		if rule.Enabled && (rule.Limit <= 0 || rule.WindowSeconds <= 0) {
			return fmt.Errorf("free group %s limit and window must be positive when enabled", name)
		}
	}
	return nil
}

func IsFreeGroupRequest(c *gin.Context) bool {
	if c == nil {
		return false
	}
	return common.GetContextKeyString(c, constant.ContextKeyUsingGroup) == model.FreeGroupName ||
		common.GetContextKeyString(c, constant.ContextKeyTokenGroup) == model.FreeGroupName
}

func IsFreeGroupEnabled() bool { return freeLoaded.Load() && GetFreeGroupConfig().Config.Enabled }

func IsFreeGroupModelAllowed(modelName string) bool {
	config := GetFreeGroupConfig().Config
	if !config.Models.WhitelistEnabled {
		return true
	}
	for _, allowed := range config.Models.Models {
		if strings.TrimSpace(allowed) == modelName {
			return true
		}
	}
	return config.Models.OutsidePolicy == model.FreeGroupOutsideModelAllow
}

func FreeGroupConfigHighRisk(config model.FreeGroupConfig) bool {
	if !config.Concurrency.LeaseTTLEnabled {
		return true
	}
	if !config.Models.WhitelistEnabled || config.Models.OutsidePolicy == model.FreeGroupOutsideModelAllow {
		return true
	}
	if config.Concurrency.Global.Enabled && config.Concurrency.Global.Limit > 5000 {
		return true
	}
	if config.Rate.DailyRequests.Enabled && config.Rate.DailyRequests.Limit > 20000 {
		return true
	}
	concurrencyEnabled := config.Concurrency.User.Enabled || config.Concurrency.Token.Enabled || config.Concurrency.IP.Enabled || config.Concurrency.Model.Enabled || config.Concurrency.Global.Enabled
	rateEnabled := config.Rate.RPM.Enabled || config.Rate.TPM.Enabled || config.Rate.HourlyRequests.Enabled || config.Rate.HourlyTokens.Enabled || config.Rate.DailyRequests.Enabled || config.Rate.DailyTokens.Enabled || config.Rate.MonthlyRequests.Enabled || config.Rate.MonthlyTokens.Enabled
	requestProtectionEnabled := config.Request.MaxTokens.Enabled || config.Request.BodySize.Enabled || config.Request.PromptTokens.Enabled || config.Request.TimeoutSeconds.Enabled || config.Request.Streaming.Enabled || config.Request.Multimodal.Enabled
	return !concurrencyEnabled && !rateEnabled && !requestProtectionEnabled
}

func SaveFreeGroupConfig(config model.FreeGroupConfig, expectedVersion int64, updatedBy int, summary string, confirmHighRisk bool) (int64, error) {
	if err := ValidateFreeGroupConfig(config); err != nil {
		return 0, err
	}
	if config.Enabled && (common.RDB == nil || !common.RedisEnabled) {
		return 0, ErrFreeGroupRedis
	}
	encoded, err := common.Marshal(config)
	if err != nil {
		return 0, err
	}
	current, err := model.GetFreeGroupConfigRecord()
	if err != nil {
		return 0, err
	}
	if current.Version != expectedVersion {
		return 0, model.ErrFreeGroupConfigVersionConflict
	}
	previous := model.DefaultFreeGroupConfig()
	if err := common.UnmarshalJsonStr(current.Config, &previous); err != nil {
		return 0, err
	}
	highRisk := FreeGroupConfigHighRisk(config) || FreeGroupRelaxed(previous, config)
	if highRisk && !confirmHighRisk {
		return 0, ErrFreeGroupRiskConfirmationRequired
	}
	if len(summary) > 512 {
		return 0, errors.New("Free change summary exceeds 512 bytes")
	}
	if current.Version != expectedVersion {
		return 0, model.ErrFreeGroupConfigVersionConflict
	}
	record := &model.FreeGroupConfigRecord{ID: 1, Version: expectedVersion, Preset: config.Preset, Config: string(encoded), UpdatedBy: updatedBy}
	snapshot := &model.FreeGroupConfigVersion{Preset: config.Preset, Config: string(encoded), UpdatedBy: updatedBy, Summary: strings.TrimSpace(summary), HighRisk: highRisk}
	if err := model.SaveFreeGroupConfig(record, snapshot); err != nil {
		return 0, err
	}
	if err := RefreshFreeGroupConfig(); err != nil {
		return snapshot.Version, &FreeGroupActivationPendingError{Version: snapshot.Version, Err: err}
	}
	if common.RDB != nil && common.RedisEnabled {
		_ = common.RDB.Publish(context.Background(), freeGroupNamespace+":config:changed", strconv.FormatInt(snapshot.Version, 10)).Err()
	}
	return snapshot.Version, nil
}

// The database transaction has committed, but activation awaits recovery of a
// dependent component. Callers must retain the committed version for CAS.
type FreeGroupActivationPendingError struct {
	Version int64
	Err     error
}

func (e *FreeGroupActivationPendingError) Error() string {
	return "Free configuration saved; activation pending"
}
func (e *FreeGroupActivationPendingError) Unwrap() error { return e.Err }

func RollbackFreeGroupConfig(version, expectedVersion int64, updatedBy int, confirmHighRisk bool) (int64, error) {
	snapshot, err := model.GetFreeGroupConfigVersion(version)
	if err != nil {
		return 0, err
	}
	config := model.DefaultFreeGroupConfig()
	if err := common.Unmarshal([]byte(snapshot.Config), &config); err != nil {
		return 0, err
	}
	return SaveFreeGroupConfig(config, expectedVersion, updatedBy, fmt.Sprintf("Rollback to version %d", version), confirmHighRisk)
}

func SubmitFreeGroupApplication(userID int) error {
	config := GetFreeGroupConfig().Config
	if userID <= 0 {
		return ErrFreeGroupForbidden
	}
	if !config.Enabled || config.AccessMode != model.FreeGroupAccessModeApply {
		return ErrFreeGroupDisabled
	}
	var member model.FreeGroupMember
	err := model.DB.Where("user_id = ?", userID).First(&member).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if member.Status == model.FreeGroupMemberBlocked || member.Status == "removed" {
		return ErrFreeGroupForbidden
	}
	if config.Admission.Captcha.Enabled && member.CaptchaUntil < time.Now().Unix() {
		return ErrFreeGroupForbidden
	}
	return model.UpsertFreeGroupMember(&model.FreeGroupMember{UserID: userID, Status: model.FreeGroupMemberPending, Source: "application"}, GetFreeGroupConfig().Version)
}

func SetFreeGroupMember(userID int, status, reason string, actorID int) error {
	if status != model.FreeGroupMemberApproved && status != model.FreeGroupMemberPending && status != model.FreeGroupMemberBlocked {
		return errors.New("invalid free group member status")
	}
	var user model.User
	if err := model.DB.Select("id").First(&user, userID).Error; err != nil {
		return err
	}
	if len(reason) > 255 {
		return errors.New("Free membership reason exceeds 255 bytes")
	}
	if err := model.UpsertFreeGroupMember(&model.FreeGroupMember{UserID: userID, Status: status, Source: "manual", Reason: reason, UpdatedBy: actorID}, GetFreeGroupConfig().Version); err != nil {
		return err
	}
	return nil
}

func CheckFreeGroupAccess(userID int) error {
	if userID <= 0 {
		return ErrFreeGroupForbidden
	}
	if !freeLoaded.Load() {
		return ErrFreeGroupRedis
	}
	return freeGroupEligibility(GetFreeGroupConfig().Config, userID)
}

func freeGroupEligibility(config model.FreeGroupConfig, userID int) error {
	if !config.Enabled {
		return ErrFreeGroupDisabled
	}
	blockedUsers := config.Blacklist.UserIDs
	if !config.Blacklist.Enabled {
		blockedUsers = nil
	}
	for _, blocked := range blockedUsers {
		if blocked == userID {
			return ErrFreeGroupForbidden
		}
	}
	var member model.FreeGroupMember
	err := model.DB.Where("user_id = ?", userID).First(&member).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err == nil && member.Status == "removed" && freeRemovalRuleActive(config, member.Reason) && (member.ExpiresAt == 0 || member.ExpiresAt > time.Now().Unix()) {
		return ErrFreeGroupForbidden
	}
	if config.Blacklist.Enabled && err == nil && member.Status == model.FreeGroupMemberBlocked {
		return ErrFreeGroupForbidden
	}
	switch config.AccessMode {
	case model.FreeGroupAccessModeAll:
		return nil
	case model.FreeGroupAccessModeApply, model.FreeGroupAccessModeInvite:
		if err == nil && member.Status == model.FreeGroupMemberApproved {
			return nil
		}
		return ErrFreeGroupForbidden
	case model.FreeGroupAccessModeCondition:
		if config.Admission.Captcha.Enabled && member.CaptchaUntil < time.Now().Unix() {
			return ErrFreeGroupForbidden
		}
		user, userErr := model.GetUserById(userID, false)
		if userErr != nil {
			return userErr
		}
		if config.Admission.Registration.Enabled && time.Since(time.Unix(user.CreatedAt, 0)).Hours() < float64(config.Admission.Registration.Limit) {
			return ErrFreeGroupForbidden
		}
		if config.Admission.Bindings.Enabled {
			for _, binding := range config.Admission.Bindings.Required {
				present := freeUserBindingPresent(user, binding)
				if binding == "oauth" {
					var count int64
					if err := model.DB.Model(&model.UserOAuthBinding{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
						return err
					}
					present = count > 0
				}
				if !present {
					return ErrFreeGroupForbidden
				}
			}
		}
		return nil
	default:
		return ErrFreeGroupForbidden
	}
}

func freeRemovalRuleActive(config model.FreeGroupConfig, name string) bool {
	if name == "admission.ip_accounts" {
		return config.AccessMode == model.FreeGroupAccessModeCondition && config.Admission.IPAccounts.Enabled
	}
	if name == "admission.fingerprint_accounts" {
		return config.AccessMode == model.FreeGroupAccessModeCondition && config.Admission.FingerprintAccounts.Enabled
	}
	if !config.AntiAbuse.Actions.Remove.Enabled {
		return false
	}
	rules := map[string]model.FreeAbuseRule{"ip_active_accounts": config.AntiAbuse.IPActiveAccounts, "fingerprint_active_accounts": config.AntiAbuse.FingerprintActiveAccounts, "ip_aggregate_rate": config.AntiAbuse.IPAggregateRate, "prompt_hash_repeat": config.AntiAbuse.PromptHashRepeat, "machine_interval": config.AntiAbuse.MachineInterval, "night_high_frequency": config.AntiAbuse.NightHighFrequency, "short_prompt": config.AntiAbuse.ShortPrompt, "failure_rate": config.AntiAbuse.FailureRate}
	rule, ok := rules[name]
	return ok && rule.Enabled && rule.Action == "remove"
}

func freeUserBindingPresent(user *model.User, binding string) bool {
	switch strings.ToLower(strings.TrimSpace(binding)) {
	case "email":
		return strings.TrimSpace(user.Email) != ""
	case "github":
		return strings.TrimSpace(user.GitHubId) != ""
	case "discord":
		return strings.TrimSpace(user.DiscordId) != ""
	case "oidc":
		return strings.TrimSpace(user.OidcId) != ""
	case "wechat":
		return strings.TrimSpace(user.WeChatId) != ""
	case "telegram":
		return strings.TrimSpace(user.TelegramId) != ""
	case "linuxdo":
		return strings.TrimSpace(user.LinuxDOId) != ""
	default:
		return false
	}
}

func FreeGroupModelCheck(c *gin.Context, modelName string) error {
	if !IsFreeGroupRequest(c) {
		return nil
	}
	if !IsFreeGroupEnabled() {
		return ErrFreeGroupDisabled
	}
	if strings.TrimSpace(modelName) == "" || !IsFreeGroupModelAllowed(modelName) {
		return ErrFreeGroupModelForbidden
	}
	return nil
}

func FreeGroupClientIP(c *gin.Context) string {
	if c == nil {
		return ""
	}
	ip := net.ParseIP(c.ClientIP())
	if ip == nil {
		return c.ClientIP()
	}
	return ip.String()
}

func FreeGroupRequestError(err error) (int, string) {
	var rejected *FreeGroupRejection
	if errors.As(err, &rejected) {
		return rejected.Status, rejected.Error()
	}
	switch {
	case errors.Is(err, ErrFreeGroupDisabled):
		return 403, "Free group is not currently available"
	case errors.Is(err, ErrFreeGroupForbidden):
		return 403, "You are not eligible for the free group"
	case errors.Is(err, ErrFreeGroupModelForbidden):
		return 403, "The requested model is not allowed by the free group configuration"
	case errors.Is(err, ErrFreeGroupQueueFull):
		return 429, "The free group queue is full"
	case errors.Is(err, ErrFreeGroupQueueTimeout):
		return 429, "The free group queue wait timed out"
	case errors.Is(err, ErrFreeGroupCapacity):
		return 429, "The free group concurrency limit has been reached"
	case errors.Is(err, ErrFreeGroupRedis):
		return 503, "The free group capacity service is temporarily unavailable"
	default:
		return 500, "The free group request was rejected"
	}
}

func FreeGroupErrorCode(err error) types.ErrorCode {
	if errors.Is(err, ErrFreeGroupRedis) {
		return types.ErrorCodeGetChannelFailed
	}
	return types.ErrorCodeInvalidRequest
}

// Validate disabled rules too: storing NaN, negative counters or unbounded
// windows must not turn a future toggle into an arithmetic or Redis error.
func validateFreeRuleNumbers(value reflect.Value, path string) error {
	switch value.Kind() {
	case reflect.Struct:
		for i := range value.NumField() {
			if err := validateFreeRuleNumbers(value.Field(i), path+"."+value.Type().Field(i).Name); err != nil {
				return err
			}
		}
	case reflect.Map:
		iter := value.MapRange()
		for iter.Next() {
			if err := validateFreeRuleNumbers(iter.Value(), path); err != nil {
				return err
			}
		}
	case reflect.Float64:
		n := value.Float()
		if math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > 1e9 {
			return fmt.Errorf("invalid numeric value for %s", path)
		}
	case reflect.Int, reflect.Int64:
		if n := value.Int(); n < 0 || n > 2147483647 {
			return fmt.Errorf("%s must be between 0 and 2147483647", path)
		}
	}
	return nil
}
