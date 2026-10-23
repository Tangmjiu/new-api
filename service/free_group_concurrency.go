package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/google/uuid"
)

// Every key shares a Redis Cluster hash slot. All types are checked before
// mutation: Lua is atomic with respect to other requests, but Redis does not
// roll back writes performed before a script error.
const freeConcurrencyAcquireScript = `
local spec = cjson.decode(ARGV[1])
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2])/1000)
for i,key in ipairs(KEYS) do
 local kind = redis.call('TYPE',key).ok
 local expected = 'zset'
 if i == 1 or i == #KEYS or i == #KEYS-3 or i == #KEYS-4 then expected = 'hash' end
 if kind ~= 'none' and kind ~= expected then return redis.error_reply('free concurrency key type mismatch') end
end
if spec.version>0 and (tonumber(redis.call('HGET',KEYS[1],'version'))~=spec.version or redis.call('HGET',KEYS[1],'enabled')~='true') then return redis.error_reply('free configuration changed') end
local order, deadlines, metrics = KEYS[#KEYS-2],KEYS[#KEYS-1],KEYS[#KEYS]
local ticket=KEYS[#KEYS-3]
local penaltyKey=KEYS[#KEYS-4]
local function reject_capacity(code,dimension,count,length,maximum)
 local p=spec.penalty
 if p and p.cooldown_on_limit.enabled and p.cooldown_minutes.enabled and not spec.exempt then
  local strikes=tonumber(redis.call('HINCRBY',penaltyKey,'limit_strikes',1))
  redis.call('PEXPIRE',penaltyKey,p.maximum_minutes*60000+86400000)
  if strikes>=p.cooldown_on_limit.limit then
   local level=tonumber(redis.call('HGET',penaltyKey,'level')) or 0
   local duration=p.cooldown_minutes.limit*60000
   if p.exponential then duration=duration*p.multiplier^math.min(level,32) end
   duration=math.min(duration,p.maximum_minutes*60000)
   redis.call('HSET',penaltyKey,'until',now+duration,'until_rule','limit','level',level+1,'limit_strikes',0)
   if p.reject_during_cooldown then code=-6;length=duration;maximum=now+duration end
   return {code,dimension,count,length,maximum,strikes,p.cooldown_on_limit.limit,duration}
  end
 end
 return {code,dimension,count,length,maximum}
end
if redis.call('HGET',ticket,'cancelled')=='1' then return {-4,0,0,0,0} end
if not spec.queued and redis.call('EXISTS',ticket)==1 then return {-5,0,0,0,0} end
local stale = redis.call('ZRANGEBYSCORE',deadlines,'-inf',now)
for _,id in ipairs(stale) do redis.call('ZREM',order,id) end
redis.call('ZREMRANGEBYSCORE',deadlines,'-inf',now)
for i,d in ipairs(spec.dimensions) do
 local expired = redis.call('ZREMRANGEBYSCORE',KEYS[i+1],'-inf',now)
 if expired > 0 then redis.call('HINCRBY',metrics,'expired:'..d.name,expired) end
end
local failure = 0
local count = 0
local wait,maxLength=spec.wait,spec.max_length
for i,d in ipairs(spec.dimensions) do
 local used = redis.call('ZCARD',KEYS[i+1])
 if not d.track_only and used >= d.limit then
  if failure == 0 or d.strategy ~= 'queue' then failure = i; count = used end
  if d.strategy ~= 'queue' then break end
  wait=math.min(wait,d.wait);maxLength=math.min(maxLength,d.max_length)
 end
end
local rank = redis.call('ZRANK',order,spec.id)
if spec.queued and not rank then return {-3,0,0,0,0} end
local first = redis.call('ZRANGE',order,0,0)[1]
if failure == 0 and (not first or first == spec.id) then
 local expiry = 9007199254740991
 if spec.ttl > 0 then expiry = now + spec.ttl end
 for i,d in ipairs(spec.dimensions) do
  redis.call('ZADD',KEYS[i+1],expiry,spec.id)
  -- Keep the key after the last lease expires so the next acquire can
  -- count expired leases. Periodic cleanup can use the same script.
  if spec.ttl > 0 then redis.call('PEXPIRE',KEYS[i+1],spec.ttl+86400000) else redis.call('PERSIST',KEYS[i+1]) end
 end
 redis.call('ZREM',order,spec.id); redis.call('ZREM',deadlines,spec.id)
 redis.call('HSET',ticket,'state','admitted');redis.call('PEXPIRE',ticket,60000)
 redis.call('HDEL',penaltyKey,'limit_strikes')
 return {1,0,0,0,0}
end
if not spec.queue or (failure > 0 and spec.dimensions[failure].strategy ~= 'queue') then
 if failure > 0 then redis.call('HINCRBY',metrics,'rejected:'..spec.dimensions[failure].name,1) end
 return reject_capacity(0,failure,count,0,0)
end
if not rank then
 local length = redis.call('ZCARD',order)
 if length >= maxLength then return reject_capacity(-2,failure,count,length,maxLength) end
 -- Sequence stays in the metrics hash; ticket deadlines are independent.
 local sequence = redis.call('HINCRBY',metrics,'queue_sequence',1)
 redis.call('ZADD',order,sequence,spec.id)
 redis.call('ZADD',deadlines,now+wait,spec.id)
 redis.call('HSET',ticket,'state','queued','user_id',spec.user_id,'token_id',spec.token_id,'started',now)
 redis.call('PEXPIRE',ticket,wait+60000)
 rank = redis.call('ZRANK',order,spec.id)
 redis.call('PEXPIRE',order,86400000+spec.wait)
 redis.call('PEXPIRE',deadlines,86400000+spec.wait)
end
-- A newly saturated dimension may impose a tighter bound on existing work.
if failure>0 then
 local deadline=tonumber(redis.call('ZSCORE',deadlines,spec.id))
 local started=tonumber(redis.call('HGET',ticket,'started'))
 if deadline>started+wait then redis.call('ZADD',deadlines,started+wait,spec.id) end
end
return {2,failure,count,rank+1,tonumber(redis.call('ZSCORE',deadlines,spec.id))-now}
`

const freeConcurrencyReleaseScript = `
for _,key in ipairs(KEYS) do
 local kind = redis.call('TYPE',key).ok
 if kind ~= 'none' and kind ~= 'zset' then return redis.error_reply('free lease key type mismatch') end
end
for _,key in ipairs(KEYS) do redis.call('ZREM',key,ARGV[1]) end
return 1
`

const freeConcurrencyRenewScript = `
local clock = redis.call('TIME')
local now = tonumber(clock[1])*1000+math.floor(tonumber(clock[2])/1000)
local length=#KEYS
local reservation=nil
if ARGV[3]=='1' then
 reservation=KEYS[length];length=length-1
 if redis.call('TYPE',reservation).ok~='hash' or not redis.call('HGET',reservation,'reserved') or redis.call('HGET',reservation,'settled') then return 0 end
end
for i=1,length do
 local key=KEYS[i]
 local kind = redis.call('TYPE',key).ok
 if kind ~= 'none' and kind ~= 'zset' then return redis.error_reply('free lease key type mismatch') end
 local expires = redis.call('ZSCORE',key,ARGV[1])
 if not expires or tonumber(expires) <= now then return 0 end
end
if tonumber(ARGV[2])>0 then
 for i=1,length do
  local key=KEYS[i]
  redis.call('ZADD',key,now+tonumber(ARGV[2]),ARGV[1])
  redis.call('PEXPIRE',key,tonumber(ARGV[2])+86400000)
 end
end
if reservation then redis.call('PEXPIRE',reservation,86400000) end
return 1
`

type FreeConcurrencyRequest struct {
	UserID  int
	TokenID int
	IP      string
	Model   string
	Queue   bool
	QueueID string
	// Called on the request goroutine; HTTP clients can poll a separate status
	// endpoint without writing nonstandard bytes into an OpenAI SSE stream.
	OnQueue func(position int64, remaining time.Duration)
}

type freeConcurrencyDimension struct {
	Name      string `json:"name"`
	Limit     int64  `json:"limit"`
	Strategy  string `json:"strategy"`
	Wait      int64  `json:"wait"`
	MaxLength int    `json:"max_length"`
	TrackOnly bool   `json:"track_only"`
}

type FreeConcurrencyLease struct {
	ReservationKey string
	ID             string
	Keys           []string
	TTL            time.Duration
	Renew          model.FreeRenewConfig
	closed         atomic.Bool
}

func freeKeyPart(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }

func freeDimensionKeys(request FreeConcurrencyRequest, config model.FreeGroupConfig) ([]string, []freeConcurrencyDimension, int64, int) {
	keys := make([]string, 0, 6)
	dimensions := make([]freeConcurrencyDimension, 0, 6)
	wait, length := config.Queue.MaxWaitSeconds, config.Queue.MaxLength
	entries := []struct {
		name, identity string
		rule           model.FreeConcurrencyDimension
	}{
		{"user", strconv.Itoa(request.UserID), config.Concurrency.User},
		{"token", strconv.Itoa(request.TokenID), config.Concurrency.Token},
		{"ip", freeKeyPart(request.IP), config.Concurrency.IP},
		{"model", freeKeyPart(request.Model), config.Concurrency.Model},
		{"global", "all", config.Concurrency.Global},
	}
	if config.Models.PerModelConcurrency {
		if override, ok := config.Models.PerModelLimits[request.Model]; ok && override.Concurrency > 0 {
			entries[3].rule.Enabled = true
			entries[3].rule.Limit = override.Concurrency
		}
	}
	if config.Concurrency.UserShare.Enabled {
		rule := model.FreeConcurrencyDimension{Enabled: true, Limit: max(1, int(math.Ceil(float64(config.Concurrency.Global.Limit)*config.Concurrency.UserShare.Percent/100))), Strategy: config.Concurrency.UserShare.Strategy, MaxWaitSeconds: config.Concurrency.UserShare.MaxWaitSeconds, MaxQueueLength: config.Concurrency.UserShare.MaxQueueLength}
		entries = append(entries, struct {
			name, identity string
			rule           model.FreeConcurrencyDimension
		}{"user_share", strconv.Itoa(request.UserID), rule})
	}
	for _, entry := range entries {
		trackOnly := !entry.rule.Enabled
		if trackOnly && entry.name != "user" && entry.name != "global" {
			continue
		}
		keys = append(keys, freeGroupNamespace+":concurrency:"+entry.name+":"+entry.identity)
		dimensions = append(dimensions, freeConcurrencyDimension{Name: entry.name, Limit: int64(entry.rule.Limit), Strategy: entry.rule.Strategy, Wait: entry.rule.MaxWaitSeconds * 1000, MaxLength: entry.rule.MaxQueueLength, TrackOnly: trackOnly})
	}
	return keys, dimensions, wait, length
}

func AcquireFreeConcurrency(ctx context.Context, request FreeConcurrencyRequest) (*FreeConcurrencyLease, error) {
	return acquireFreeConcurrency(ctx, request, GetFreeGroupConfig().Config)
}

func acquireFreeConcurrency(ctx context.Context, request FreeConcurrencyRequest, config model.FreeGroupConfig, versions ...int64) (*FreeConcurrencyLease, error) {
	if !config.Enabled {
		return nil, ErrFreeGroupDisabled
	}
	if common.RDB == nil || !common.RedisEnabled {
		return nil, ErrFreeGroupRedis
	}
	keys, dimensions, wait, length := freeDimensionKeys(request, config)
	ttl := time.Duration(0)
	if config.Concurrency.LeaseTTLEnabled {
		ttl = time.Duration(config.Concurrency.LeaseTTLSeconds) * time.Second
	}
	id := uuid.NewString()
	if request.QueueID != "" {
		parsed, err := uuid.Parse(request.QueueID)
		if err != nil {
			return nil, &FreeGroupRejection{Rule: "queue.id", Status: 400, Recovery: "valid_uuid"}
		}
		id = parsed.String()
	}
	// User identity is part of the ticket to prevent another account from
	// replacing or cancelling a caller-supplied queue identifier.
	id = strconv.Itoa(request.UserID) + ":" + id
	lease := &FreeConcurrencyLease{ID: id, Keys: keys, TTL: ttl, Renew: config.Concurrency.Renew}
	expectedVersion := int64(0)
	if len(versions) > 0 {
		expectedVersion = versions[0]
	}
	scriptKeys := append(append([]string{freeGroupNamespace + ":config"}, keys...), freeGroupNamespace+":penalty:user:"+strconv.Itoa(request.UserID), freeGroupNamespace+":queue:ticket:"+id, freeGroupNamespace+":queue:order", freeGroupNamespace+":queue:deadlines", freeGroupNamespace+":metrics")
	spec := struct {
		ID         string                       `json:"id"`
		Dimensions []freeConcurrencyDimension   `json:"dimensions"`
		TTL        int64                        `json:"ttl"`
		Queue      bool                         `json:"queue"`
		Queued     bool                         `json:"queued"`
		Wait       int64                        `json:"wait"`
		MaxLength  int                          `json:"max_length"`
		Version    int64                        `json:"version"`
		UserID     int                          `json:"user_id"`
		TokenID    int                          `json:"token_id"`
		Penalty    model.FreeGroupPenaltyConfig `json:"penalty"`
		Exempt     bool                         `json:"exempt"`
	}{ID: lease.ID, Dimensions: dimensions, TTL: ttl.Milliseconds(), Queue: request.Queue && config.Queue.Enabled, Wait: wait * 1000, MaxLength: length, Version: expectedVersion, UserID: request.UserID, TokenID: request.TokenID, Penalty: config.Penalty, Exempt: config.Penalty.WhitelistEnabled && (slices.Contains(config.Penalty.WhitelistUsers, request.UserID) || slices.Contains(config.Penalty.WhitelistIPs, request.IP))}
	started := time.Now()
	defer func() {
		if spec.Queued {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := common.RDB.Eval(cleanupCtx, freeConcurrencyReleaseScript, scriptKeys[len(keys)+3:len(keys)+5], lease.ID).Err(); err != nil {
				common.SysError("Free queue ticket cleanup failed")
			}
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Config changes invalidate queued work before it can reach the upstream.
		if snapshot := GetFreeGroupConfig(); expectedVersion > 0 && (!snapshot.Ready || snapshot.Version != expectedVersion) {
			return nil, ErrFreeGroupRedis
		}
		if spec.Queued && GetFreeGroupConfig().Config.Enabled == false {
			return nil, ErrFreeGroupDisabled
		}
		encoded, err := common.Marshal(spec)
		if err != nil {
			return nil, err
		}
		result, err := common.RDB.Eval(ctx, freeConcurrencyAcquireScript, scriptKeys, string(encoded)).Int64Slice()
		if err != nil || (len(result) != 5 && len(result) != 8) {
			return nil, ErrFreeGroupRedis
		}
		if len(result) == 8 {
			input := &FreeAbuseContext{UserID: request.UserID, IP: request.IP, Version: expectedVersion}
			if err := recordFreeAbuseEvents(input, []freeAbuseEvent{{Rule: "penalty.consecutive_limits", Current: float64(result[5]), Limit: float64(result[6]), Action: "cooldown", Window: result[7]}}); err != nil {
				return nil, err
			}
		}
		switch result[0] {
		case 1:
			if spec.Queued {
				freeQueueWaitSeconds.Observe(time.Since(started).Seconds())
			}
			return lease, nil
		case -2:
			return nil, &FreeGroupRejection{Rule: "queue.length", Current: float64(result[3]), Limit: float64(result[4]), Status: 429, Recovery: "capacity_available"}
		case -3:
			return nil, &FreeGroupRejection{Rule: "queue.wait", Current: time.Since(started).Seconds(), Limit: float64(wait), Status: 429, Recovery: "retry"}
		case -4:
			return nil, &FreeGroupRejection{Rule: "queue.cancelled", Status: 409, Recovery: "new_request"}
		case -5:
			return nil, &FreeGroupRejection{Rule: "queue.duplicate", Status: 409, Recovery: "new_queue_id"}
		case -6:
			return nil, &FreeGroupRejection{Rule: "penalty.cooldown", Current: float64(result[5]), Limit: float64(result[6]), Status: 429, RetryAfter: (result[3] + 999) / 1000, ResetAt: result[4] / 1000, Recovery: "cooldown_or_manual_release"}
		case 0:
			name := "queue.fairness"
			limit := int64(length)
			if result[1] > 0 {
				dimension := dimensions[result[1]-1]
				name = "concurrency." + dimension.Name
				limit = dimension.Limit
			}
			return nil, &FreeGroupRejection{Rule: name, Current: float64(result[2]), Limit: float64(limit), Status: 429, RetryAfter: 1, Recovery: "capacity_available"}
		case 2:
			spec.Queued = true
			if request.OnQueue != nil {
				request.OnQueue(result[3], time.Duration(result[4])*time.Millisecond)
			}
		default:
			return nil, ErrFreeGroupRedis
		}
		timer := time.NewTimer(time.Duration(config.Queue.PollMillis) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (lease *FreeConcurrencyLease) Release(ctx context.Context) {
	if lease == nil || lease.closed.Swap(true) || len(lease.Keys) == 0 {
		return
	}
	if common.RDB == nil {
		return
	}
	if err := common.RDB.Eval(ctx, freeConcurrencyReleaseScript, lease.Keys, lease.ID).Err(); err != nil {
		freeComponentErrors.WithLabelValues("release").Inc()
		common.SysError("Free concurrency release failed; lease expiry will reclaim capacity")
	}
}

func (lease *FreeConcurrencyLease) RenewLease(ctx context.Context) error {
	if lease == nil || lease.closed.Load() {
		return ErrFreeGroupRedis
	}
	if (len(lease.Keys) == 0 || lease.TTL == 0) && lease.ReservationKey == "" {
		return nil
	}
	if common.RDB == nil || !common.RedisEnabled {
		return ErrFreeGroupRedis
	}
	keys := lease.Keys
	hasReservation := 0
	if lease.ReservationKey != "" {
		keys = append(append([]string{}, keys...), lease.ReservationKey)
		hasReservation = 1
	}
	result, err := common.RDB.Eval(ctx, freeConcurrencyRenewScript, keys, lease.ID, lease.TTL.Milliseconds(), hasReservation).Int()
	if err != nil || result != 1 {
		freeComponentErrors.WithLabelValues("renew").Inc()
		return ErrFreeGroupRedis
	}
	return nil
}

type FreeQueueStatus struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	Position    int64  `json:"position"`
	RemainingMS int64  `json:"remaining_ms"`
	Cancellable bool   `json:"cancellable"`
}

// InspectFreeQueue restricts reads and cancellation to the authenticated user.
// Cancellation never removes a running request's concurrency lease.
func InspectFreeQueue(ctx context.Context, userID int, queueID string, cancel bool) (*FreeQueueStatus, error) {
	parsed, err := uuid.Parse(queueID)
	if err != nil || userID <= 0 {
		return nil, &FreeGroupRejection{Rule: "queue.id", Status: 400, Recovery: "valid_uuid"}
	}
	if common.RDB == nil || !common.RedisEnabled {
		return nil, ErrFreeGroupRedis
	}
	id := strconv.Itoa(userID) + ":" + parsed.String()
	keys := []string{freeGroupNamespace + ":queue:ticket:" + id, freeGroupNamespace + ":queue:order", freeGroupNamespace + ":queue:deadlines"}
	raw, err := common.RDB.Eval(ctx, `
local state=redis.call('HGET',KEYS[1],'state')
if not state then return {'missing',0,0} end
local clock=redis.call('TIME');local now=tonumber(clock[1])*1000+math.floor(tonumber(clock[2])/1000)
local deadline=tonumber(redis.call('ZSCORE',KEYS[3],ARGV[1])) or 0
local rank=redis.call('ZRANK',KEYS[2],ARGV[1])
if state=='queued' and (not rank or deadline<=now) then state='expired' end
if state=='queued' and ARGV[2]=='1' then
 redis.call('HSET',KEYS[1],'state','cancelled','cancelled',1)
 redis.call('PEXPIRE',KEYS[1],60000)
 redis.call('ZREM',KEYS[2],ARGV[1]);redis.call('ZREM',KEYS[3],ARGV[1])
 state='cancelled';rank=nil
end
return {state,rank and rank+1 or 0,math.max(0,deadline-now)}
`, keys, id, map[bool]string{false: "0", true: "1"}[cancel]).Slice()
	if err != nil || len(raw) != 3 {
		return nil, ErrFreeGroupRedis
	}
	state := fmt.Sprint(raw[0])
	if state == "missing" {
		return nil, &FreeGroupRejection{Rule: "queue.not_found", Status: 404, Recovery: "new_request"}
	}
	position, err := strconv.ParseInt(fmt.Sprint(raw[1]), 10, 64)
	if err != nil {
		return nil, ErrFreeGroupRedis
	}
	remaining, err := strconv.ParseInt(fmt.Sprint(raw[2]), 10, 64)
	if err != nil {
		return nil, ErrFreeGroupRedis
	}
	return &FreeQueueStatus{ID: parsed.String(), State: state, Position: position, RemainingMS: remaining, Cancellable: state == "queued"}, nil
}

// ListFreeUserQueue returns only the caller's live tickets. It never exposes
// account identifiers or queue IDs belonging to another user.
func ListFreeUserQueue(ctx context.Context, userID int) ([]FreeQueueStatus, error) {
	items := make([]FreeQueueStatus, 0)
	config := GetFreeGroupConfig()
	if !config.Ready || !config.Config.Enabled || !config.Config.Queue.Enabled {
		return items, nil
	}
	if common.RDB == nil || !common.RedisEnabled {
		return nil, ErrFreeGroupRedis
	}
	raw, err := common.RDB.Eval(ctx, `local entries=redis.call('ZRANGE',KEYS[1],0,9999);local clock=redis.call('TIME');local now=tonumber(clock[1])*1000+math.floor(tonumber(clock[2])/1000);local items={};for i,id in ipairs(entries) do if string.sub(id,1,string.len(ARGV[1]))==ARGV[1] then local deadline=tonumber(redis.call('ZSCORE',KEYS[2],id)) or 0;if deadline>now then table.insert(items,{id=string.sub(id,string.len(ARGV[1])+1),state='queued',position=i,remaining_ms=deadline-now,cancellable=true}) end end end;if #items==0 then return '[]' end;return cjson.encode(items)`, []string{freeGroupNamespace + ":queue:order", freeGroupNamespace + ":queue:deadlines"}, strconv.Itoa(userID)+":").Text()
	if err != nil {
		return nil, ErrFreeGroupRedis
	}
	if err := common.UnmarshalJsonStr(raw, &items); err != nil {
		return nil, ErrFreeGroupRedis
	}
	return items, nil
}
