package service

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/google/uuid"
)

// One round trip checks every rate/quota rule before mutating any counter.
// Calendar boundaries are computed in the configured timezone, including DST.
// Token amounts are reservations; settlement only adjusts token dimensions.
const freeRateTakeScript = `
local spec=cjson.decode(ARGV[1])
local clock=redis.call('TIME')
local now=tonumber(clock[1])*1000+math.floor(tonumber(clock[2])/1000)
for _,key in ipairs(KEYS) do
 local kind=redis.call('TYPE',key).ok
 if kind~='none' and kind~='hash' then return redis.error_reply('free rate key type mismatch') end
end
if spec.config_index and spec.config_index>0 and (tonumber(redis.call('HGET',KEYS[spec.config_index],'version'))~=spec.version or redis.call('HGET',KEYS[spec.config_index],'enabled')~='true') then return redis.error_reply('free configuration changed') end
` + freeAbusePrelude + `
local states={}
local credit,creditUsed=0,0
if spec.idle then
 local saved=redis.call('HMGET',KEYS[spec.idle.index],'credit','updated')
 credit=math.min(spec.idle.maximum,(tonumber(saved[1]) or 0)+math.max(0,now-(tonumber(saved[2]) or now))*spec.idle.accrual/3600000)
end
local granted=spec.tokens
if spec.elastic then
 granted=spec.maximum
 for i,r in ipairs(spec.rules) do
  if r.tokens and not r.unlimited then
   local values=redis.call('HMGET',KEYS[i],'value','updated')
   local value=tonumber(values[1]) or 0
   local available=r.limit-value
   if r.bucket then
    local updated=tonumber(values[2]) or now
    if not values[1] then value=r.capacity end
    value=math.min(r.capacity,value+math.max(0,now-updated)*r.limit/60000)
    available=value
   end
   if available<spec.minimum then return reject_limit(i,spec.minimum,math.max(1,r.bucket and math.ceil((spec.minimum-available)*60000/r.limit) or r.reset-now)) end
   granted=math.min(granted,math.floor(available))
  end
 end
end
for i,r in ipairs(spec.rules) do
 if spec.elastic and r.tokens then r.amount=granted end
 local values=redis.call('HMGET',KEYS[i],'value','updated')
 local value=tonumber(values[1]) or 0
 if r.bucket then
  local updated=tonumber(values[2]) or now
  if not values[1] then value=r.capacity end
  value=math.min(r.capacity,value+math.max(0,now-updated)*r.limit/60000)
  if value<r.amount then
   return reject_limit(i,r.capacity-value,math.ceil((r.amount-value)*60000/r.limit))
  end
 else
  if not r.unlimited and value+r.amount>r.limit then
   local needed=math.max(0,value+r.amount-r.limit)-math.max(0,value-r.limit)
   if r.credit and math.floor(credit)>=needed then creditUsed=needed else return reject_limit(i,value+r.amount,math.max(0,r.reset-now)) end
  end
 end
 states[i]=value
end
for i,r in ipairs(spec.rules) do
 local value=states[i]+r.amount
 if r.bucket then value=states[i]-r.amount end
 redis.call('HSET',KEYS[i],'value',value,'updated',now)
 redis.call('PEXPIREAT',KEYS[i],r.expiry)
end
redis.call('HSET',KEYS[#KEYS],'reserved',granted,'credit_used',creditUsed)
redis.call('PEXPIRE',KEYS[#KEYS],86400000)
if spec.idle then
 redis.call('HSET',KEYS[spec.idle.index],'credit',credit-creditUsed,'updated',now)
 redis.call('PEXPIRE',KEYS[spec.idle.index],spec.idle.ttl)
end
-- Live relay heartbeats extend the idempotency record. A crashed process
-- leaves conservative quota counters, but no permanent reservation hashes.
if penalty and not penalty.exempt then
 local slowUntil=tonumber(redis.call('HGET',stateKey,'slow_until')) or 0
 local slowRule=redis.call('HGET',stateKey,'slow_rule')
 if slowUntil>now and active_rule(slowRule,'slowdown') then
  redis.call('HSET',stateKey,'slow_next',now+(tonumber(redis.call('HGET',stateKey,'slow_interval')) or 1000))
 end
end
return {1,0,granted,0,now,(#events==0 and '[]' or cjson.encode(events))}
`

const freeRateSettleScript = `
local spec=cjson.decode(ARGV[1])
for _,key in ipairs(KEYS) do
 local kind=redis.call('TYPE',key).ok
 if kind~='none' and kind~='hash' then return redis.error_reply('free rate key type mismatch') end
end
local reservation=KEYS[#KEYS]
if redis.call('HGET',reservation,'settled') then return 0 end
if not redis.call('HGET',reservation,'reserved') then return redis.error_reply('free reservation missing') end
if spec.cancel and spec.idle then
 local used=tonumber(redis.call('HGET',reservation,'credit_used')) or 0
 local current=tonumber(redis.call('HGET',KEYS[spec.idle.index],'credit')) or 0
 if used>0 then redis.call('HSET',KEYS[spec.idle.index],'credit',math.min(spec.idle.maximum,current+used)) end
end
for i,r in ipairs(spec.rules) do
 if (r.tokens or spec.cancel) and redis.call('EXISTS',KEYS[i])==1 then
  local delta=spec.actual-spec.reserved
  if spec.cancel then delta=-r.amount end
  local value=tonumber(redis.call('HGET',KEYS[i],'value')) or 0
  if r.bucket then value=math.min(r.capacity,value-delta) else value=math.max(0,value+delta) end
  redis.call('HSET',KEYS[i],'value',value)
 end
end
redis.call('HSET',reservation,'settled',1,'actual',spec.actual)
redis.call('PEXPIRE',reservation,86400000)
return 1
`

type freeRateRule struct {
	Name      string `json:"name"`
	Limit     int64  `json:"limit"`
	Amount    int64  `json:"amount"`
	Capacity  int64  `json:"capacity"`
	Bucket    bool   `json:"bucket"`
	Tokens    bool   `json:"tokens"`
	Reset     int64  `json:"reset"`
	Expiry    int64  `json:"expiry"`
	Unlimited bool   `json:"unlimited"`
	Credit    bool   `json:"credit"`
}

type FreeRateReservation struct {
	Keys     []string
	Rules    []freeRateRule
	Reserved int64
	Idle     *freeIdleCreditSpec
}

type freeIdleCreditSpec struct {
	Index   int   `json:"index"`
	Accrual int64 `json:"accrual"`
	Maximum int64 `json:"maximum"`
	TTL     int64 `json:"ttl"`
}

func freeRateRules(config model.FreeGroupConfig, userID int, modelName string, tokens int64, now time.Time) ([]string, []freeRateRule, error) {
	location, err := time.LoadLocation(config.Rate.Timezone)
	if err != nil {
		return nil, nil, err
	}
	now = now.In(location)
	keys := make([]string, 0, 12)
	rules := make([]freeRateRule, 0, 12)
	prefix := freeGroupNamespace + ":rate:" + strconv.Itoa(userID) + ":"
	for _, entry := range []struct {
		name   string
		rule   model.FreeBurstRule
		tokens bool
	}{{"rpm", config.Rate.RPM, false}, {"tpm", config.Rate.TPM, true}} {
		if !entry.rule.Enabled {
			continue
		}
		amount := int64(1)
		if entry.tokens {
			amount = tokens
		}
		keys = append(keys, prefix+entry.name)
		rules = append(rules, freeRateRule{Name: entry.name, Limit: entry.rule.Limit, Amount: amount, Capacity: entry.rule.Burst, Bucket: true, Tokens: entry.tokens, Expiry: now.Add(time.Duration(max(60, entry.rule.Burst*60/entry.rule.Limit))*time.Second + time.Hour).UnixMilli()})
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	hour := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, location)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, location)
	dailyRequests, dailyTokens := config.Rate.DailyRequests, config.Rate.DailyTokens
	requestName, tokenName := "daily_requests", "daily_tokens"
	if config.Models.PerModelQuota {
		if override, ok := config.Models.PerModelLimits[modelName]; ok {
			if override.DailyRequests > 0 {
				dailyRequests = model.FreeWindowRule{Enabled: true, Limit: override.DailyRequests}
				requestName = "model_requests:" + freeKeyPart(modelName)
			}
			if override.DailyTokens > 0 {
				dailyTokens = model.FreeWindowRule{Enabled: true, Limit: override.DailyTokens}
				tokenName = "model_tokens:" + freeKeyPart(modelName)
			}
		}
	}
	entries := []struct {
		name       string
		rule       model.FreeWindowRule
		tokens     bool
		start, end time.Time
	}{
		{"hourly_requests", config.Rate.HourlyRequests, false, hour, hour.Add(time.Hour)},
		{"hourly_tokens", config.Rate.HourlyTokens, true, hour, hour.Add(time.Hour)},
		{requestName, dailyRequests, false, day, day.AddDate(0, 0, 1)},
		{tokenName, dailyTokens, true, day, day.AddDate(0, 0, 1)},
		{"monthly_requests", config.Rate.MonthlyRequests, false, month, month.AddDate(0, 1, 0)},
		{"monthly_tokens", config.Rate.MonthlyTokens, true, month, month.AddDate(0, 1, 0)},
	}
	for _, entry := range entries {
		if !entry.rule.Enabled {
			continue
		}
		amount := int64(1)
		if entry.tokens {
			amount = tokens
		}
		keys = append(keys, prefix+entry.name+":"+strconv.FormatInt(entry.start.Unix(), 10))
		rules = append(rules, freeRateRule{Name: entry.name, Limit: entry.rule.Limit, Amount: amount, Tokens: entry.tokens, Reset: entry.end.UnixMilli(), Expiry: entry.end.Add(24 * time.Hour).UnixMilli(), Credit: config.Rate.IdleCredit.Enabled && entry.name == requestName})
	}
	// Usage remains visible even when every quota rule is explicitly disabled.
	for _, scope := range []string{"user:" + strconv.Itoa(userID), "global"} {
		for _, metric := range []string{"requests", "tokens"} {
			amount := int64(1)
			if metric == "tokens" {
				amount = tokens
			}
			keys = append(keys, freeGroupNamespace+":usage:"+scope+":"+metric+":"+strconv.FormatInt(day.Unix(), 10))
			rules = append(rules, freeRateRule{Name: "usage." + scope + "." + metric, Amount: amount, Tokens: metric == "tokens", Unlimited: true, Reset: day.AddDate(0, 0, 1).UnixMilli(), Expiry: day.AddDate(0, 0, 2).UnixMilli()})
		}
	}
	return keys, rules, nil
}

func ReserveFreeGroupUsage(ctx context.Context, config model.FreeGroupConfig, userID int, modelName string, tokens int64, abuseInputs ...*FreeAbuseContext) (*FreeRateReservation, error) {
	return reserveFreeGroupUsage(ctx, config, userID, modelName, tokens, false, tokens, abuseInputs...)
}

func reserveFreeGroupUsage(ctx context.Context, config model.FreeGroupConfig, userID int, modelName string, tokens int64, elastic bool, maximum int64, abuseInputs ...*FreeAbuseContext) (*FreeRateReservation, error) {
	if common.RDB == nil || !common.RedisEnabled {
		return nil, ErrFreeGroupRedis
	}
	if tokens < 0 || tokens > 2147483647 {
		return nil, fmt.Errorf("invalid Free token reservation")
	}
	keys, rules, err := freeRateRules(config, userID, modelName, tokens, time.Now())
	if err != nil {
		return nil, err
	}
	var abuse *FreeAbuseContext
	if len(abuseInputs) > 0 {
		abuse = abuseInputs[0]
	}
	keys, observations, penalty := freeAbusePlan(config, abuse, keys)
	var idle *freeIdleCreditSpec
	if config.Rate.IdleCredit.Enabled {
		keys = append(keys, freeGroupNamespace+":credit:user:"+strconv.Itoa(userID))
		idle = &freeIdleCreditSpec{Index: len(keys), Accrual: config.Rate.IdleCredit.AccrualPerHour, Maximum: config.Rate.IdleCredit.Maximum, TTL: (config.Rate.IdleCredit.Maximum/max(1, config.Rate.IdleCredit.AccrualPerHour) + 48) * 3600000}
	}
	configIndex, version := 0, int64(0)
	if abuse != nil && abuse.Version > 0 {
		keys = append(keys, freeGroupNamespace+":config")
		configIndex = len(keys)
		version = abuse.Version
	}
	keys = append(keys, freeGroupNamespace+":reservation:"+uuid.NewString())
	minimum := tokens + 1
	if abuse != nil {
		minimum = tokens + max(1, abuse.OutputMultiplicity)
	}
	spec, err := common.Marshal(struct {
		Rules        []freeRateRule         `json:"rules"`
		Tokens       int64                  `json:"tokens"`
		Observations []freeAbuseObservation `json:"observations,omitempty"`
		Penalty      *freePenaltySpec       `json:"penalty,omitempty"`
		ConfigIndex  int                    `json:"config_index,omitempty"`
		Version      int64                  `json:"version"`
		Elastic      bool                   `json:"elastic"`
		Minimum      int64                  `json:"minimum"`
		Maximum      int64                  `json:"maximum"`
		Idle         *freeIdleCreditSpec    `json:"idle,omitempty"`
	}{rules, tokens, observations, penalty, configIndex, version, elastic, minimum, maximum, idle})
	if err != nil {
		return nil, err
	}
	raw, err := common.RDB.Eval(ctx, freeRateTakeScript, keys, string(spec)).Slice()
	if err != nil || len(raw) != 6 {
		return nil, ErrFreeGroupRedis
	}
	values := make([]int64, 5)
	for i := range values {
		values[i], err = strconv.ParseInt(fmt.Sprint(raw[i]), 10, 64)
		if err != nil {
			return nil, ErrFreeGroupRedis
		}
	}
	var events []freeAbuseEvent
	if values[0] == 1 && elastic {
		tokens = values[2]
		for i := range rules {
			if rules[i].Tokens {
				rules[i].Amount = tokens
			}
		}
	}
	encodedEvents, ok := raw[5].(string)
	if !ok {
		return nil, ErrFreeGroupRedis
	}
	if err := common.UnmarshalJsonStr(encodedEvents, &events); err != nil {
		return nil, ErrFreeGroupRedis
	}
	if err := recordFreeAbuseEvents(abuse, events); err != nil {
		if values[0] == 1 {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			reservation := &FreeRateReservation{Keys: keys, Rules: rules, Reserved: tokens, Idle: idle}
			if settleErr := reservation.Cancel(cleanupCtx); settleErr != nil {
				freeComponentErrors.WithLabelValues("refund").Inc()
			}
		}
		return nil, fmt.Errorf("record Free abuse decision: %w", err)
	}
	if values[0] == -1 {
		return nil, &FreeGroupRejection{Rule: "penalty.cooldown", Status: 429, RetryAfter: (values[3] + 999) / 1000, ResetAt: (values[4] + values[3]) / 1000, Recovery: "cooldown_or_manual_release"}
	}
	if values[0] == -2 {
		return nil, &FreeGroupRejection{Rule: "penalty.slowdown", Status: 429, RetryAfter: (values[3] + 999) / 1000, ResetAt: (values[4] + values[3]) / 1000, Recovery: "reduced_frequency"}
	}
	if values[0] != 1 {
		if values[1] < 1 || values[1] > int64(len(rules)) {
			return nil, ErrFreeGroupRedis
		}
		rule := rules[values[1]-1]
		retry := (values[3] + 999) / 1000
		return nil, &FreeGroupRejection{Rule: "rate." + rule.Name, Current: float64(values[2]), Limit: float64(rule.Limit), Status: 429, RetryAfter: retry, ResetAt: (values[4] + values[3]) / 1000, Recovery: "reset"}
	}
	return &FreeRateReservation{Keys: keys, Rules: rules, Reserved: tokens, Idle: idle}, nil
}

func (reservation *FreeRateReservation) Settle(ctx context.Context, actual int64) error {
	return reservation.settle(ctx, actual, false)
}

// Cancel returns every reservation when a request never reached the relay,
// including request counts and bucket capacity consumed before queueing.
func (reservation *FreeRateReservation) Cancel(ctx context.Context) error {
	return reservation.settle(ctx, 0, true)
}

func (reservation *FreeRateReservation) settle(ctx context.Context, actual int64, canceled bool) error {
	if reservation == nil {
		return nil
	}
	if actual < 0 || actual > 2147483647 {
		return fmt.Errorf("invalid Free token usage")
	}
	if common.RDB == nil {
		return ErrFreeGroupRedis
	}
	spec, err := common.Marshal(struct {
		Rules    []freeRateRule      `json:"rules"`
		Reserved int64               `json:"reserved"`
		Actual   int64               `json:"actual"`
		Cancel   bool                `json:"cancel"`
		Idle     *freeIdleCreditSpec `json:"idle,omitempty"`
	}{reservation.Rules, reservation.Reserved, actual, canceled, reservation.Idle})
	if err != nil {
		return err
	}
	if err := common.RDB.Eval(ctx, freeRateSettleScript, reservation.Keys, string(spec)).Err(); err != nil {
		freeComponentErrors.WithLabelValues("settle").Inc()
		return ErrFreeGroupRedis
	}
	return nil
}
