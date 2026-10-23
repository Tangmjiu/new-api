package model

import (
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	FreeGroupName = "free"

	FreeGroupAccessModeAll       = "all"
	FreeGroupAccessModeCondition = "condition"
	FreeGroupAccessModeApply     = "apply"
	FreeGroupAccessModeInvite    = "invite"

	FreeGroupQueueStrategyReject = "reject"
	FreeGroupQueueStrategyQueue  = "queue"

	FreeGroupOutsideModelReject = "reject"
	FreeGroupOutsideModelAllow  = "allow"

	FreeGroupMemberApproved = "approved"
	FreeGroupMemberPending  = "pending"
	FreeGroupMemberBlocked  = "blocked"
)

// FreeGroupConfig is deliberately independent from the traditional group
// ratio settings. It is persisted as one JSON document so adding a toggle does
// not require a schema migration for every new restriction.
type FreeGroupConfig struct {
	SchemaVersion int    `json:"schema_version"`
	Enabled       bool   `json:"enabled"`
	AccessMode    string `json:"access_mode"`
	Preset        string `json:"preset"`

	Blacklist   FreeGroupBlacklistConfig   `json:"blacklist"`
	Admission   FreeGroupAdmissionConfig   `json:"admission"`
	Models      FreeGroupModelConfig       `json:"models"`
	Concurrency FreeGroupConcurrencyConfig `json:"concurrency"`
	Rate        FreeGroupRateConfig        `json:"rate"`
	Request     FreeGroupRequestConfig     `json:"request"`
	AntiAbuse   FreeGroupAntiAbuseConfig   `json:"anti_abuse"`
	Penalty     FreeGroupPenaltyConfig     `json:"penalty"`
	Notify      FreeGroupNotifyConfig      `json:"notify"`
	Queue       FreeGroupQueueConfig       `json:"queue"`
}

type FreeGroupBlacklistConfig struct {
	Enabled  bool   `json:"enabled"`
	UserIDs  []int  `json:"user_ids"`
	Recovery string `json:"recovery"` // auto or manual
}

type FreeGroupAdmissionConfig struct {
	Registration        FreeGroupIntRule     `json:"registration"`
	Bindings            FreeGroupBindingRule `json:"bindings"`
	Captcha             FreeGroupCaptchaRule `json:"captcha"`
	FingerprintAccounts FreeGroupIntRule     `json:"fingerprint_accounts"`
	IPAccounts          FreeGroupIntRule     `json:"ip_accounts"`
}

type FreeGroupIntRule struct {
	Enabled       bool   `json:"enabled"`
	Limit         int    `json:"limit"`
	WindowSeconds int64  `json:"window_seconds"`
	Action        string `json:"action"`
}

type FreeGroupBindingRule struct {
	Enabled  bool     `json:"enabled"`
	Required []string `json:"required"`
}

type FreeGroupCaptchaRule struct {
	Enabled bool   `json:"enabled"`
	Method  string `json:"method"`
}

type FreeGroupModelConfig struct {
	PerModelConcurrency bool                      `json:"per_model_concurrency_enabled"`
	WhitelistEnabled    bool                      `json:"whitelist_enabled"`
	Models              []string                  `json:"models"`
	OutsidePolicy       string                    `json:"outside_policy"`
	PerModelQuota       bool                      `json:"per_model_quota_enabled"`
	PerModelLimits      map[string]FreeModelLimit `json:"per_model_limits"`
}

type FreeModelLimit struct {
	DailyRequests int64 `json:"daily_requests,omitempty"`
	DailyTokens   int64 `json:"daily_tokens,omitempty"`
	Concurrency   int   `json:"concurrency,omitempty"`
}

type FreeGroupConcurrencyConfig struct {
	LeaseTTLEnabled bool                     `json:"lease_ttl_enabled"`
	User            FreeConcurrencyDimension `json:"user"`
	Token           FreeConcurrencyDimension `json:"token"`
	IP              FreeConcurrencyDimension `json:"ip"`
	Model           FreeConcurrencyDimension `json:"model"`
	Global          FreeConcurrencyDimension `json:"global"`
	UserShare       FreeUserShareLimit       `json:"user_share"`
	LeaseTTLSeconds int64                    `json:"lease_ttl_seconds"`
	Renew           FreeRenewConfig          `json:"renew"`
}

type FreeConcurrencyDimension struct {
	Enabled        bool   `json:"enabled"`
	Limit          int    `json:"limit"`
	Strategy       string `json:"strategy"`
	MaxWaitSeconds int64  `json:"max_wait_seconds"`
	MaxQueueLength int    `json:"max_queue_length"`
}

type FreeUserShareLimit struct {
	MaxWaitSeconds int64   `json:"max_wait_seconds"`
	MaxQueueLength int     `json:"max_queue_length"`
	Enabled        bool    `json:"enabled"`
	Percent        float64 `json:"percent"`
	Strategy       string  `json:"strategy"`
}

type FreeRenewConfig struct {
	Enabled          bool  `json:"enabled"`
	HeartbeatSeconds int64 `json:"heartbeat_seconds"`
}

type FreeGroupRateConfig struct {
	RPM             FreeBurstRule        `json:"rpm"`
	TPM             FreeBurstRule        `json:"tpm"`
	HourlyRequests  FreeWindowRule       `json:"hourly_requests"`
	HourlyTokens    FreeWindowRule       `json:"hourly_tokens"`
	DailyRequests   FreeWindowRule       `json:"daily_requests"`
	DailyTokens     FreeWindowRule       `json:"daily_tokens"`
	MonthlyRequests FreeWindowRule       `json:"monthly_requests"`
	MonthlyTokens   FreeWindowRule       `json:"monthly_tokens"`
	Timezone        string               `json:"timezone"`
	IdleCredit      FreeIdleCreditConfig `json:"idle_credit"`
}

type FreeBurstRule struct {
	Enabled bool  `json:"enabled"`
	Limit   int64 `json:"limit"`
	Burst   int64 `json:"burst"`
}

type FreeWindowRule struct {
	Enabled       bool  `json:"enabled"`
	Limit         int64 `json:"limit"`
	WindowSeconds int64 `json:"window_seconds"`
}

type FreeIdleCreditConfig struct {
	Enabled        bool  `json:"enabled"`
	AccrualPerHour int64 `json:"accrual_per_hour"`
	Maximum        int64 `json:"maximum"`
}

type FreeGroupRequestConfig struct {
	MaxTokens        FreeMaxTokensRule  `json:"max_tokens"`
	PromptTokens     FreeIntRule        `json:"prompt_tokens"`
	BodySize         FreeIntRule        `json:"body_size_kb"`
	Parameters       FreeParametersRule `json:"parameters"`
	Streaming        FreeBoolRule       `json:"streaming"`
	Multimodal       FreeBoolRule       `json:"multimodal"`
	TimeoutSeconds   FreeIntRule        `json:"timeout_seconds"`
	OutputInputRatio FreeFloatRule      `json:"output_input_ratio"`
}

type FreeMaxTokensRule struct {
	Enabled bool   `json:"enabled"`
	Limit   int    `json:"limit"`
	Action  string `json:"action"` // truncate or reject
}

type FreeIntRule struct {
	Enabled bool  `json:"enabled"`
	Limit   int64 `json:"limit"`
}

type FreeFloatRule struct {
	Enabled bool    `json:"enabled"`
	Limit   float64 `json:"limit"`
}

type FreeBoolRule struct {
	Enabled bool `json:"enabled"`
	Allowed bool `json:"allowed"`
}

type FreeParametersRule struct {
	Ranges         map[string]FreeParameterRange `json:"ranges"`
	Enabled        bool                          `json:"enabled"`
	TemperatureMin float64                       `json:"temperature_min"`
	TemperatureMax float64                       `json:"temperature_max"`
}

type FreeParameterRange struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

type FreeGroupAntiAbuseConfig struct {
	IPActiveAccounts          FreeAbuseRule     `json:"ip_active_accounts"`
	FingerprintActiveAccounts FreeAbuseRule     `json:"fingerprint_active_accounts"`
	IPAggregateRate           FreeAbuseRule     `json:"ip_aggregate_rate"`
	PromptHashRepeat          FreeAbuseRule     `json:"prompt_hash_repeat"`
	MachineInterval           FreeAbuseRule     `json:"machine_interval"`
	NightHighFrequency        FreeAbuseRule     `json:"night_high_frequency"`
	ShortPrompt               FreeAbuseRule     `json:"short_prompt"`
	FailureRate               FreeAbuseRule     `json:"failure_rate"`
	RequestRetention          FreeRetentionRule `json:"request_retention"`
	Actions                   FreeAbuseActions  `json:"actions"`
}

type FreeAbuseRule struct {
	StartHour          int     `json:"start_hour"`
	EndHour            int     `json:"end_hour"`
	PromptTokenMinimum int     `json:"prompt_token_minimum"`
	Enabled            bool    `json:"enabled"`
	Threshold          float64 `json:"threshold"`
	WindowSeconds      int64   `json:"window_seconds"`
	SampleCount        int     `json:"sample_count"`
	Action             string  `json:"action"`
}

type FreeRetentionRule struct {
	Enabled       bool `json:"enabled"`
	RetentionDays int  `json:"retention_days"`
}

type FreeAbuseActions struct {
	Observe  FreeActionConfig `json:"observe"`
	Notify   FreeActionConfig `json:"notify"`
	Slowdown FreeActionConfig `json:"slowdown"`
	Cooldown FreeActionConfig `json:"cooldown"`
	Remove   FreeActionConfig `json:"remove"`
}

type FreeActionConfig struct {
	Enabled         bool  `json:"enabled"`
	Threshold       int64 `json:"threshold"`
	WindowSeconds   int64 `json:"window_seconds"`
	IntervalSeconds int64 `json:"interval_seconds"`
}

type FreeGroupPenaltyConfig struct {
	CooldownOnLimit      FreeIntRule `json:"cooldown_on_limit"`
	CooldownMinutes      FreeIntRule `json:"cooldown_minutes"`
	Exponential          bool        `json:"exponential"`
	Multiplier           float64     `json:"multiplier"`
	MaximumMinutes       int         `json:"maximum_minutes"`
	RejectDuringCooldown bool        `json:"reject_during_cooldown"`
	WhitelistEnabled     bool        `json:"whitelist_enabled"`
	WhitelistUsers       []int       `json:"whitelist_users"`
	WhitelistIPs         []string    `json:"whitelist_ips"`
}

type FreeGroupNotifyConfig struct {
	WebhookEnabled   bool   `json:"webhook_enabled"`
	WebhookURL       string `json:"webhook_url"`
	EmailEnabled     bool   `json:"email_enabled"`
	SiteEnabled      bool   `json:"site_enabled"`
	UserLimitDisplay bool   `json:"user_limit_display"`
	ErrorTemplate    string `json:"error_template"`
}

type FreeGroupQueueConfig struct {
	Enabled        bool  `json:"enabled"`
	MaxLength      int   `json:"max_length"`
	MaxWaitSeconds int64 `json:"max_wait_seconds"`
	PollMillis     int64 `json:"poll_millis"`
}

// FreeGroupConfigRecord stores the current document. ID is always 1.
type FreeGroupConfigRecord struct {
	ID        int    `gorm:"primaryKey" json:"id"`
	Version   int64  `gorm:"not null;index" json:"version"`
	Preset    string `gorm:"type:varchar(32)" json:"preset"`
	Config    string `gorm:"type:text;not null" json:"-"`
	UpdatedBy int    `gorm:"index" json:"updated_by"`
	UpdatedAt int64  `gorm:"not null" json:"updated_at"`
}

type FreeGroupConfigVersion struct {
	ID        int    `gorm:"primaryKey" json:"id"`
	Version   int64  `gorm:"uniqueIndex" json:"version"`
	Preset    string `gorm:"type:varchar(32)" json:"preset"`
	Config    string `gorm:"type:text;not null" json:"-"`
	UpdatedBy int    `gorm:"index" json:"updated_by"`
	CreatedAt int64  `gorm:"not null;index" json:"created_at"`
	Summary   string `gorm:"type:varchar(512)" json:"summary"`
	HighRisk  bool   `json:"high_risk"`
}

type FreeGroupMember struct {
	CaptchaUntil int64  `json:"captcha_until"`
	ExpiresAt    int64  `json:"expires_at"`
	ID           int    `gorm:"primaryKey" json:"id"`
	UserID       int    `gorm:"uniqueIndex;not null" json:"user_id"`
	Status       string `gorm:"type:varchar(16);index;not null" json:"status"` // approved, pending, blocked
	Source       string `gorm:"type:varchar(16)" json:"source"`
	Reason       string `gorm:"type:varchar(255)" json:"reason"`
	UpdatedBy    int    `gorm:"index" json:"updated_by"`
	CreatedAt    int64  `gorm:"not null" json:"created_at"`
	UpdatedAt    int64  `gorm:"not null" json:"updated_at"`
}

type FreeGroupAudit struct {
	ID            int    `gorm:"primaryKey" json:"id"`
	UserID        int    `gorm:"index" json:"user_id"`
	IP            string `gorm:"type:varchar(64);index" json:"ip"`
	Fingerprint   string `gorm:"type:varchar(128);index" json:"fingerprint"`
	RuleName      string `gorm:"type:varchar(128);index" json:"rule_name"`
	Threshold     string `gorm:"type:varchar(255)" json:"threshold"`
	Snapshot      string `gorm:"type:text" json:"snapshot"`
	Action        string `gorm:"type:varchar(32)" json:"action"`
	Source        string `gorm:"type:varchar(16)" json:"source"`
	ConfigVersion int64  `gorm:"index" json:"config_version"`
	CreatedAt     int64  `gorm:"not null;index" json:"created_at"`
}

func GetFreeGroupConfigRecord() (*FreeGroupConfigRecord, error) {
	var record FreeGroupConfigRecord
	err := DB.First(&record, 1).Error
	return &record, err
}

func GetFreeGroupConfigVersion(version int64) (*FreeGroupConfigVersion, error) {
	var snapshot FreeGroupConfigVersion
	err := DB.Where("version = ?", version).First(&snapshot).Error
	return &snapshot, err
}

func SaveFreeGroupConfig(record *FreeGroupConfigRecord, snapshot *FreeGroupConfigVersion) error {
	if record == nil || snapshot == nil {
		return errors.New("free group config is nil")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		nextVersion := record.Version + 1
		now := time.Now().Unix()
		result := tx.Model(&FreeGroupConfigRecord{}).Where("id = ? AND version = ?", 1, record.Version).Updates(map[string]any{
			"version": nextVersion, "preset": record.Preset, "config": record.Config,
			"updated_by": record.UpdatedBy, "updated_at": now,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrFreeGroupConfigVersionConflict
		}
		snapshot.Version = nextVersion
		snapshot.CreatedAt = now
		return tx.Create(snapshot).Error
	})
}

var ErrFreeGroupConfigVersionConflict = errors.New("free group config version conflict")

func DefaultFreeGroupConfig() FreeGroupConfig {
	return FreeGroupConfig{
		SchemaVersion: 1,
		Enabled:       false,
		AccessMode:    FreeGroupAccessModeAll,
		Preset:        "custom",
		Blacklist:     FreeGroupBlacklistConfig{Enabled: true, UserIDs: []int{}, Recovery: "manual"},
		Admission: FreeGroupAdmissionConfig{
			FingerprintAccounts: FreeGroupIntRule{Enabled: false, Limit: 3, WindowSeconds: 3600, Action: "remove"},
			Registration:        FreeGroupIntRule{Enabled: true, Limit: 24},
			Bindings:            FreeGroupBindingRule{Enabled: true, Required: []string{"email"}},
			Captcha:             FreeGroupCaptchaRule{Enabled: false, Method: ""},
			IPAccounts:          FreeGroupIntRule{Enabled: true, Limit: 3, WindowSeconds: 3600, Action: "remove"},
		},
		Models: FreeGroupModelConfig{
			WhitelistEnabled: true,
			Models:           []string{},
			OutsidePolicy:    FreeGroupOutsideModelReject,
			PerModelLimits:   map[string]FreeModelLimit{},
		},
		Concurrency: FreeGroupConcurrencyConfig{
			LeaseTTLEnabled: true,
			User:            FreeConcurrencyDimension{Enabled: true, Limit: 2, Strategy: FreeGroupQueueStrategyReject, MaxWaitSeconds: 30, MaxQueueLength: 100},
			Token:           FreeConcurrencyDimension{Enabled: true, Limit: 2, Strategy: FreeGroupQueueStrategyReject, MaxWaitSeconds: 30, MaxQueueLength: 100},
			IP:              FreeConcurrencyDimension{Enabled: true, Limit: 5, Strategy: FreeGroupQueueStrategyReject, MaxWaitSeconds: 30, MaxQueueLength: 100},
			Model:           FreeConcurrencyDimension{Enabled: true, Limit: 10, Strategy: FreeGroupQueueStrategyReject, MaxWaitSeconds: 30, MaxQueueLength: 100},
			Global:          FreeConcurrencyDimension{Enabled: true, Limit: 50, Strategy: FreeGroupQueueStrategyReject, MaxWaitSeconds: 30, MaxQueueLength: 100},
			UserShare:       FreeUserShareLimit{Enabled: true, Percent: 10, Strategy: FreeGroupQueueStrategyReject, MaxWaitSeconds: 30, MaxQueueLength: 100},
			LeaseTTLSeconds: 90,
			Renew:           FreeRenewConfig{Enabled: true, HeartbeatSeconds: 30},
		},
		Rate: FreeGroupRateConfig{
			RPM:             FreeBurstRule{Enabled: true, Limit: 3, Burst: 5},
			TPM:             FreeBurstRule{Enabled: true, Limit: 2000, Burst: 2000},
			HourlyRequests:  FreeWindowRule{Enabled: false, Limit: 0, WindowSeconds: 3600},
			HourlyTokens:    FreeWindowRule{Enabled: false, Limit: 0, WindowSeconds: 3600},
			DailyRequests:   FreeWindowRule{Enabled: true, Limit: 20, WindowSeconds: 86400},
			DailyTokens:     FreeWindowRule{Enabled: true, Limit: 20000, WindowSeconds: 86400},
			MonthlyRequests: FreeWindowRule{Enabled: false, Limit: 0, WindowSeconds: 2592000},
			MonthlyTokens:   FreeWindowRule{Enabled: false, Limit: 0, WindowSeconds: 2592000},
			Timezone:        "Local",
			IdleCredit:      FreeIdleCreditConfig{Enabled: false, AccrualPerHour: 0, Maximum: 0},
		},
		Request: FreeGroupRequestConfig{
			MaxTokens:        FreeMaxTokensRule{Enabled: true, Limit: 512, Action: "reject"},
			PromptTokens:     FreeIntRule{Enabled: false, Limit: 0},
			BodySize:         FreeIntRule{Enabled: true, Limit: 64},
			Parameters:       FreeParametersRule{Enabled: false, TemperatureMin: 0, TemperatureMax: 2, Ranges: map[string]FreeParameterRange{}},
			Streaming:        FreeBoolRule{Enabled: true, Allowed: true},
			Multimodal:       FreeBoolRule{Enabled: true, Allowed: false},
			TimeoutSeconds:   FreeIntRule{Enabled: true, Limit: 60},
			OutputInputRatio: FreeFloatRule{Enabled: false, Limit: 0},
		},
		AntiAbuse: FreeGroupAntiAbuseConfig{
			IPActiveAccounts:          FreeAbuseRule{Enabled: true, Threshold: 3, WindowSeconds: 3600, Action: "alert"},
			FingerprintActiveAccounts: FreeAbuseRule{Enabled: true, Threshold: 3, WindowSeconds: 3600, Action: "alert"},
			IPAggregateRate:           FreeAbuseRule{Enabled: false, Action: "slowdown"},
			PromptHashRepeat:          FreeAbuseRule{Enabled: true, Threshold: 20, WindowSeconds: 3600, Action: "alert"},
			MachineInterval:           FreeAbuseRule{Enabled: false, Action: "alert"},
			NightHighFrequency:        FreeAbuseRule{Enabled: false, Action: "observe"},
			ShortPrompt:               FreeAbuseRule{Enabled: false, Action: "observe"},
			FailureRate:               FreeAbuseRule{Enabled: true, Threshold: 20, WindowSeconds: 300, Action: "slowdown"},
			RequestRetention:          FreeRetentionRule{Enabled: true, RetentionDays: 30},
			Actions: FreeAbuseActions{
				Observe:  FreeActionConfig{Enabled: true, Threshold: 1, WindowSeconds: 3600},
				Notify:   FreeActionConfig{Enabled: true, Threshold: 3, WindowSeconds: 3600},
				Slowdown: FreeActionConfig{Enabled: false, Threshold: 5, WindowSeconds: 60, IntervalSeconds: 5},
				Cooldown: FreeActionConfig{Enabled: false, Threshold: 10, WindowSeconds: 600},
				Remove:   FreeActionConfig{Enabled: false, Threshold: 20, WindowSeconds: 3600},
			},
		},
		Penalty: FreeGroupPenaltyConfig{
			CooldownOnLimit:      FreeIntRule{Enabled: true, Limit: 5},
			CooldownMinutes:      FreeIntRule{Enabled: true, Limit: 10},
			Exponential:          true,
			Multiplier:           2,
			MaximumMinutes:       240,
			RejectDuringCooldown: true,
			WhitelistEnabled:     true,
			WhitelistUsers:       []int{},
			WhitelistIPs:         []string{},
		},
		Notify: FreeGroupNotifyConfig{UserLimitDisplay: true, ErrorTemplate: "Free limit reached: {{rule}} ({{current}}/{{limit}}), retry after {{retry_after}} seconds."},
		Queue:  FreeGroupQueueConfig{Enabled: false, MaxLength: 100, MaxWaitSeconds: 30, PollMillis: 250},
	}
}

func InitializeFreeGroupConfig() error {
	config := DefaultFreeGroupConfig()
	encoded, err := common.Marshal(config)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	return DB.Transaction(func(tx *gorm.DB) error {
		current := FreeGroupConfigRecord{ID: 1, Version: 1, Preset: config.Preset, Config: string(encoded), UpdatedAt: now}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&current)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		return tx.Create(&FreeGroupConfigVersion{Version: 1, Preset: config.Preset, Config: string(encoded), CreatedAt: now, Summary: "Initial Free group configuration"}).Error
	})
}

func ListFreeGroupConfigVersions(offset, limit int) ([]FreeGroupConfigVersion, int64, error) {
	var versions []FreeGroupConfigVersion
	var total int64
	query := DB.Model(&FreeGroupConfigVersion{})
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = 50
	}
	err := query.Order("version desc").Offset(max(offset, 0)).Limit(min(limit, 200)).Find(&versions).Error
	return versions, total, err
}

func ListFreeGroupMembers(status string, offset, limit int) ([]FreeGroupMember, int64, error) {
	query := DB.Model(&FreeGroupMember{})
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = 50
	}
	var members []FreeGroupMember
	err := query.Order("id desc").Offset(max(offset, 0)).Limit(min(limit, 200)).Find(&members).Error
	return members, total, err
}

func UpsertFreeGroupMember(member *FreeGroupMember, versions ...int64) error {
	if member == nil || member.UserID <= 0 {
		return errors.New("free group member user id is invalid")
	}
	now := time.Now().Unix()
	if member.CreatedAt == 0 {
		member.CreatedAt = now
	}
	member.UpdatedAt = now
	return DB.Transaction(func(tx *gorm.DB) error {
		// An application can create a pending membership, never replace a
		// decision made by an administrator (including a block).
		conflict := clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}}, DoNothing: true}
		if member.Source != "application" {
			conflict.DoNothing = false
			conflict.DoUpdates = clause.AssignmentColumns([]string{"status", "source", "reason", "updated_by", "updated_at", "expires_at"})
		}
		result := tx.Clauses(conflict).Create(member)
		if result.Error != nil {
			return result.Error
		}
		if member.Source == "application" && result.RowsAffected == 0 {
			result = tx.Model(&FreeGroupMember{}).Where("user_id = ? AND status = ?", member.UserID, "verified").Updates(map[string]any{"status": member.Status, "source": member.Source, "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return nil
			}
		}
		snapshot, err := common.Marshal(map[string]any{"status": member.Status, "reason": member.Reason, "actor_id": member.UpdatedBy})
		if err != nil {
			return err
		}
		version := int64(0)
		if len(versions) > 0 {
			version = versions[0]
		}
		return tx.Create(&FreeGroupAudit{UserID: member.UserID, RuleName: "member_access", Snapshot: string(snapshot), Action: member.Status, Source: member.Source, ConfigVersion: version, CreatedAt: now}).Error
	})
}

func RemoveFreeGroupMember(userID, actorID int, version int64) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var member FreeGroupMember
		if err := lockForUpdate(tx).Where("user_id = ?", userID).First(&member).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if err := tx.Delete(&member).Error; err != nil {
			return err
		}
		snapshot, err := common.Marshal(map[string]any{"previous_status": member.Status, "actor_id": actorID})
		if err != nil {
			return err
		}
		return tx.Create(&FreeGroupAudit{UserID: userID, RuleName: "member_access", Snapshot: string(snapshot), Action: "removed_from_list", Source: "manual", ConfigVersion: version, CreatedAt: time.Now().Unix()}).Error
	})
}

func VerifyFreeGroupHuman(userID int, until int64) error {
	member := FreeGroupMember{UserID: userID, Status: "verified", Source: "verification", CaptchaUntil: until, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()}
	return DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}}, DoUpdates: clause.AssignmentColumns([]string{"captcha_until", "updated_at"})}).Create(&member).Error
}
