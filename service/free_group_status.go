package service

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/go-redis/redis/v8"
)

type FreeModelUsageStatus struct {
	Model        string `json:"model"`
	Requests     int64  `json:"requests"`
	Tokens       int64  `json:"tokens"`
	RequestLimit *int64 `json:"request_limit"`
	TokenLimit   *int64 `json:"token_limit"`
	RequestsLeft *int64 `json:"requests_left"`
	TokensLeft   *int64 `json:"tokens_left"`
}

type FreeUsageStatus struct {
	ModelQuotas  []FreeModelUsageStatus `json:"model_quotas"`
	Requests     int64                  `json:"requests"`
	Tokens       int64                  `json:"tokens"`
	Concurrency  int64                  `json:"concurrency"`
	CooldownMS   int64                  `json:"cooldown_ms"`
	RequestLimit *int64                 `json:"request_limit"`
	TokenLimit   *int64                 `json:"token_limit"`
	RequestsLeft *int64                 `json:"requests_left"`
	TokensLeft   *int64                 `json:"tokens_left"`
	ResetAt      int64                  `json:"reset_at"`
}

// GetFreeUsageStatus reads independent usage counters, not monetary quota or
// group ratios. Disabled daily limits are represented by null, never zero.
func GetFreeUsageStatus(ctx context.Context, userID int) (*FreeUsageStatus, error) {
	if common.RDB == nil || !common.RedisEnabled {
		return nil, ErrFreeGroupRedis
	}
	config := GetFreeGroupConfig().Config
	location, err := time.LoadLocation(config.Rate.Timezone)
	if err != nil {
		return nil, err
	}
	now := time.Now().In(location)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	prefix := freeGroupNamespace + ":usage:user:" + strconv.Itoa(userID) + ":"
	suffix := ":" + strconv.FormatInt(day.Unix(), 10)
	pipe := common.RDB.Pipeline()
	requests := pipe.HGet(ctx, prefix+"requests"+suffix, "value")
	tokens := pipe.HGet(ctx, prefix+"tokens"+suffix, "value")
	concurrent := pipe.ZCount(ctx, freeGroupNamespace+":concurrency:user:"+strconv.Itoa(userID), "("+strconv.FormatInt(now.UnixMilli(), 10), "+inf")
	cooldown := pipe.HGet(ctx, freeGroupNamespace+":penalty:user:"+strconv.Itoa(userID), "until")
	quotaPrefix := freeGroupNamespace + ":rate:" + strconv.Itoa(userID) + ":"
	defaultRequests := pipe.HGet(ctx, quotaPrefix+"daily_requests"+suffix, "value")
	defaultTokens := pipe.HGet(ctx, quotaPrefix+"daily_tokens"+suffix, "value")
	type modelCounters struct {
		name             string
		limits           model.FreeModelLimit
		requests, tokens *redis.StringCmd
	}
	var modelUsage []modelCounters
	if config.Models.PerModelQuota {
		for name, limits := range config.Models.PerModelLimits {
			if limits.DailyRequests <= 0 && limits.DailyTokens <= 0 {
				continue
			}
			requests, tokens := defaultRequests, defaultTokens
			if limits.DailyRequests > 0 {
				requests = pipe.HGet(ctx, quotaPrefix+"model_requests:"+freeKeyPart(name)+suffix, "value")
			}
			if limits.DailyTokens > 0 {
				tokens = pipe.HGet(ctx, quotaPrefix+"model_tokens:"+freeKeyPart(name)+suffix, "value")
			}
			modelUsage = append(modelUsage, modelCounters{name, limits, requests, tokens})
		}
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, ErrFreeGroupRedis
	}
	usedRequests, err := requests.Int64()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, ErrFreeGroupRedis
	}
	usedTokens, err := tokens.Int64()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, ErrFreeGroupRedis
	}
	until, err := cooldown.Int64()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, ErrFreeGroupRedis
	}
	if concurrent.Err() != nil {
		return nil, ErrFreeGroupRedis
	}
	defaultRequestCount, err := defaultRequests.Int64()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, ErrFreeGroupRedis
	}
	defaultTokenCount, err := defaultTokens.Int64()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, ErrFreeGroupRedis
	}
	status := &FreeUsageStatus{Requests: usedRequests, Tokens: usedTokens, Concurrency: concurrent.Val(), CooldownMS: max(0, until-now.UnixMilli()), ResetAt: day.AddDate(0, 0, 1).Unix()}
	if config.Rate.DailyRequests.Enabled {
		limit, left := config.Rate.DailyRequests.Limit, max(0, config.Rate.DailyRequests.Limit-defaultRequestCount)
		status.RequestLimit, status.RequestsLeft = &limit, &left
	}
	if config.Rate.DailyTokens.Enabled {
		limit, left := config.Rate.DailyTokens.Limit, max(0, config.Rate.DailyTokens.Limit-defaultTokenCount)
		status.TokenLimit, status.TokensLeft = &limit, &left
	}
	status.ModelQuotas = make([]FreeModelUsageStatus, 0, len(modelUsage))
	for _, counters := range modelUsage {
		requests, err := counters.requests.Int64()
		if err != nil && !errors.Is(err, redis.Nil) {
			return nil, ErrFreeGroupRedis
		}
		tokens, err := counters.tokens.Int64()
		if err != nil && !errors.Is(err, redis.Nil) {
			return nil, ErrFreeGroupRedis
		}
		item := FreeModelUsageStatus{Model: counters.name, Requests: requests, Tokens: tokens, RequestLimit: status.RequestLimit, TokenLimit: status.TokenLimit, RequestsLeft: status.RequestsLeft, TokensLeft: status.TokensLeft}
		if counters.limits.DailyRequests > 0 {
			limit, left := counters.limits.DailyRequests, max(0, counters.limits.DailyRequests-requests)
			item.RequestLimit, item.RequestsLeft = &limit, &left
		}
		if counters.limits.DailyTokens > 0 {
			limit, left := counters.limits.DailyTokens, max(0, counters.limits.DailyTokens-tokens)
			item.TokenLimit, item.TokensLeft = &limit, &left
		}
		status.ModelQuotas = append(status.ModelQuotas, item)
	}
	slices.SortFunc(status.ModelQuotas, func(a, b FreeModelUsageStatus) int { return strings.Compare(a.Model, b.Model) })
	return status, nil
}

func GetFreeDashboard(ctx context.Context) (map[string]any, error) {
	if common.RDB == nil || !common.RedisEnabled {
		return nil, ErrFreeGroupRedis
	}
	config := GetFreeGroupConfig().Config
	location, err := time.LoadLocation(config.Rate.Timezone)
	if err != nil {
		return nil, err
	}
	now := time.Now().In(location)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	suffix := ":" + strconv.FormatInt(day.Unix(), 10)
	pipe := common.RDB.Pipeline()
	requests := pipe.HGet(ctx, freeGroupNamespace+":usage:global:requests"+suffix, "value")
	tokens := pipe.HGet(ctx, freeGroupNamespace+":usage:global:tokens"+suffix, "value")
	concurrent := pipe.ZCount(ctx, freeGroupNamespace+":concurrency:global:all", "("+strconv.FormatInt(now.UnixMilli(), 10), "+inf")
	queued := pipe.ZCount(ctx, freeGroupNamespace+":queue:deadlines", "("+strconv.FormatInt(now.UnixMilli(), 10), "+inf")
	metrics := pipe.HGetAll(ctx, freeGroupNamespace+":metrics")
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, ErrFreeGroupRedis
	}
	usedRequests, err := requests.Int64()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, ErrFreeGroupRedis
	}
	usedTokens, err := tokens.Int64()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, ErrFreeGroupRedis
	}
	if concurrent.Err() != nil || queued.Err() != nil || metrics.Err() != nil {
		return nil, ErrFreeGroupRedis
	}
	var rejected, blocked int64
	if err := model.DB.Model(&model.FreeGroupAudit{}).Where("created_at >= ? AND action = ?", day.Unix(), "reject").Count(&rejected).Error; err != nil {
		return nil, err
	}
	if err := model.DB.Model(&model.FreeGroupMember{}).Where("status IN ? AND (expires_at = ? OR expires_at > ?)", []string{model.FreeGroupMemberBlocked, "removed"}, 0, now.Unix()).Count(&blocked).Error; err != nil {
		return nil, err
	}
	return map[string]any{"requests": usedRequests, "tokens": usedTokens, "concurrency": concurrent.Val(), "queued": queued.Val(), "rejected": rejected, "blocked_users": blocked, "metrics": metrics.Val(), "config_version": GetFreeGroupConfig().Version, "ready": freeLoaded.Load()}, nil
}
