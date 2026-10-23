package middleware

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func FreeGroupCaptcha() gin.HandlerFunc {
	return func(c *gin.Context) {
		config := service.GetFreeGroupConfig().Config
		if !config.Enabled {
			service.AbortFreeGroupRequest(c, service.ErrFreeGroupDisabled)
			return
		}
		required := config.Admission.Captcha.Enabled && (config.AccessMode == model.FreeGroupAccessModeCondition || config.AccessMode == model.FreeGroupAccessModeApply)
		if !required {
			c.Next()
			return
		}
		if config.Admission.Captcha.Method != "turnstile" || !common.TurnstileCheckEnabled {
			service.AbortFreeGroupRequest(c, service.ErrFreeGroupRedis)
			return
		}
		c.Set("free_group_captcha_verified", true)
		TurnstileCheckFresh()(c)
	}
}
