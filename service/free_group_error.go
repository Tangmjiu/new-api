package service

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	freeRejections       = promauto.NewCounterVec(prometheus.CounterOpts{Name: "new_api_free_rejections_total", Help: "Free admission rejections by configured rule."}, []string{"rule"})
	freeComponentErrors  = promauto.NewCounterVec(prometheus.CounterOpts{Name: "new_api_free_component_errors_total", Help: "Free component failures, always fail closed."}, []string{"component"})
	freeQueueWaitSeconds = promauto.NewHistogram(prometheus.HistogramOpts{Name: "new_api_free_queue_wait_seconds", Help: "Time spent waiting for Free relay capacity."})
)

type FreeGroupRejection struct {
	Rule       string  `json:"rule"`
	Current    float64 `json:"current"`
	Limit      float64 `json:"limit"`
	RetryAfter int64   `json:"retry_after"`
	ResetAt    int64   `json:"reset_at"`
	Recovery   string  `json:"recovery"`
	Status     int     `json:"-"`
}

func (e *FreeGroupRejection) Error() string {
	return fmt.Sprintf("Free limit %s: %g/%g; retry after %d seconds", e.Rule, e.Current, e.Limit, e.RetryAfter)
}

func AbortFreeGroupRequest(c *gin.Context, err error) {
	status, message := FreeGroupRequestError(err)
	rule := &FreeGroupRejection{Rule: "internal", Status: status, Recovery: "service_recovery"}
	switch {
	case errors.Is(err, ErrFreeGroupDisabled):
		rule.Rule = "enabled"
		rule.Recovery = "administrator_enable"
		message = i18n.T(c, "free_group.disabled")
	case errors.Is(err, ErrFreeGroupForbidden):
		rule.Rule = "access"
		rule.Recovery = "eligibility_change"
		message = i18n.T(c, "free_group.forbidden")
	case errors.Is(err, ErrFreeGroupModelForbidden):
		rule.Rule = "models.whitelist"
		rule.Recovery = "configuration_change"
		message = i18n.T(c, "free_group.model_forbidden")
	default:
		var rejection *FreeGroupRejection
		if errors.As(err, &rejection) {
			rule = rejection
			status = rule.Status
			message = i18n.T(c, "free_group.limit", map[string]any{"Rule": rule.Rule, "Current": rule.Current, "Limit": rule.Limit, "RetryAfter": rule.RetryAfter, "Recovery": rule.Recovery})
		} else {
			message = i18n.T(c, "free_group.unavailable")
			freeComponentErrors.WithLabelValues("admission").Inc()
		}
	}
	if rule.RetryAfter > 0 {
		c.Header("Retry-After", strconv.FormatInt(rule.RetryAfter, 10))
	}
	freeRejections.WithLabelValues(rule.Rule).Inc()
	if model.DB != nil {
		encoded, marshalErr := common.Marshal(rule)
		if marshalErr == nil {
			userID := common.GetContextKeyInt(c, "id")
			if userID <= 0 {
				userID = c.GetInt("id")
			}
			if err := model.DB.WithContext(c.Request.Context()).Create(&model.FreeGroupAudit{UserID: userID, IP: FreeGroupClientIP(c), RuleName: rule.Rule, Threshold: strconv.FormatFloat(rule.Limit, 'g', -1, 64), Snapshot: string(encoded), Action: "reject", Source: "auto", ConfigVersion: GetFreeGroupConfig().Version, CreatedAt: time.Now().Unix()}).Error; err != nil {
				freeComponentErrors.WithLabelValues("audit").Inc()
			}
		}
	}
	if rule.Status == 429 {
		if template := GetFreeGroupConfig().Config.Notify.ErrorTemplate; template != "" {
			message = strings.NewReplacer("{{rule}}", rule.Rule, "{{current}}", strconv.FormatFloat(rule.Current, 'g', -1, 64), "{{limit}}", strconv.FormatFloat(rule.Limit, 'g', -1, 64), "{{retry_after}}", strconv.FormatInt(rule.RetryAfter, 10)).Replace(template)
		}
	}
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"type": "free_group_error", "code": "free_group_" + rule.Rule, "message": message, "details": rule}})
}
