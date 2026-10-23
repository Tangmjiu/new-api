package service

import (
	"context"
	"encoding/hex"
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

type FreeAuditFilter struct {
	UserID      int
	IP          string
	Fingerprint string
	Rule        string
	Start       int64
	End         int64
	Offset      int
	Limit       int
}

func ListFreeAudits(ctx context.Context, filter FreeAuditFilter) ([]model.FreeGroupAudit, int64, error) {
	query := model.DB.WithContext(ctx).Model(&model.FreeGroupAudit{})
	if filter.UserID > 0 {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if filter.IP != "" {
		query = query.Where("ip = ?", filter.IP)
	}
	if filter.Fingerprint != "" {
		query = query.Where("fingerprint = ?", filter.Fingerprint)
	}
	if filter.Rule != "" {
		query = query.Where("rule_name = ?", filter.Rule)
	}
	if filter.Start > 0 {
		query = query.Where("created_at >= ?", filter.Start)
	}
	if filter.End > 0 {
		query = query.Where("created_at <= ?", filter.End)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []model.FreeGroupAudit
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	err := query.Order("id desc").Offset(max(0, filter.Offset)).Limit(min(limit, 200)).Find(&items).Error
	return items, total, err
}

type FreeManualPenalty struct {
	Scope   string `json:"scope"`
	Target  string `json:"target"`
	Action  string `json:"action"`
	Minutes int64  `json:"minutes"`
	Reason  string `json:"reason"`
}

func ApplyFreeManualPenalty(ctx context.Context, request FreeManualPenalty, actorID int) error {
	if common.RDB == nil || !common.RedisEnabled {
		return ErrFreeGroupRedis
	}
	if request.Action != "release" && request.Action != "cooldown" {
		return errors.New("Invalid Free penalty action")
	}
	if request.Action == "cooldown" && (request.Minutes < 1 || request.Minutes > 525600) {
		return errors.New("Free cooldown must be between 1 and 525600 minutes")
	}
	if len(request.Reason) > 255 || strings.TrimSpace(request.Reason) == "" {
		return errors.New("A Free penalty reason of at most 255 bytes is required")
	}
	identity := request.Target
	row := model.FreeGroupAudit{RuleName: "manual." + request.Scope, Action: request.Action, Source: "manual", ConfigVersion: GetFreeGroupConfig().Version, CreatedAt: time.Now().Unix()}
	switch request.Scope {
	case "user":
		userID, err := strconv.Atoi(request.Target)
		if err != nil || userID <= 0 {
			return errors.New("Invalid user id")
		}
		if _, err := model.GetUserById(userID, false); err != nil {
			return err
		}
		identity, row.UserID = strconv.Itoa(userID), userID
	case "ip":
		ip, err := netip.ParseAddr(request.Target)
		if err != nil {
			return errors.New("Invalid IP address")
		}
		row.IP = ip.Unmap().String()
		identity = freeKeyPart(row.IP)
	case "fingerprint":
		fingerprint, err := hex.DecodeString(request.Target)
		if err != nil || len(fingerprint) != 32 {
			return errors.New("Invalid fingerprint hash")
		}
		identity, row.Fingerprint = strings.ToLower(request.Target), strings.ToLower(request.Target)
	default:
		return errors.New("Invalid Free penalty scope")
	}
	snapshot := map[string]any{"actor_id": actorID, "target": request.Target, "reason": request.Reason, "minutes": request.Minutes, "result": "pending"}
	encoded, err := common.Marshal(snapshot)
	if err != nil {
		return err
	}
	row.Snapshot = string(encoded)
	// Persist intent first. A Redis outage can leave a recorded pending/failed
	// operation, but must never perform an unaudited administrative action.
	if err := model.DB.WithContext(ctx).Create(&row).Error; err != nil {
		return err
	}
	key := freeGroupNamespace + ":penalty:" + request.Scope + ":" + identity
	if request.Action == "release" {
		err = common.RDB.Del(ctx, key).Err()
	} else {
		err = common.RDB.Eval(ctx, `local clock=redis.call('TIME');local now=tonumber(clock[1])*1000+math.floor(tonumber(clock[2])/1000);redis.call('HSET',KEYS[1],'until',now+tonumber(ARGV[1]),'until_rule','manual');redis.call('PEXPIRE',KEYS[1],tonumber(ARGV[1])+86400000);return 1`, []string{key}, request.Minutes*60000).Err()
	}
	snapshot["result"] = "applied"
	if err != nil {
		snapshot["result"] = "failed"
	}
	encoded, marshalErr := common.Marshal(snapshot)
	if marshalErr != nil {
		return marshalErr
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if auditErr := model.DB.WithContext(cleanupCtx).Model(&row).Update("snapshot", string(encoded)).Error; auditErr != nil {
		return auditErr
	}
	if err != nil {
		return ErrFreeGroupRedis
	}
	return nil
}
