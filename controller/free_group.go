package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type freeGroupConfigUpdateRequest struct {
	Version         int64                 `json:"version"`
	Config          model.FreeGroupConfig `json:"config"`
	Summary         string                `json:"summary"`
	ConfirmHighRisk bool                  `json:"confirm_high_risk"`
}

type freeGroupRollbackRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
	ConfirmHighRisk bool  `json:"confirm_high_risk"`
}

type freeGroupMemberUpdateRequest struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func GetFreeGroupConfig(c *gin.Context) {
	record, err := model.GetFreeGroupConfigRecord()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	config := model.DefaultFreeGroupConfig()
	if err := common.Unmarshal([]byte(record.Config), &config); err != nil {
		common.ApiError(c, err)
		return
	}
	snapshot := service.GetFreeGroupConfig()
	effectiveVersion := int64(0)
	if snapshot.Ready {
		effectiveVersion = snapshot.Version
	}
	common.ApiSuccess(c, gin.H{
		"config": config, "version": record.Version, "preset": record.Preset,
		"updated_by": record.UpdatedBy, "updated_at": record.UpdatedAt,
		"defaults":          model.DefaultFreeGroupConfig(),
		"effective_version": effectiveVersion,
		"ready":             snapshot.Ready,
		"strength_score":    service.FreeGroupStrength(config),
		"available_models":  model.GetEnabledModels(),
		"bindings":          []string{"email", "github", "discord", "oidc", "wechat", "telegram", "linuxdo", "oauth"},
	})
}

func UpdateFreeGroupConfig(c *gin.Context) {
	request := freeGroupConfigUpdateRequest{Config: model.DefaultFreeGroupConfig()}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	if request.Version <= 0 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("A current configuration version is required"))
		return
	}
	version, err := service.SaveFreeGroupConfig(request.Config, request.Version, c.GetInt("id"), request.Summary, request.ConfirmHighRisk)
	if err != nil {
		var pending *service.FreeGroupActivationPendingError
		if errors.As(err, &pending) {
			common.ApiSuccess(c, gin.H{"version": pending.Version, "effective": false, "activation_pending": true})
			return
		}
		status := http.StatusBadRequest
		if errors.Is(err, model.ErrFreeGroupConfigVersionConflict) {
			status = http.StatusConflict
		} else if errors.Is(err, service.ErrFreeGroupRiskConfirmationRequired) {
			status = http.StatusPreconditionRequired
		}
		common.ApiErrorStatus(c, status, err)
		return
	}
	common.ApiSuccess(c, gin.H{"version": version, "effective": true})
}

func ListFreeGroupConfigVersions(c *gin.Context) {
	offset, _ := strconv.Atoi(c.Query("offset"))
	limit, _ := strconv.Atoi(c.Query("limit"))
	versions, total, err := model.ListFreeGroupConfigVersions(offset, limit)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"items": versions, "total": total})
}

func GetFreeGroupConfigVersion(c *gin.Context) {
	version, err := strconv.ParseInt(c.Param("version"), 10, 64)
	if err != nil || version <= 0 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid configuration version"))
		return
	}
	snapshot, err := model.GetFreeGroupConfigVersion(version)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		common.ApiErrorStatus(c, http.StatusNotFound, common.NewMessage("Invalid configuration version"))
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	config := model.DefaultFreeGroupConfig()
	if err := common.UnmarshalJsonStr(snapshot.Config, &config); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"version": snapshot.Version, "config": config, "summary": snapshot.Summary, "high_risk": snapshot.HighRisk, "updated_by": snapshot.UpdatedBy, "created_at": snapshot.CreatedAt})
}

func RollbackFreeGroupConfig(c *gin.Context) {
	version, err := strconv.ParseInt(c.Param("version"), 10, 64)
	if err != nil || version <= 0 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid configuration version"))
		return
	}
	var request freeGroupRollbackRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	newVersion, err := service.RollbackFreeGroupConfig(version, request.ExpectedVersion, c.GetInt("id"), request.ConfirmHighRisk)
	if err != nil {
		var pending *service.FreeGroupActivationPendingError
		if errors.As(err, &pending) {
			common.ApiSuccess(c, gin.H{"version": pending.Version, "effective": false, "activation_pending": true})
			return
		}
		status := http.StatusBadRequest
		if errors.Is(err, model.ErrFreeGroupConfigVersionConflict) {
			status = http.StatusConflict
		} else if errors.Is(err, service.ErrFreeGroupRiskConfirmationRequired) {
			status = http.StatusPreconditionRequired
		}
		common.ApiErrorStatus(c, status, err)
		return
	}
	common.ApiSuccess(c, gin.H{"version": newVersion, "effective": true})
}

func ListFreeGroupMembers(c *gin.Context) {
	offset, _ := strconv.Atoi(c.Query("offset"))
	limit, _ := strconv.Atoi(c.Query("limit"))
	status := strings.TrimSpace(c.Query("status"))
	members, total, err := model.ListFreeGroupMembers(status, offset, limit)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"items": members, "total": total})
}

func UpdateFreeGroupMember(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("user_id"))
	if err != nil || userID <= 0 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid user id"))
		return
	}
	var request freeGroupMemberUpdateRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	if err := service.SetFreeGroupMember(userID, request.Status, request.Reason, c.GetInt("id")); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			common.ApiErrorStatus(c, http.StatusNotFound, common.NewMessage("User not found"))
		} else {
			common.ApiError(c, err)
		}
		return
	}
	common.ApiSuccess(c, gin.H{"user_id": userID, "status": request.Status})
}

func RemoveFreeGroupMember(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("user_id"))
	if err != nil || userID <= 0 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid user id"))
		return
	}
	if err := model.RemoveFreeGroupMember(userID, c.GetInt("id"), service.GetFreeGroupConfig().Version); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"user_id": userID, "removed": true})
}

func GetMyFreeGroup(c *gin.Context) {
	snapshot := service.GetFreeGroupConfig()
	if !snapshot.Ready || !snapshot.Config.Enabled {
		common.ApiSuccess(c, gin.H{"enabled": false, "eligible": false, "models": []string{}, "usage": nil})
		return
	}
	userID := c.GetInt("id")
	err := service.CheckFreeGroupAccess(userID)
	models := []string{}
	if snapshot.Config.Enabled && err == nil {
		models = model.GetGroupEnabledModels(model.FreeGroupName)
	}
	allowedModels := make([]string, 0, len(models))
	for _, modelName := range models {
		if service.IsFreeGroupModelAllowed(modelName) {
			allowedModels = append(allowedModels, modelName)
		}
	}
	var member model.FreeGroupMember
	memberErr := model.DB.Where("user_id = ?", userID).First(&member).Error
	if memberErr != nil && !errors.Is(memberErr, gorm.ErrRecordNotFound) {
		common.ApiError(c, memberErr)
		return
	}
	var usage *service.FreeUsageStatus
	if snapshot.Config.Enabled && err == nil && snapshot.Config.Notify.UserLimitDisplay {
		var usageErr error
		usage, usageErr = service.GetFreeUsageStatus(c.Request.Context(), userID)
		if usageErr != nil {
			common.ApiError(c, usageErr)
			return
		}
	}
	common.ApiSuccess(c, gin.H{
		"enabled": snapshot.Config.Enabled, "eligible": err == nil, "access_mode": snapshot.Config.AccessMode,
		"captcha_required": snapshot.Config.Admission.Captcha.Enabled && (snapshot.Config.AccessMode == "condition" || snapshot.Config.AccessMode == "apply") && member.CaptchaUntil < time.Now().Unix(),
		"captcha_site_key": common.TurnstileSiteKey,
		"member_status":    member.Status, "models": allowedModels, "config_version": snapshot.Version,
		"daily_requests": snapshot.Config.Rate.DailyRequests.Limit, "daily_tokens": snapshot.Config.Rate.DailyTokens.Limit,
		"error": freeGroupAccessMessage(err),
		"usage": usage, "display_limits": snapshot.Config.Notify.UserLimitDisplay,
	})
}

func GetFreeGroupDashboard(c *gin.Context) {
	status, err := service.GetFreeDashboard(c.Request.Context())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, status)
}

func GetFreeGroupAudits(c *gin.Context) {
	userID, _ := strconv.Atoi(c.Query("user_id"))
	start, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	end, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	offset, _ := strconv.Atoi(c.Query("offset"))
	limit, _ := strconv.Atoi(c.Query("limit"))
	items, total, err := service.ListFreeAudits(c.Request.Context(), service.FreeAuditFilter{UserID: userID, IP: c.Query("ip"), Fingerprint: c.Query("fingerprint"), Rule: c.Query("rule"), Start: start, End: end, Offset: offset, Limit: limit})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"items": items, "total": total})
}

func UpdateFreeGroupPenalty(c *gin.Context) {
	var request service.FreeManualPenalty
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	if err := service.ApplyFreeManualPenalty(c.Request.Context(), request, c.GetInt("id")); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"applied": true})
}

func ApplyForFreeGroup(c *gin.Context) {
	if c.GetBool("free_group_captcha_verified") {
		if err := model.VerifyFreeGroupHuman(c.GetInt("id"), time.Now().Add(24*time.Hour).Unix()); err != nil {
			common.ApiError(c, err)
			return
		}
	}
	if err := service.SubmitFreeGroupApplication(c.GetInt("id")); err != nil {
		status, message := service.FreeGroupRequestError(err)
		common.ApiErrorStatus(c, status, common.NewMessage(message))
		return
	}
	common.ApiSuccess(c, gin.H{"status": model.FreeGroupMemberPending})
}

func freeGroupAccessMessage(err error) string {
	if err == nil {
		return ""
	}
	_, message := service.FreeGroupRequestError(err)
	return message
}

func VerifyFreeGroupHuman(c *gin.Context) {
	if !c.GetBool("free_group_captcha_verified") {
		service.AbortFreeGroupRequest(c, service.ErrFreeGroupForbidden)
		return
	}
	if err := model.VerifyFreeGroupHuman(c.GetInt("id"), time.Now().Add(24*time.Hour).Unix()); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"verified": true})
}

func GetMyFreeGroupQueue(c *gin.Context) {
	status, err := service.InspectFreeQueue(c.Request.Context(), c.GetInt("id"), c.Param("queue_id"), false)
	if err != nil {
		service.AbortFreeGroupRequest(c, err)
		return
	}
	common.ApiSuccess(c, status)
}

func CancelMyFreeGroupQueue(c *gin.Context) {
	status, err := service.InspectFreeQueue(c.Request.Context(), c.GetInt("id"), c.Param("queue_id"), true)
	if err != nil {
		service.AbortFreeGroupRequest(c, err)
		return
	}
	common.ApiSuccess(c, status)
}

// PreviewFreeGroupConfig never persists the candidate or changes live admission.
func PreviewFreeGroupConfig(c *gin.Context) {
	request := freeGroupConfigUpdateRequest{Config: model.DefaultFreeGroupConfig()}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorStatus(c, 400, common.NewMessage("Invalid parameters"))
		return
	}
	if err := service.ValidateFreeGroupConfig(request.Config); err != nil {
		common.ApiErrorStatus(c, 400, err)
		return
	}
	preview, err := service.PreviewFreeGroupImpact(c.Request.Context(), request.Config)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, preview)
}

func PreviewFreeGroupPreset(c *gin.Context) {
	request := struct {
		Config model.FreeGroupConfig `json:"config"`
		Preset string                `json:"preset"`
	}{Config: model.DefaultFreeGroupConfig()}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorStatus(c, 400, common.NewMessage("Invalid parameters"))
		return
	}
	config, err := service.FreeGroupPreset(request.Config, request.Preset)
	if err != nil {
		common.ApiErrorStatus(c, 400, err)
		return
	}
	common.ApiSuccess(c, gin.H{"config": config, "strength_score": service.FreeGroupStrength(config), "high_risk": service.FreeGroupConfigHighRisk(config) || service.FreeGroupRelaxed(request.Config, config)})
}

func ListMyFreeGroupQueue(c *gin.Context) {
	items, err := service.ListFreeUserQueue(c.Request.Context(), c.GetInt("id"))
	if err != nil {
		service.AbortFreeGroupRequest(c, err)
		return
	}
	common.ApiSuccess(c, items)
}
