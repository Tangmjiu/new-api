package service

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func freeTestDatabase(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "free.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	oldDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = oldDB; require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.FreeGroupConfigRecord{}, &model.FreeGroupConfigVersion{}, &model.FreeGroupMember{}, &model.FreeGroupAudit{}, &model.User{}))
	require.NoError(t, model.InitializeFreeGroupConfig())
}

func freeTestConfig(t *testing.T) (*miniredis.Miniredis, model.FreeGroupConfig) {
	t.Helper()
	server := miniredis.RunT(t)
	server.SetTime(time.Now())
	oldRDB, oldEnabled := common.RDB, common.RedisEnabled
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	common.RedisEnabled = true
	freeConfigMu.Lock()
	oldConfig, oldVersion, oldLoaded := freeConfig, freeVersion, freeLoaded.Load()
	freeConfigMu.Unlock()
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RDB = oldRDB
		common.RedisEnabled = oldEnabled
		freeConfigMu.Lock()
		freeConfig = oldConfig
		freeVersion = oldVersion
		freeLoaded.Store(oldLoaded)
		freeConfigMu.Unlock()
	})
	config := model.DefaultFreeGroupConfig()
	config.Enabled = true
	config.Concurrency.UserShare.Enabled = false
	freeConfigMu.Lock()
	freeConfig = config
	freeVersion = 1
	freeLoaded.Store(true)
	freeConfigMu.Unlock()
	return server, config
}

func TestFreeGroupConcurrency(t *testing.T) {
	server, config := freeTestConfig(t)
	ctx := context.Background()
	request := FreeConcurrencyRequest{UserID: 1, TokenID: 1, IP: "192.0.2.1", Model: "admin-selected"}
	t.Run("atomic all or nothing", func(t *testing.T) {
		config.Concurrency.User.Limit = 1
		lease, err := acquireFreeConcurrency(ctx, request, config)
		require.NoError(t, err)
		other := request
		other.TokenID = 2
		other.IP = "192.0.2.2"
		_, err = acquireFreeConcurrency(ctx, other, config)
		var rejected *FreeGroupRejection
		require.ErrorAs(t, err, &rejected)
		assert.Equal(t, "concurrency.user", rejected.Rule)
		keys, _, _, _ := freeDimensionKeys(other, config)
		assert.EqualValues(t, 0, common.RDB.ZCard(ctx, keys[1]).Val())
		assert.EqualValues(t, 0, common.RDB.ZCard(ctx, keys[2]).Val())
		lease.Release(ctx)
		lease.Release(ctx)
		for _, key := range lease.Keys {
			assert.EqualValues(t, 0, common.RDB.ZCard(ctx, key).Val())
		}
	})
	t.Run("expired lease cannot renew", func(t *testing.T) {
		server.FlushAll()
		server.SetTime(time.Now())
		lease, err := acquireFreeConcurrency(ctx, request, config)
		require.NoError(t, err)
		server.SetTime(time.Now().Add(91 * time.Second))
		assert.Error(t, lease.RenewLease(ctx))
		next, err := acquireFreeConcurrency(ctx, request, config)
		require.NoError(t, err)
		next.Release(ctx)
		expired, err := common.RDB.HGet(ctx, freeGroupNamespace+":metrics", "expired:user").Int64()
		require.NoError(t, err)
		assert.Positive(t, expired)
	})
	t.Run("renew keeps every dimension", func(t *testing.T) {
		server.FlushAll()
		server.SetTime(time.Now())
		lease, err := acquireFreeConcurrency(ctx, request, config)
		require.NoError(t, err)
		server.SetTime(time.Now().Add(45 * time.Second))
		require.NoError(t, lease.RenewLease(ctx))
		for _, key := range lease.Keys {
			score, err := common.RDB.ZScore(ctx, key, lease.ID).Result()
			require.NoError(t, err)
			assert.Greater(t, score, float64(time.Now().Add(90*time.Second).UnixMilli()))
		}
		lease.Release(ctx)
	})
	t.Run("wrong key type cannot leave partial leases", func(t *testing.T) {
		server.FlushAll()
		keys, _, _, _ := freeDimensionKeys(request, config)
		require.NoError(t, common.RDB.Set(ctx, keys[2], "wrong", 0).Err())
		_, err := acquireFreeConcurrency(ctx, request, config)
		require.ErrorIs(t, err, ErrFreeGroupRedis)
		assert.EqualValues(t, 0, common.RDB.ZCard(ctx, keys[0]).Val())
	})
	t.Run("disabled dimensions impose no limit", func(t *testing.T) {
		server.FlushAll()
		disabled := config
		disabled.Concurrency.User.Enabled = false
		disabled.Concurrency.Token.Enabled = false
		disabled.Concurrency.IP.Enabled = false
		disabled.Concurrency.Model.Enabled = false
		disabled.Concurrency.Global.Enabled = false
		lease, err := acquireFreeConcurrency(ctx, request, disabled)
		require.NoError(t, err)
		second, err := acquireFreeConcurrency(ctx, request, disabled)
		require.NoError(t, err)
		second.Release(ctx)
		lease.Release(ctx)
	})
}

func TestFreeGroupQueueCancellationAndRecovery(t *testing.T) {
	server, config := freeTestConfig(t)
	server.SetTime(time.Now())
	config.Concurrency.User.Limit = 1
	config.Concurrency.User.Strategy = "queue"
	config.Queue.Enabled = true
	config.Queue.PollMillis = 50
	ctx := context.Background()
	request := FreeConcurrencyRequest{UserID: 1, TokenID: 1, IP: "192.0.2.1", Model: "selected", Queue: true}
	active, err := acquireFreeConcurrency(ctx, request, config)
	require.NoError(t, err)
	queuedCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	request.QueueID = uuid.NewString()
	enqueued := make(chan int64, 1)
	finished := make(chan error, 1)
	request.OnQueue = func(position int64, _ time.Duration) {
		select {
		case enqueued <- position:
		default:
		}
	}
	go func() { _, err := acquireFreeConcurrency(queuedCtx, request, config); finished <- err }()
	select {
	case position := <-enqueued:
		assert.EqualValues(t, 1, position)
	case <-time.After(3 * time.Second):
		t.Fatal("request did not queue")
	}
	status, err := InspectFreeQueue(ctx, request.UserID, request.QueueID, false)
	require.NoError(t, err)
	assert.Equal(t, "queued", status.State)
	assert.EqualValues(t, 1, status.Position)
	_, err = InspectFreeQueue(ctx, 99, request.QueueID, true)
	require.Error(t, err)
	status, err = InspectFreeQueue(ctx, request.UserID, request.QueueID, true)
	require.NoError(t, err)
	assert.Equal(t, "cancelled", status.State)
	select {
	case err := <-finished:
		var rejection *FreeGroupRejection
		require.ErrorAs(t, err, &rejection)
		assert.Equal(t, "queue.cancelled", rejection.Rule)
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled ticket did not stop waiting")
	}
	assert.EqualValues(t, 0, common.RDB.ZCard(ctx, freeGroupNamespace+":queue:order").Val())
	// A crashed queue head has an individual deadline; it cannot block others.
	require.NoError(t, common.RDB.ZAdd(ctx, freeGroupNamespace+":queue:order", &redis.Z{Score: 1, Member: "abandoned"}).Err())
	require.NoError(t, common.RDB.ZAdd(ctx, freeGroupNamespace+":queue:deadlines", &redis.Z{Score: float64(time.Now().Add(-time.Second).UnixMilli()), Member: "abandoned"}).Err())
	active.Release(ctx)
	request.QueueID = uuid.NewString()
	lease, err := acquireFreeConcurrency(ctx, request, config)
	require.NoError(t, err)
	lease.Release(ctx)
	assert.EqualValues(t, 0, common.RDB.ZCard(ctx, freeGroupNamespace+":queue:order").Val())
}

func TestFreeGroupRateReservation(t *testing.T) {
	server, config := freeTestConfig(t)
	ctx := context.Background()
	config.Rate.RPM.Enabled = false
	config.Rate.TPM.Enabled = false
	config.Rate.DailyRequests.Limit = 1
	config.Rate.DailyTokens.Limit = 100
	reservation, err := ReserveFreeGroupUsage(ctx, config, 1, "selected", 80)
	require.NoError(t, err)
	_, err = ReserveFreeGroupUsage(ctx, config, 1, "selected", 10)
	var rejection *FreeGroupRejection
	require.ErrorAs(t, err, &rejection)
	assert.Equal(t, "rate.daily_requests", rejection.Rule)
	require.NoError(t, reservation.Settle(ctx, 20))
	require.NoError(t, reservation.Settle(ctx, 20))
	assert.Equal(t, "20", common.RDB.HGet(ctx, reservation.Keys[1], "value").Val())
	assert.Equal(t, "1", common.RDB.HGet(ctx, reservation.Keys[0], "value").Val())
	t.Run("cancel refunds request and token reservations once", func(t *testing.T) {
		server.FlushAll()
		reserved, err := ReserveFreeGroupUsage(ctx, config, 1, "selected", 80)
		require.NoError(t, err)
		require.NoError(t, reserved.Cancel(ctx))
		require.NoError(t, reserved.Cancel(ctx))
		assert.Equal(t, "0", common.RDB.HGet(ctx, reserved.Keys[0], "value").Val())
		assert.Equal(t, "0", common.RDB.HGet(ctx, reserved.Keys[1], "value").Val())
		_, err = ReserveFreeGroupUsage(ctx, config, 1, "selected", 80)
		require.NoError(t, err)
	})
	t.Run("model limits override default daily limits", func(t *testing.T) {
		server.FlushAll()
		current := config
		current.Models.PerModelQuota = true
		current.Models.PerModelLimits = map[string]model.FreeModelLimit{"selected": {DailyRequests: 2, DailyTokens: 200}}
		first, err := ReserveFreeGroupUsage(ctx, current, 1, "selected", 80)
		require.NoError(t, err)
		_, err = ReserveFreeGroupUsage(ctx, current, 1, "selected", 80)
		require.NoError(t, err)
		_, err = ReserveFreeGroupUsage(ctx, current, 1, "selected", 1)
		require.ErrorAs(t, err, &rejection)
		assert.Equal(t, "rate.model_requests:"+freeKeyPart("selected"), rejection.Rule)
		assert.EqualValues(t, 2, first.Rules[0].Limit)
		current.Models.PerModelQuota = false
		_, err = ReserveFreeGroupUsage(ctx, current, 1, "selected", 80)
		require.NoError(t, err)
		_, err = ReserveFreeGroupUsage(ctx, current, 1, "selected", 1)
		require.ErrorAs(t, err, &rejection)
		assert.Equal(t, "rate.daily_requests", rejection.Rule)
	})
	t.Run("bucket burst and rejected request do not consume quota", func(t *testing.T) {
		server.FlushAll()
		config.Rate.RPM = model.FreeBurstRule{Enabled: true, Limit: 3, Burst: 2}
		config.Rate.DailyRequests.Enabled = false
		config.Rate.DailyTokens.Enabled = false
		first, err := ReserveFreeGroupUsage(ctx, config, 1, "selected", 1)
		require.NoError(t, err)
		_, err = ReserveFreeGroupUsage(ctx, config, 1, "selected", 1)
		require.NoError(t, err)
		_, err = ReserveFreeGroupUsage(ctx, config, 1, "selected", 1)
		require.ErrorAs(t, err, &rejection)
		assert.Equal(t, "rate.rpm", rejection.Rule)
		assert.Equal(t, "0", common.RDB.HGet(ctx, first.Keys[0], "value").Val())
	})
	t.Run("calendar uses configured timezone and DST", func(t *testing.T) {
		config.Rate.DailyRequests.Enabled = true
		config.Rate.RPM.Enabled = false
		config.Rate.Timezone = "America/New_York"
		now := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
		_, rules, err := freeRateRules(config, 1, "selected", 1, now)
		require.NoError(t, err)
		assert.Equal(t, time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC).UnixMilli(), rules[0].Reset)
	})
}

func TestFreeGroupRequestRules(t *testing.T) {
	_, config := freeTestConfig(t)
	cases := []struct {
		name, body  string
		change      func(*model.FreeGroupConfig)
		rule        string
		path        string
		contentType string
	}{
		{name: "max tokens", body: `{"max_tokens":513}`, rule: "request.max_tokens"},
		{name: "disabled max tokens", body: `{"max_tokens":513}`, change: func(c *model.FreeGroupConfig) { c.Request.MaxTokens.Enabled = false }},
		{name: "checks both token caps", body: `{"max_tokens":10,"max_completion_tokens":600}`, rule: "request.max_tokens"},
		{name: "nested override", body: `{"max_tokens":10,"extra_body":{"max_tokens":600}}`, rule: "request.max_tokens"},
		{name: "content type cannot bypass cap", body: `{"max_tokens":600}`, contentType: "text/plain", rule: "request.max_tokens"},
		{name: "media prompt text is ordinary text", body: `{"messages":[{"role":"user","content":"image_url file image"}]}`},
		{name: "structured media", body: `{"messages":[{"content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`, rule: "request.multimodal"},
		{name: "file reference", body: `{"input":{"file_id":"file-test"}}`, rule: "request.multimodal"},
		{name: "audio output", body: `{"modalities":["text","audio"]}`, rule: "request.multimodal"},
		{name: "text output", body: `{"modalities":["text"]}`},
		{name: "gemini image output", body: `{"generationConfig":{"responseModalities":["IMAGE"]}}`, rule: "request.multimodal"},
		{name: "image endpoint", body: `{"prompt":"hello"}`, path: "/v1/images/generations", rule: "request.multimodal"},
		{name: "multipart file", body: "--test\r\nContent-Disposition: form-data; name=\"file\"; filename=\"x.txt\"\r\n\r\nhello\r\n--test--\r\n", contentType: "multipart/form-data; boundary=test", rule: "request.multimodal"},
		{name: "disabled endpoint guard", body: `{"prompt":"hello"}`, path: "/v1/images/generations", change: func(c *model.FreeGroupConfig) { c.Request.Multimodal.Enabled = false }},
		{name: "disabled media guard", body: `{"input":[{"type":"input_image"}]}`, change: func(c *model.FreeGroupConfig) { c.Request.Multimodal.Enabled = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			current := config
			if tc.change != nil {
				tc.change(&current)
			}
			freeConfigMu.Lock()
			freeConfig = current
			freeConfigMu.Unlock()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			path, contentType := tc.path, tc.contentType
			if path == "" {
				path = "/v1/chat/completions"
			}
			if contentType == "" {
				contentType = "application/json"
			}
			c.Request = httptest.NewRequest("POST", path, strings.NewReader(tc.body))
			c.Request.Header.Set("Content-Type", contentType)
			common.SetContextKey(c, constant.ContextKeyUsingGroup, model.FreeGroupName)
			err := FreeGroupRequestValidation(c)
			if tc.rule == "" {
				require.NoError(t, err)
			} else {
				var rejection *FreeGroupRejection
				require.ErrorAs(t, err, &rejection)
				assert.Equal(t, tc.rule, rejection.Rule)
			}
		})
	}
}

func TestFreeGroupCandidateBudgets(t *testing.T) {
	server, config := freeTestConfig(t)
	freeTestDatabase(t)
	config.Models.Models = []string{"selected"}
	config.Request.MaxTokens.Enabled = false
	config.Rate.RPM.Enabled = false
	config.Rate.TPM.Enabled = false
	config.Rate.DailyTokens.Limit = 100
	_, err := SaveFreeGroupConfig(config, 1, 17, "Enable Free", true)
	require.NoError(t, err)
	user := model.User{Username: "candidate-budgets", Group: "default"}
	require.NoError(t, model.DB.Create(&user).Error)
	for _, tc := range []struct {
		name, body, path, rule string
		budget                 uint
	}{
		{name: "two candidates share remaining budget", body: `{"n":2}`, budget: 45},
		{name: "too little for each candidate", body: `{"n":91}`, rule: "rate.daily_tokens"},
		{name: "all explicit candidates reserved", body: `{"n":3,"max_tokens":40}`, rule: "rate.daily_tokens"},
		{name: "candidate product overflow", body: `{"n":1073741823,"max_tokens":512}`, rule: "request.token_budget"},
		{name: "negative candidates", body: `{"n":-1}`, rule: "request.output_count"},
		{name: "fractional candidates", body: `{"n":1.5}`, rule: "request.output_count"},
		{name: "best of candidates reserved", body: `{"n":1,"best_of":3,"max_tokens":40}`, rule: "rate.daily_tokens"},
		{name: "batch completion output", body: `{"prompt":["first","second"],"n":2}`, path: "/v1/completions", budget: 22},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server.FlushAll()
			require.NoError(t, RefreshFreeGroupConfig())
			path := tc.path
			if path == "" {
				path = "/v1/chat/completions"
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", path, strings.NewReader(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")
			common.SetContextKey(c, constant.ContextKeyUsingGroup, model.FreeGroupName)
			common.SetContextKey(c, constant.ContextKeyFreeGroup, true)
			request := &dto.GeneralOpenAIRequest{}
			if err := common.UnmarshalJsonStr(tc.body, request); err != nil {
				require.Equal(t, "request.output_count", tc.rule)
			}
			info := &relaycommon.RelayInfo{UserId: user.Id, TokenId: 1, UsingGroup: model.FreeGroupName, OriginModelName: "selected", Request: request}
			err := BeginFreeGroupRelay(c, info, 10, &types.TokenCountMeta{CombineText: "hello"})
			if tc.rule != "" {
				var rejected *FreeGroupRejection
				require.ErrorAs(t, err, &rejected)
				assert.Equal(t, tc.rule, rejected.Rule)
				assert.EqualValues(t, 0, common.RDB.ZCard(context.Background(), freeGroupNamespace+":concurrency:user:"+strconv.Itoa(user.Id)).Val())
				return
			}
			require.NoError(t, err)
			defer FinishFreeGroupRelay(c)
			if tc.path == "/v1/completions" {
				require.NotNil(t, request.MaxTokens)
				assert.Equal(t, tc.budget, *request.MaxTokens)
			} else {
				require.NotNil(t, request.MaxCompletionTokens)
				assert.Equal(t, tc.budget, *request.MaxCompletionTokens)
			}
		})
	}
}

func TestFreeGroupFailureIsolation(t *testing.T) {
	_, config := freeTestConfig(t)
	common.RedisEnabled = false
	_, err := acquireFreeConcurrency(context.Background(), FreeConcurrencyRequest{}, config)
	assert.ErrorIs(t, err, ErrFreeGroupRedis)
	_, err = ReserveFreeGroupUsage(context.Background(), config, 1, "selected", 1)
	assert.ErrorIs(t, err, ErrFreeGroupRedis)
	config.Enabled = false
	_, err = acquireFreeConcurrency(context.Background(), FreeConcurrencyRequest{}, config)
	assert.ErrorIs(t, err, ErrFreeGroupDisabled)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	assert.False(t, IsFreeGroupRequest(c))
	require.NoError(t, FreeGroupRequestValidation(c))
	assert.False(t, errors.Is(err, context.Canceled))
}

func TestFreeGroupUsageBudgetAndIdleCredit(t *testing.T) {
	server, config := freeTestConfig(t)
	ctx := context.Background()
	config.Rate.RPM.Enabled = false
	config.Rate.TPM.Enabled = false
	config.Rate.DailyTokens.Limit = 100

	t.Run("omitted output budget reserves remaining tokens atomically", func(t *testing.T) {
		reservation, err := reserveFreeGroupUsage(ctx, config, 1, "selected", 10, true, 1000)
		require.NoError(t, err)
		assert.EqualValues(t, 100, reservation.Reserved)
		_, err = reserveFreeGroupUsage(ctx, config, 1, "selected", 10, true, 1000)
		var rejected *FreeGroupRejection
		require.ErrorAs(t, err, &rejected)
		assert.Equal(t, "rate.daily_tokens", rejected.Rule)
		require.NoError(t, reservation.Settle(ctx, 40))
		next, err := reserveFreeGroupUsage(ctx, config, 1, "selected", 10, true, 1000)
		require.NoError(t, err)
		assert.EqualValues(t, 60, next.Reserved)
		require.NoError(t, next.Cancel(ctx))
	})
	t.Run("usage is visible with every quota disabled", func(t *testing.T) {
		server.FlushAll()
		current := config
		current.Rate.DailyRequests.Enabled = false
		current.Rate.DailyTokens.Enabled = false
		freeConfigMu.Lock()
		freeConfig = current
		freeConfigMu.Unlock()
		reservation, err := ReserveFreeGroupUsage(ctx, current, 1, "selected", 80)
		require.NoError(t, err)
		require.NoError(t, reservation.Settle(ctx, 30))
		status, err := GetFreeUsageStatus(ctx, 1)
		require.NoError(t, err)
		assert.EqualValues(t, 1, status.Requests)
		assert.EqualValues(t, 30, status.Tokens)
		assert.Nil(t, status.RequestLimit)
		assert.Nil(t, status.TokenLimit)
		assert.Nil(t, status.RequestsLeft)
		assert.Nil(t, status.TokensLeft)
	})
	t.Run("idle request credits are capped and canceled requests refund them", func(t *testing.T) {
		server.FlushAll()
		start := time.Now()
		server.SetTime(start)
		current := config
		current.Rate.DailyRequests.Limit = 1
		current.Rate.DailyTokens.Enabled = false
		current.Rate.IdleCredit = model.FreeIdleCreditConfig{Enabled: true, AccrualPerHour: 1, Maximum: 2}
		_, err := ReserveFreeGroupUsage(ctx, current, 1, "selected", 1)
		require.NoError(t, err)
		server.SetTime(start.Add(3 * time.Hour))
		credit, err := ReserveFreeGroupUsage(ctx, current, 1, "selected", 1)
		require.NoError(t, err)
		require.NoError(t, credit.Cancel(ctx))
		for range 2 {
			_, err := ReserveFreeGroupUsage(ctx, current, 1, "selected", 1)
			require.NoError(t, err)
		}
		_, err = ReserveFreeGroupUsage(ctx, current, 1, "selected", 1)
		var rejected *FreeGroupRejection
		require.ErrorAs(t, err, &rejected)
		assert.Equal(t, "rate.daily_requests", rejected.Rule)
		current.Rate.DailyRequests.Enabled = false
		_, err = ReserveFreeGroupUsage(ctx, current, 1, "selected", 1)
		require.NoError(t, err)
	})
}

func TestFreeGroupManualPenaltyScopes(t *testing.T) {
	server, config := freeTestConfig(t)
	freeTestDatabase(t)
	ctx := context.Background()
	user := model.User{Username: "free-penalty", Group: "default"}
	require.NoError(t, model.DB.Create(&user).Error)
	input := &FreeAbuseContext{UserID: user.Id, IP: "192.0.2.1", Fingerprint: freeKeyPart("device")}
	for _, tc := range []struct{ scope, target string }{{"user", strconv.Itoa(user.Id)}, {"ip", input.IP}, {"fingerprint", input.Fingerprint}} {
		t.Run(tc.scope, func(t *testing.T) {
			server.FlushAll()
			request := FreeManualPenalty{Scope: tc.scope, Target: tc.target, Action: "cooldown", Minutes: 10, Reason: "Administrator review"}
			require.NoError(t, ApplyFreeManualPenalty(ctx, request, 17))
			_, err := ReserveFreeGroupUsage(ctx, config, user.Id, "selected", 1, input)
			var rejected *FreeGroupRejection
			require.ErrorAs(t, err, &rejected)
			assert.Equal(t, "penalty.cooldown", rejected.Rule)
			assert.Positive(t, rejected.RetryAfter)
			request.Action = "release"
			require.NoError(t, ApplyFreeManualPenalty(ctx, request, 17))
			_, err = ReserveFreeGroupUsage(ctx, config, user.Id, "selected", 1, input)
			require.NoError(t, err)
			items, total, err := ListFreeAudits(ctx, FreeAuditFilter{Rule: "manual." + tc.scope})
			require.NoError(t, err)
			assert.EqualValues(t, 2, total)
			require.Len(t, items, 2)
			assert.Contains(t, items[0].Snapshot, `"result":"applied"`)
			assert.Contains(t, items[0].Snapshot, `"actor_id":17`)
		})
	}
}

func TestFreeGroupAdmissionAndHotUpdates(t *testing.T) {
	_, config := freeTestConfig(t)
	freeTestDatabase(t)
	user := model.User{Username: "free-admission", Email: "user@example.test", Group: "default", CreatedAt: time.Now().Add(-48 * time.Hour).Unix()}
	require.NoError(t, model.DB.Create(&user).Error)
	config.Models.Models = []string{"selected"}
	version, err := SaveFreeGroupConfig(config, 1, 17, "Enable Free", true)
	require.NoError(t, err)
	assert.True(t, IsFreeGroupEnabled())
	require.NoError(t, CheckFreeGroupAccess(user.Id))
	for _, mode := range []string{"condition", "apply", "invite"} {
		t.Run(mode, func(t *testing.T) {
			config.AccessMode = mode
			var err error
			version, err = SaveFreeGroupConfig(config, version, 17, "Change admission", true)
			require.NoError(t, err)
			require.NoError(t, model.DB.Where("user_id = ?", user.Id).Delete(&model.FreeGroupMember{}).Error)
			if mode == "condition" {
				require.NoError(t, CheckFreeGroupAccess(user.Id))
				require.NoError(t, model.DB.Model(&user).Update("created_at", time.Now().Unix()).Error)
				assert.ErrorIs(t, CheckFreeGroupAccess(user.Id), ErrFreeGroupForbidden)
				config.Admission.Registration.Enabled = false
				version, err = SaveFreeGroupConfig(config, version, 17, "Disable registration age", true)
				require.NoError(t, err)
				require.NoError(t, CheckFreeGroupAccess(user.Id))
				return
			}
			assert.ErrorIs(t, CheckFreeGroupAccess(user.Id), ErrFreeGroupForbidden)
			if mode == "apply" {
				require.NoError(t, SubmitFreeGroupApplication(user.Id))
				assert.ErrorIs(t, CheckFreeGroupAccess(user.Id), ErrFreeGroupForbidden)
			} else {
				assert.Error(t, SubmitFreeGroupApplication(user.Id))
			}
			require.NoError(t, SetFreeGroupMember(user.Id, model.FreeGroupMemberApproved, "Approved", 17))
			require.NoError(t, CheckFreeGroupAccess(user.Id))
		})
	}
	config.AccessMode = "all"
	config.Enabled = false
	version, err = SaveFreeGroupConfig(config, version, 17, "Disable Free", true)
	require.NoError(t, err)
	assert.False(t, IsFreeGroupEnabled())
	assert.ErrorIs(t, CheckFreeGroupAccess(user.Id), ErrFreeGroupDisabled)
	_, err = SaveFreeGroupConfig(config, version-1, 17, "Stale draft", true)
	assert.ErrorIs(t, err, model.ErrFreeGroupConfigVersionConflict)
	version, err = RollbackFreeGroupConfig(2, version, 17, true)
	require.NoError(t, err)
	assert.Equal(t, version, GetFreeGroupConfig().Version)
	assert.True(t, IsFreeGroupEnabled())
	require.NoError(t, SetFreeGroupMember(user.Id, model.FreeGroupMemberBlocked, "Blocked", 17))
	assert.ErrorIs(t, CheckFreeGroupAccess(user.Id), ErrFreeGroupForbidden)
	config = GetFreeGroupConfig().Config
	config.Blacklist.Enabled = false
	_, err = SaveFreeGroupConfig(config, version, 17, "Disable blacklist", true)
	require.NoError(t, err)
	require.NoError(t, CheckFreeGroupAccess(user.Id))
}

func TestFreeGroupRelayReleasesAndKeepsPaidQuota(t *testing.T) {
	for _, outcome := range []string{"success", "panic", "disconnect", "timeout"} {
		t.Run(outcome, func(t *testing.T) {
			_, config := freeTestConfig(t)
			freeTestDatabase(t)
			config.Models.Models = []string{"selected"}
			config.Request.MaxTokens.Enabled = false
			config.Rate.RPM.Enabled = false
			config.Rate.TPM.Enabled = false
			config.Rate.DailyTokens.Limit = 100
			if outcome == "timeout" {
				config.Request.TimeoutSeconds.Limit = 1
			}
			_, err := SaveFreeGroupConfig(config, 1, 17, "Enable Free", true)
			require.NoError(t, err)
			user := model.User{Username: "free-lifecycle", Group: "default", Quota: 12345}
			require.NoError(t, model.DB.Create(&user).Error)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			original, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"selected","messages":[{"role":"user","content":"hello"}]}`)).WithContext(original)
			c.Request.Header.Set("Content-Type", "application/json")
			common.SetContextKey(c, constant.ContextKeyUsingGroup, model.FreeGroupName)
			common.SetContextKey(c, constant.ContextKeyFreeGroup, true)
			request := &dto.GeneralOpenAIRequest{Model: "selected"}
			info := &relaycommon.RelayInfo{UserId: user.Id, TokenId: 1, UsingGroup: model.FreeGroupName, OriginModelName: "selected", Request: request}
			work := func() {
				defer FinishFreeGroupRelay(c)
				require.NoError(t, BeginFreeGroupRelay(c, info, 10, &types.TokenCountMeta{CombineText: "hello"}))
				require.NotNil(t, request.MaxCompletionTokens)
				assert.EqualValues(t, 90, *request.MaxCompletionTokens)
				status, err := GetFreeUsageStatus(context.Background(), user.Id)
				require.NoError(t, err)
				assert.EqualValues(t, 1, status.Concurrency)
				if outcome == "panic" {
					panic("upstream panic")
				}
				if outcome == "disconnect" {
					cancel()
					return
				}
				if outcome == "timeout" {
					select {
					case <-c.Request.Context().Done():
					case <-time.After(3 * time.Second):
						t.Fatal("Free request timeout did not cancel the request")
					}
					// Observe release while this simulated upstream still holds
					// the handler, before the deferred finalizer can run.
					require.Eventually(t, func() bool {
						status, err := GetFreeUsageStatus(context.Background(), user.Id)
						return err == nil && status.Concurrency == 0
					}, time.Second, time.Millisecond)
					return
				}
				RecordFreeGroupUsage(c, info, &dto.Usage{PromptTokens: 10, CompletionTokens: 20})
			}
			if outcome == "panic" {
				assert.Panics(t, work)
			} else {
				work()
			}
			require.Eventually(t, func() bool {
				status, err := GetFreeUsageStatus(context.Background(), user.Id)
				return err == nil && status.Concurrency == 0
			}, time.Second, time.Millisecond)
			var saved model.User
			require.NoError(t, model.DB.First(&saved, user.Id).Error)
			assert.EqualValues(t, 12345, saved.Quota)
			status, err := GetFreeUsageStatus(context.Background(), user.Id)
			require.NoError(t, err)
			if outcome == "success" {
				assert.EqualValues(t, 30, status.Tokens)
			} else {
				assert.EqualValues(t, 100, status.Tokens)
			}
		})
	}
}

func TestFreeGroupAbuseActions(t *testing.T) {
	server, config := freeTestConfig(t)
	freeTestDatabase(t)
	ctx := context.Background()
	config.Rate.RPM.Enabled = false
	config.Rate.TPM.Enabled = false
	config.Rate.DailyRequests.Enabled = false
	config.Rate.DailyTokens.Enabled = false
	config.AntiAbuse.IPActiveAccounts.Enabled = false
	config.AntiAbuse.FingerprintActiveAccounts.Enabled = false
	config.AntiAbuse.PromptHashRepeat.Enabled = false
	config.AntiAbuse.FailureRate.Enabled = false
	input := &FreeAbuseContext{UserID: 1, IP: "192.0.2.1", PromptHash: freeKeyPart("example")}
	t.Run("concurrency rejections contribute consecutive strikes", func(t *testing.T) {
		server.FlushAll()
		server.SetTime(time.Now())
		current := config
		current.Concurrency.User.Limit = 1
		current.Penalty.CooldownOnLimit.Limit = 2
		request := FreeConcurrencyRequest{UserID: 1, TokenID: 1, IP: input.IP, Model: "selected"}
		active, err := acquireFreeConcurrency(ctx, request, current)
		require.NoError(t, err)
		defer active.Release(ctx)
		for _, want := range []string{"concurrency.user", "penalty.cooldown"} {
			reservation, err := ReserveFreeGroupUsage(ctx, current, 1, "selected", 1, input)
			require.NoError(t, err)
			_, err = acquireFreeConcurrency(ctx, request, current)
			var rejected *FreeGroupRejection
			require.ErrorAs(t, err, &rejected)
			assert.Equal(t, want, rejected.Rule)
			require.NoError(t, reservation.Cancel(ctx))
		}
		var count int64
		require.NoError(t, model.DB.Model(&model.FreeGroupAudit{}).Where("rule_name = ? AND action = ?", "penalty.consecutive_limits", "cooldown").Count(&count).Error)
		assert.EqualValues(t, 1, count)
	})
	t.Run("successful response resets consecutive failures", func(t *testing.T) {
		server.FlushAll()
		require.NoError(t, recordFreeFailure(ctx, 1, 300, true))
		require.NoError(t, recordFreeFailure(ctx, 1, 300, true))
		require.NoError(t, recordFreeFailure(ctx, 1, 300, false))
		assert.EqualValues(t, 0, common.RDB.Exists(ctx, freeGroupNamespace+":abuse:failure:1").Val())
	})
	t.Run("slowdown allows spaced requests and disabled rule stops enforcing", func(t *testing.T) {
		server.FlushAll()
		now := time.Now()
		server.SetTime(now)
		current := config
		current.AntiAbuse.IPAggregateRate = model.FreeAbuseRule{Enabled: true, Threshold: 1, WindowSeconds: 60, Action: "slowdown"}
		current.AntiAbuse.Actions.Slowdown = model.FreeActionConfig{Enabled: true, Threshold: 1, WindowSeconds: 60, IntervalSeconds: 10}
		for range 2 {
			_, err := ReserveFreeGroupUsage(ctx, current, 1, "selected", 1, input)
			require.NoError(t, err)
		}
		_, err := ReserveFreeGroupUsage(ctx, current, 1, "selected", 1, input)
		var rejection *FreeGroupRejection
		require.ErrorAs(t, err, &rejection)
		assert.Equal(t, "penalty.slowdown", rejection.Rule)
		assert.EqualValues(t, 10, rejection.RetryAfter)
		server.SetTime(now.Add(11 * time.Second))
		_, err = ReserveFreeGroupUsage(ctx, current, 1, "selected", 1, input)
		require.NoError(t, err)
		current.AntiAbuse.IPAggregateRate.Enabled = false
		_, err = ReserveFreeGroupUsage(ctx, current, 1, "selected", 1, input)
		require.NoError(t, err)
	})
	t.Run("consecutive excess triggers exponential cooldown", func(t *testing.T) {
		server.FlushAll()
		now := time.Now()
		server.SetTime(now)
		current := config
		current.Rate.DailyRequests = model.FreeWindowRule{Enabled: true, Limit: 1}
		current.Penalty.CooldownOnLimit.Limit = 2
		_, err := ReserveFreeGroupUsage(ctx, current, 1, "selected", 1, input)
		require.NoError(t, err)
		_, err = ReserveFreeGroupUsage(ctx, current, 1, "selected", 1, input)
		var rejection *FreeGroupRejection
		require.ErrorAs(t, err, &rejection)
		assert.Equal(t, "rate.daily_requests", rejection.Rule)
		_, err = ReserveFreeGroupUsage(ctx, current, 1, "selected", 1, input)
		require.ErrorAs(t, err, &rejection)
		assert.Equal(t, "penalty.cooldown", rejection.Rule)
		assert.EqualValues(t, 600, rejection.RetryAfter)
		server.SetTime(now.Add(601 * time.Second))
		_, err = ReserveFreeGroupUsage(ctx, current, 1, "selected", 1, input)
		require.ErrorAs(t, err, &rejection)
		assert.Equal(t, "rate.daily_requests", rejection.Rule)
		_, err = ReserveFreeGroupUsage(ctx, current, 1, "selected", 1, input)
		require.ErrorAs(t, err, &rejection)
		assert.EqualValues(t, 1200, rejection.RetryAfter)
		current.Penalty.CooldownMinutes.Enabled = false
		current.Rate.DailyRequests.Enabled = false
		_, err = ReserveFreeGroupUsage(ctx, current, 1, "selected", 1, input)
		require.NoError(t, err)
	})
	t.Run("exemption cannot remove member", func(t *testing.T) {
		server.FlushAll()
		server.SetTime(time.Now())
		current := config
		current.AntiAbuse.IPAggregateRate = model.FreeAbuseRule{Enabled: true, Threshold: 1, WindowSeconds: 60, Action: "remove"}
		current.AntiAbuse.Actions.Remove = model.FreeActionConfig{Enabled: true, Threshold: 1, WindowSeconds: 60}
		current.Penalty.WhitelistUsers = []int{1}
		for range 2 {
			_, err := ReserveFreeGroupUsage(ctx, current, 1, "selected", 1, input)
			require.NoError(t, err)
		}
		var count int64
		require.NoError(t, model.DB.Model(&model.FreeGroupMember{}).Where("user_id = ? AND status = ?", 1, "removed").Count(&count).Error)
		assert.Zero(t, count)
		require.NoError(t, model.DB.Model(&model.FreeGroupAudit{}).Where("user_id = ? AND action = ?", 1, "exempt").Count(&count).Error)
		assert.Positive(t, count)
	})
}

func TestFreeGroupNativeRedis(t *testing.T) {
	connection := os.Getenv("FREE_GROUP_TEST_REDIS")
	if connection == "" {
		t.Skip("FREE_GROUP_TEST_REDIS is not configured")
	}
	options, err := redis.ParseURL(connection)
	require.NoError(t, err)
	options.MaxRetries = -1
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	require.NoError(t, client.Ping(ctx).Err())
	old, enabled := common.RDB, common.RedisEnabled
	common.RDB = client
	common.RedisEnabled = true
	t.Cleanup(func() { common.RDB = old; common.RedisEnabled = enabled })
	keys, err := client.Keys(ctx, freeGroupNamespace+":*").Result()
	require.NoError(t, err)
	if len(keys) > 0 {
		require.NoError(t, client.Del(ctx, keys...).Err())
	}
	t.Cleanup(func() {
		keys, _ := client.Keys(ctx, freeGroupNamespace+":*").Result()
		if len(keys) > 0 {
			_ = client.Del(ctx, keys...).Err()
		}
	})
	config := model.DefaultFreeGroupConfig()
	config.Enabled = true
	config.Concurrency.UserShare.Enabled = false
	config.Concurrency.User.Limit = 1
	config.Rate.RPM.Enabled = false
	config.Rate.TPM.Enabled = false
	reservation, err := ReserveFreeGroupUsage(ctx, config, 1, "selected", 20)
	require.NoError(t, err)
	require.NoError(t, reservation.Settle(ctx, 10))
	require.NoError(t, reservation.Settle(ctx, 10))
	request := FreeConcurrencyRequest{UserID: 1, TokenID: 1, Model: "selected", IP: "192.0.2.1"}
	lease, err := acquireFreeConcurrency(ctx, request, config)
	require.NoError(t, err)
	other := request
	other.TokenID = 2
	_, err = acquireFreeConcurrency(ctx, other, config)
	var rejected *FreeGroupRejection
	require.ErrorAs(t, err, &rejected)
	otherKeys, _, _, _ := freeDimensionKeys(other, config)
	assert.EqualValues(t, 0, client.ZCard(ctx, otherKeys[1]).Val())
	require.NoError(t, lease.RenewLease(ctx))
	lease.Release(ctx)
}

func TestFreeGroupPresetsAndRiskConfirmation(t *testing.T) {
	_, base := freeTestConfig(t)
	freeTestDatabase(t)
	base.Models.Models = []string{"admin-selected"}
	base.Queue.Enabled = false
	previous := 0
	for _, name := range []string{"very_strict", "strict", "standard", "relaxed", "very_relaxed"} {
		t.Run(name, func(t *testing.T) {
			config, err := FreeGroupPreset(base, name)
			require.NoError(t, err)
			require.NoError(t, ValidateFreeGroupConfig(config))
			assert.Equal(t, []string{"admin-selected"}, config.Models.Models)
			assert.Equal(t, base.AccessMode, config.AccessMode)
			assert.False(t, config.Queue.Enabled)
			assert.Greater(t, config.Concurrency.Global.Limit, previous)
			previous = config.Concurrency.Global.Limit
		})
	}
	standard, err := FreeGroupPreset(base, "standard")
	require.NoError(t, err)
	version, err := SaveFreeGroupConfig(standard, 1, 17, "Standard", true)
	require.NoError(t, err)
	relaxed, err := FreeGroupPreset(standard, "relaxed")
	require.NoError(t, err)
	_, err = SaveFreeGroupConfig(relaxed, version, 17, "Relax", false)
	assert.ErrorIs(t, err, ErrFreeGroupRiskConfirmationRequired)
	_, err = SaveFreeGroupConfig(relaxed, version, 17, "Relax", true)
	require.NoError(t, err)
	saved, err := model.GetFreeGroupConfigVersion(version + 1)
	require.NoError(t, err)
	assert.True(t, saved.HighRisk)
	assert.Greater(t, FreeGroupStrength(standard), FreeGroupStrength(relaxed))
	_, err = FreeGroupPreset(base, "unknown")
	require.Error(t, err)
}
