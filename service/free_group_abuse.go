package service

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
)

var freeNotificationSlots = make(chan struct{}, 16)

// Penalty preflight and atomic rate reservation share these checks. They never
// return prompt text, credentials, or session tokens.
const freePenaltyPrelude = `
local events={}
local penalty=spec.penalty
local stateKey=nil
local function encode_events() return #events==0 and '[]' or cjson.encode(events) end
local function active_rule(name,action)
 if name=='limit' then return penalty.limit_enabled and penalty.duration_enabled end
 for _,o in ipairs(spec.observations or {}) do
  if o.name==name and o.action==action and o.action_enabled then return true end
 end
 return name=='manual'
end
if penalty then
 stateKey=KEYS[penalty.index]
 for _,index in ipairs(penalty.indices) do
  local key=KEYS[index]
  local untilTime=tonumber(redis.call('HGET',key,'until')) or 0
  local rule=redis.call('HGET',key,'until_rule') or 'manual'
  if penalty.reject and not penalty.exempt and untilTime>now and active_rule(rule,'cooldown') then
   return {-1,0,0,untilTime-now,now,'[]'}
  end
  local slowUntil=tonumber(redis.call('HGET',key,'slow_until')) or 0
  local slowRule=redis.call('HGET',key,'slow_rule')
  local nextTime=tonumber(redis.call('HGET',key,'slow_next')) or 0
  if not penalty.exempt and slowUntil>now and nextTime>now and active_rule(slowRule,'slowdown') then
   return {-2,0,0,nextTime-now,now,'[]'}
  end
 end
end
`

const freeAbusePrelude = freePenaltyPrelude + `
local function reject_limit(index,current,wait)
 if penalty and not penalty.exempt and penalty.limit_enabled and penalty.duration_enabled then
  local strikes=tonumber(redis.call('HINCRBY',stateKey,'limit_strikes',1))
  redis.call('PEXPIRE',stateKey,penalty.maximum+86400000)
  if strikes>=penalty.threshold then
   local level=tonumber(redis.call('HGET',stateKey,'level')) or 0
   local duration=penalty.duration
   if penalty.exponential then duration=math.min(penalty.maximum,duration*penalty.multiplier^math.min(level,32)) end
   duration=math.min(duration,penalty.maximum)
   redis.call('HSET',stateKey,'until',now+duration,'until_rule','limit','level',level+1,'limit_strikes',0)
   redis.call('PEXPIRE',stateKey,penalty.maximum+86400000)
   table.insert(events,{rule='penalty.consecutive_limits',current=strikes,limit=penalty.threshold,action='cooldown',window=duration})
   if penalty.reject then return {-1,0,0,duration,now,encode_events()} end
  end
 end
 return {0,index,current,wait,now,encode_events()}
end
if spec.observations then
 for _,o in ipairs(spec.observations) do
  local key=KEYS[o.index]
  local start=tonumber(redis.call('HGET',key,'start')) or now
  local value=tonumber(redis.call('HGET',key,'value')) or 0
  if now-start>=o.window then
   value=0;start=now
   redis.call('HDEL',key,'last','samples','hits','action_start')
  end
  local hit=false
  if o.kind=='distinct' then
   local members=redis.call('HGETALL',key)
   value=0
   for i=1,#members,2 do
    if string.sub(members[i],1,2)=='u:' then
     if tonumber(members[i+1])<=now-o.window then redis.call('HDEL',key,members[i]) else value=value+1 end
    end
   end
   if not redis.call('HGET',key,'u:'..o.identity) then value=value+1 end
   if value<=10000 then redis.call('HSET',key,'u:'..o.identity,now) end
  elseif o.kind=='variance' then
   local last=tonumber(redis.call('HGET',key,'last'))
   local samples={}
   local encoded=redis.call('HGET',key,'samples')
   if encoded and now-start<o.window then samples=cjson.decode(encoded) end
   if last then table.insert(samples,now-last) end
   while #samples>o.samples do table.remove(samples,1) end
   if #samples>=o.samples then
    local mean=0;for _,n in ipairs(samples) do mean=mean+n end;mean=mean/#samples
    value=0;for _,n in ipairs(samples) do value=value+(n-mean)^2 end;value=value/#samples
    hit=value<=o.threshold
   end
   redis.call('HSET',key,'last',now,'samples',cjson.encode(samples))
  elseif o.kind=='failures' then
   hit=value>=o.threshold
  else
   value=value+1
  end
  if o.kind~='variance' and o.kind~='failures' then hit=value>o.threshold end
  redis.call('HSET',key,'start',start,'value',value)
  redis.call('PEXPIRE',key,o.window)
  if hit and o.action_enabled then
   local actionStart=tonumber(redis.call('HGET',key,'action_start')) or now
   if now-actionStart>=o.action_window then redis.call('HSET',key,'hits',0);actionStart=now end
   redis.call('HSET',key,'action_start',actionStart)
   local strikes=tonumber(redis.call('HINCRBY',key,'hits',1))
   if strikes>=o.action_threshold then
    local event={rule=o.name,current=value,limit=o.threshold,action=o.action,window=o.window}
    if penalty and penalty.exempt and (o.action=='slowdown' or o.action=='cooldown' or o.action=='remove') then event.action='exempt' end
    table.insert(events,event)
    if penalty and not penalty.exempt and (o.action=='cooldown' or o.action=='remove') then
     local untilTime=now+o.action_window
     if o.action=='remove' then untilTime=now+o.window end
     event.window=untilTime-now
     redis.call('HSET',KEYS[penalty.index],'until',untilTime,'until_rule',o.name)
     redis.call('PEXPIRE',KEYS[penalty.index],math.max(o.window,o.action_window)+86400000)
    end
    if penalty and not penalty.exempt and o.action=='slowdown' then
     redis.call('HSET',stateKey,'slow_until',now+o.action_window,'slow_rule',o.name,'slow_interval',o.interval)
     redis.call('PEXPIRE',stateKey,o.action_window+86400000)
    end
   end
  end
 end
end
for _,event in ipairs(events) do
 if penalty and not penalty.exempt and penalty.reject and (event.action=='cooldown' or event.action=='remove') then
  return {-1,0,0,event.window,now,cjson.encode(events)}
 end
end
`

type freeAbuseObservation struct {
	Name            string  `json:"name"`
	Index           int     `json:"index"`
	Kind            string  `json:"kind"`
	Identity        string  `json:"identity"`
	Threshold       float64 `json:"threshold"`
	Window          int64   `json:"window"`
	Samples         int     `json:"samples"`
	Action          string  `json:"action"`
	ActionEnabled   bool    `json:"action_enabled"`
	ActionThreshold int64   `json:"action_threshold"`
	ActionWindow    int64   `json:"action_window"`
	Interval        int64   `json:"interval"`
}

type freePenaltySpec struct {
	Index           int     `json:"index"`
	Indices         []int   `json:"indices"`
	Reject          bool    `json:"reject"`
	Exempt          bool    `json:"exempt"`
	LimitEnabled    bool    `json:"limit_enabled"`
	DurationEnabled bool    `json:"duration_enabled"`
	Threshold       int64   `json:"threshold"`
	Duration        int64   `json:"duration"`
	Exponential     bool    `json:"exponential"`
	Multiplier      float64 `json:"multiplier"`
	Maximum         int64   `json:"maximum"`
}

type freeAbuseEvent struct {
	Rule    string  `json:"rule"`
	Current float64 `json:"current"`
	Limit   float64 `json:"limit"`
	Action  string  `json:"action"`
	Window  int64   `json:"window"`
}

type FreeAbuseContext struct {
	UserID             int
	IP                 string
	Fingerprint        string
	PromptHash         string
	PromptTokens       int
	OutputMultiplicity int64
	Version            int64
}

// Admission checks penalties before model/body validation. The rate script
// repeats this check atomically with reservations to close concurrent updates.
func CheckFreeGroupCooldown(c *gin.Context, userID int) error {
	snapshot := GetFreeGroupConfig()
	if !snapshot.Ready {
		return ErrFreeGroupRedis
	}
	if !snapshot.Config.Enabled {
		return ErrFreeGroupDisabled
	}
	if c.GetInt64("free_group_cooldown_version") == snapshot.Version {
		return nil
	}
	if common.RDB == nil || !common.RedisEnabled {
		return ErrFreeGroupRedis
	}
	fingerprint := c.GetHeader("X-Free-Fingerprint")
	if fingerprint != "" {
		fingerprint = freeKeyPart(fingerprint)
	}
	input := &FreeAbuseContext{UserID: userID, IP: FreeGroupClientIP(c), Fingerprint: fingerprint}
	keys, observations, penalty := freeAbusePlan(snapshot.Config, input, nil)
	spec, err := common.Marshal(struct {
		Observations []freeAbuseObservation `json:"observations"`
		Penalty      *freePenaltySpec       `json:"penalty"`
	}{observations, penalty})
	if err != nil {
		return err
	}
	script := `local spec=cjson.decode(ARGV[1]);local clock=redis.call('TIME');local now=tonumber(clock[1])*1000+math.floor(tonumber(clock[2])/1000);` + freePenaltyPrelude + `return {1,0,0,0,now,'[]'}`
	result, err := common.RDB.Eval(c.Request.Context(), script, keys, string(spec)).Slice()
	if err != nil || len(result) != 6 {
		return ErrFreeGroupRedis
	}
	status, err := strconv.ParseInt(fmt.Sprint(result[0]), 10, 64)
	if err != nil {
		return ErrFreeGroupRedis
	}
	if status == 1 {
		c.Set("free_group_cooldown_version", snapshot.Version)
		return nil
	}
	wait, err := strconv.ParseInt(fmt.Sprint(result[3]), 10, 64)
	if err != nil {
		return ErrFreeGroupRedis
	}
	now, err := strconv.ParseInt(fmt.Sprint(result[4]), 10, 64)
	if err != nil {
		return ErrFreeGroupRedis
	}
	rule, recovery := "penalty.cooldown", "cooldown_or_manual_release"
	if status == -2 {
		rule, recovery = "penalty.slowdown", "reduced_frequency"
	}
	return &FreeGroupRejection{Rule: rule, Status: 429, RetryAfter: (wait + 999) / 1000, ResetAt: (now + wait) / 1000, Recovery: recovery}
}

func freeAbusePlan(config model.FreeGroupConfig, input *FreeAbuseContext, keys []string) ([]string, []freeAbuseObservation, *freePenaltySpec) {
	if input == nil {
		return keys, nil, nil
	}
	prefix := freeGroupNamespace + ":abuse:"
	user := strconv.Itoa(input.UserID)
	observations := make([]freeAbuseObservation, 0, 10)
	entries := []struct {
		name, key, kind string
		rule            model.FreeAbuseRule
	}{
		{"ip_active_accounts", "ip:" + freeKeyPart(input.IP), "distinct", config.AntiAbuse.IPActiveAccounts},
		{"fingerprint_active_accounts", "fingerprint:" + input.Fingerprint, "distinct", config.AntiAbuse.FingerprintActiveAccounts},
		{"ip_aggregate_rate", "ip_rate:" + freeKeyPart(input.IP), "count", config.AntiAbuse.IPAggregateRate},
		{"prompt_hash_repeat", "prompt:" + user + ":" + input.PromptHash, "count", config.AntiAbuse.PromptHashRepeat},
		{"machine_interval", "interval:" + user, "variance", config.AntiAbuse.MachineInterval},
		{"failure_rate", "failure:" + user, "failures", config.AntiAbuse.FailureRate},
	}
	location, _ := time.LoadLocation(config.Rate.Timezone)
	hour := time.Now().In(location).Hour()
	night := config.AntiAbuse.NightHighFrequency
	isNight := hour >= night.StartHour && hour < night.EndHour
	if night.StartHour > night.EndHour {
		isNight = hour >= night.StartHour || hour < night.EndHour
	}
	if isNight {
		entries = append(entries, struct {
			name, key, kind string
			rule            model.FreeAbuseRule
		}{"night_high_frequency", "night:" + user, "count", night})
	}
	if input.PromptTokens < config.AntiAbuse.ShortPrompt.PromptTokenMinimum {
		entries = append(entries, struct {
			name, key, kind string
			rule            model.FreeAbuseRule
		}{"short_prompt", "short:" + user, "count", config.AntiAbuse.ShortPrompt})
	}
	if config.AccessMode == model.FreeGroupAccessModeCondition {
		for _, admission := range []struct {
			name, key string
			rule      model.FreeGroupIntRule
		}{{"admission.ip_accounts", "admission:ip:" + freeKeyPart(input.IP), config.Admission.IPAccounts}, {"admission.fingerprint_accounts", "admission:fp:" + input.Fingerprint, config.Admission.FingerprintAccounts}} {
			if admission.rule.Enabled {
				entries = append(entries, struct {
					name, key, kind string
					rule            model.FreeAbuseRule
				}{admission.name, admission.key, "distinct", model.FreeAbuseRule{Enabled: true, Threshold: float64(admission.rule.Limit), WindowSeconds: admission.rule.WindowSeconds, Action: "remove"}})
			}
		}
	}
	for _, entry := range entries {
		if !entry.rule.Enabled {
			continue
		}
		if input.Fingerprint == "" && (entry.name == "fingerprint_active_accounts" || entry.name == "admission.fingerprint_accounts") {
			continue
		}
		action := config.AntiAbuse.Actions.Observe
		switch entry.rule.Action {
		case "alert":
			action = config.AntiAbuse.Actions.Notify
		case "slowdown":
			action = config.AntiAbuse.Actions.Slowdown
		case "cooldown":
			action = config.AntiAbuse.Actions.Cooldown
		case "remove":
			action = config.AntiAbuse.Actions.Remove
		}
		if entry.name == "admission.ip_accounts" || entry.name == "admission.fingerprint_accounts" {
			action = model.FreeActionConfig{Enabled: true, Threshold: 1, WindowSeconds: entry.rule.WindowSeconds}
		}
		keys = append(keys, prefix+entry.key)
		observations = append(observations, freeAbuseObservation{Name: entry.name, Index: len(keys), Kind: entry.kind, Identity: user, Threshold: entry.rule.Threshold, Window: entry.rule.WindowSeconds * 1000, Samples: max(2, entry.rule.SampleCount), Action: entry.rule.Action, ActionEnabled: action.Enabled, ActionThreshold: max(1, action.Threshold), ActionWindow: max(1, action.WindowSeconds) * 1000, Interval: max(1, action.IntervalSeconds) * 1000})
	}
	keys = append(keys, freeGroupNamespace+":penalty:user:"+user)
	userIndex := len(keys)
	indices := []int{userIndex}
	if input.IP != "" {
		keys = append(keys, freeGroupNamespace+":penalty:ip:"+freeKeyPart(input.IP))
		indices = append(indices, len(keys))
	}
	if input.Fingerprint != "" {
		keys = append(keys, freeGroupNamespace+":penalty:fingerprint:"+input.Fingerprint)
		indices = append(indices, len(keys))
	}
	exempt := config.Penalty.WhitelistEnabled && (slices.Contains(config.Penalty.WhitelistUsers, input.UserID) || slices.Contains(config.Penalty.WhitelistIPs, input.IP))
	return keys, observations, &freePenaltySpec{Index: userIndex, Indices: indices, Reject: config.Penalty.RejectDuringCooldown, Exempt: exempt, LimitEnabled: config.Penalty.CooldownOnLimit.Enabled, DurationEnabled: config.Penalty.CooldownMinutes.Enabled, Threshold: config.Penalty.CooldownOnLimit.Limit, Duration: config.Penalty.CooldownMinutes.Limit * 60000, Exponential: config.Penalty.Exponential, Multiplier: config.Penalty.Multiplier, Maximum: int64(config.Penalty.MaximumMinutes) * 60000}
}

func recordFreeAbuseEvents(input *FreeAbuseContext, events []freeAbuseEvent) error {
	if input == nil || len(events) == 0 {
		return nil
	}
	rows := make([]model.FreeGroupAudit, 0, len(events))
	for _, event := range events {
		snapshot, err := common.Marshal(event)
		if err != nil {
			return err
		}
		rows = append(rows, model.FreeGroupAudit{UserID: input.UserID, IP: input.IP, Fingerprint: input.Fingerprint, RuleName: event.Rule, Threshold: fmt.Sprint(event.Limit), Snapshot: string(snapshot), Action: event.Action, Source: "auto", ConfigVersion: input.Version, CreatedAt: time.Now().Unix()})
	}
	if err := model.DB.Create(&rows).Error; err != nil {
		return err
	}
	for _, event := range events {
		if event.Action == "alert" {
			config := GetFreeGroupConfig().Config.Notify
			if config.SiteEnabled {
				encoded, err := common.Marshal(event)
				if err != nil {
					return err
				}
				if err := model.DB.Create(&model.FreeGroupAudit{UserID: input.UserID, IP: input.IP, RuleName: "notification.site", Snapshot: string(encoded), Action: "unread", Source: "auto", ConfigVersion: input.Version, CreatedAt: time.Now().Unix()}).Error; err != nil {
					return err
				}
			}
			if config.WebhookEnabled || config.EmailEnabled {
				userID, version := input.UserID, input.Version
				select {
				case freeNotificationSlots <- struct{}{}:
					go notifyFreeGroupAdministrators(config, event, userID, version)
				default:
					// The triggering event remains durable in the audit log.
					freeComponentErrors.WithLabelValues("notification_capacity").Inc()
				}
			}
		}
		if event.Action != "remove" {
			continue
		}
		expires := int64(0)
		if GetFreeGroupConfig().Config.Blacklist.Recovery == "auto" {
			expires = time.Now().Add(time.Duration(event.Window) * time.Millisecond).Unix()
		}
		if err := model.UpsertFreeGroupMember(&model.FreeGroupMember{UserID: input.UserID, Status: "removed", Source: "auto", Reason: event.Rule, ExpiresAt: expires}, input.Version); err != nil {
			return err
		}
	}
	return nil
}

func notifyFreeGroupAdministrators(config model.FreeGroupNotifyConfig, event freeAbuseEvent, userID int, version int64) {
	defer func() { <-freeNotificationSlots }()
	// Existing notification delivery handles SSRF protection and email formatting.
	data := dto.NewNotify("free_group_alert", i18n.Translate("", "free_group.alert_title"), i18n.Translate("", "free_group.alert_content", map[string]any{"Rule": event.Rule, "User": userID, "Current": event.Current, "Limit": event.Limit, "Version": version}), nil)
	if config.WebhookEnabled {
		if err := SendWebhookNotify(config.WebhookURL, "", data); err != nil {
			freeComponentErrors.WithLabelValues("webhook_notification").Inc()
			common.SysError("Free webhook notification failed")
		}
	}
	if config.EmailEnabled {
		var users []model.User
		if err := model.DB.Select("id", "email", "setting").Where("status = ? AND role >= ? AND email <> ?", common.UserStatusEnabled, common.RoleRootUser, "").Find(&users).Error; err != nil {
			freeComponentErrors.WithLabelValues("email_notification").Inc()
			return
		}
		for _, user := range users {
			setting := user.GetSetting()
			setting.NotifyType = dto.NotifyTypeEmail
			data.Title = i18n.Translate(setting.Language, "free_group.alert_title")
			data.Content = i18n.Translate(setting.Language, "free_group.alert_content", map[string]any{"Rule": event.Rule, "User": userID, "Current": event.Current, "Limit": event.Limit, "Version": version})
			if err := NotifyUser(user.Id, user.Email, setting, data); err != nil {
				freeComponentErrors.WithLabelValues("email_notification").Inc()
			}
		}
	}
}

func RecordFreeGroupTrace(c *gin.Context, input *FreeAbuseContext, tokens int64, action string) error {
	if input == nil || !GetFreeGroupConfig().Config.AntiAbuse.RequestRetention.Enabled {
		return nil
	}
	snapshot, err := common.Marshal(map[string]any{"prompt_hash": input.PromptHash, "tokens": tokens, "user_agent": c.GetHeader("User-Agent")[:min(len(c.GetHeader("User-Agent")), 512)]})
	if err != nil {
		return err
	}
	return model.DB.Create(&model.FreeGroupAudit{UserID: input.UserID, IP: input.IP, Fingerprint: input.Fingerprint, RuleName: "request_trace", Snapshot: string(snapshot), Action: action, Source: "auto", ConfigVersion: input.Version, CreatedAt: time.Now().Unix()}).Error
}

func recordFreeFailure(ctx context.Context, userID int, window int64, failures ...bool) error {
	if common.RDB == nil {
		return ErrFreeGroupRedis
	}
	if len(failures) > 0 && !failures[0] {
		return common.RDB.Del(ctx, freeGroupNamespace+":abuse:failure:"+strconv.Itoa(userID)).Err()
	}
	return common.RDB.Eval(ctx, `local n=redis.call('HINCRBY',KEYS[1],'value',1);if n==1 then redis.call('PEXPIRE',KEYS[1],ARGV[1]) end;return n`, []string{freeGroupNamespace + ":abuse:failure:" + strconv.Itoa(userID)}, window*1000).Err()
}
