package service

import (
	"context"
	"errors"
	"math"
	"reflect"
	"slices"

	"github.com/QuantumNous/new-api/model"
)

// FreeGroupPreset changes numerical protections only. Admission, model choices,
// overrides, notifications and the feature switch remain administrator owned.
func FreeGroupPreset(base model.FreeGroupConfig, preset string) (model.FreeGroupConfig, error) {
	type limits struct {
		user, token, ip, model, global    int
		rpm, burst, tpm, requests, tokens int64
		output                            int
	}
	profiles := map[string]limits{
		"very_strict":  {1, 1, 2, 3, 10, 1, 2, 1000, 5, 5000, 256},
		"strict":       {1, 1, 3, 5, 25, 2, 3, 1500, 10, 10000, 384},
		"standard":     {2, 2, 5, 10, 50, 3, 5, 2000, 20, 20000, 512},
		"relaxed":      {4, 4, 10, 25, 100, 10, 15, 10000, 100, 100000, 2048},
		"very_relaxed": {10, 10, 30, 100, 500, 60, 90, 100000, 1000, 1000000, 8192},
	}
	p, ok := profiles[preset]
	if !ok {
		return base, errors.New("Invalid Free preset")
	}
	base.Preset = preset
	for _, item := range []struct {
		rule  *model.FreeConcurrencyDimension
		limit int
	}{
		{&base.Concurrency.User, p.user}, {&base.Concurrency.Token, p.token}, {&base.Concurrency.IP, p.ip}, {&base.Concurrency.Model, p.model}, {&base.Concurrency.Global, p.global},
	} {
		item.rule.Enabled = true
		item.rule.Limit = item.limit
	}
	base.Rate.RPM = model.FreeBurstRule{Enabled: true, Limit: p.rpm, Burst: p.burst}
	base.Rate.TPM = model.FreeBurstRule{Enabled: true, Limit: p.tpm, Burst: p.tpm}
	base.Rate.DailyRequests.Enabled, base.Rate.DailyRequests.Limit = true, p.requests
	base.Rate.DailyTokens.Enabled, base.Rate.DailyTokens.Limit = true, p.tokens
	base.Request.MaxTokens.Enabled, base.Request.MaxTokens.Limit = true, p.output
	return base, nil
}

// FreeGroupStrength is a descriptive 0..100 score, not an admission decision.
// It includes enabled-rule coverage and normalized capacity relative to standard.
func FreeGroupStrength(config model.FreeGroupConfig) int {
	on, total := freeEnabledRules(reflect.ValueOf(config))
	if total == 0 {
		return 0
	}
	coverage := float64(on) / float64(total)
	baseline := model.DefaultFreeGroupConfig()
	tightness := 0.0
	for _, pair := range [][2]float64{
		{float64(baseline.Concurrency.Global.Limit), float64(config.Concurrency.Global.Limit)},
		{float64(baseline.Rate.RPM.Limit), float64(config.Rate.RPM.Limit)},
		{float64(baseline.Rate.DailyRequests.Limit), float64(config.Rate.DailyRequests.Limit)},
		{float64(baseline.Rate.DailyTokens.Limit), float64(config.Rate.DailyTokens.Limit)},
		{float64(baseline.Request.MaxTokens.Limit), float64(config.Request.MaxTokens.Limit)},
	} {
		if pair[1] > 0 {
			tightness += min(1, pair[0]/pair[1])
		}
	}
	return int(100 * coverage * (0.5 + 0.5*tightness/5))
}

func freeEnabledRules(value reflect.Value) (int, int) {
	if value.Kind() != reflect.Struct {
		return 0, 0
	}
	on, total := 0, 0
	for i := range value.NumField() {
		field := value.Field(i)
		name := value.Type().Field(i).Name
		if field.Kind() == reflect.Bool && (name == "Enabled" || len(name) > 7 && name[len(name)-7:] == "Enabled") {
			total++
			if field.Bool() {
				on++
			}
		} else if field.Kind() == reflect.Struct {
			enabled, count := freeEnabledRules(field)
			on += enabled
			total += count
		}
	}
	return on, total
}

// Relaxing any admission-independent threshold is recorded as high risk. This
// also covers custom edits and rollback, so clients cannot bypass confirmation
// by renaming a preset.
func FreeGroupRelaxed(previous, next model.FreeGroupConfig) bool {
	if previous.Models.OutsidePolicy == "reject" && next.Models.OutsidePolicy == "allow" {
		return true
	}
	if previous.Admission.Registration.Enabled && next.Admission.Registration.Limit < previous.Admission.Registration.Limit {
		return true
	}
	if previous.Admission.Bindings.Enabled {
		for _, binding := range previous.Admission.Bindings.Required {
			if !slices.Contains(next.Admission.Bindings.Required, binding) {
				return true
			}
		}
	}
	if previous.Models.WhitelistEnabled {
		for _, name := range next.Models.Models {
			if !slices.Contains(previous.Models.Models, name) {
				return true
			}
		}
	}
	if len(next.Penalty.WhitelistUsers) > len(previous.Penalty.WhitelistUsers) || len(next.Penalty.WhitelistIPs) > len(previous.Penalty.WhitelistIPs) {
		return true
	}
	return previous.AccessMode != "all" && next.AccessMode == "all" || freeRulesRelaxed(reflect.ValueOf(previous), reflect.ValueOf(next))
}

func freeRulesRelaxed(previous, next reflect.Value) bool {
	for i := range next.NumField() {
		a, b := previous.Field(i), next.Field(i)
		name := next.Type().Field(i).Name
		if b.Kind() == reflect.Struct && freeRulesRelaxed(a, b) {
			return true
		}
		if b.Kind() == reflect.Bool && (name == "Enabled" || len(name) > 7 && name[len(name)-7:] == "Enabled") && name != "Enabled" && a.Bool() && !b.Bool() {
			return true
		}
		if name == "Enabled" && a.Bool() && !b.Bool() && next.Type() != reflect.TypeFor[model.FreeGroupConfig]() {
			return true
		}
		if b.Kind() == reflect.Bool && name == "Allowed" && !a.Bool() && b.Bool() {
			return true
		}
		if name == "Limit" || name == "Burst" || name == "Percent" {
			switch b.Kind() {
			case reflect.Int, reflect.Int64:
				if b.Int() > a.Int() {
					return true
				}
			case reflect.Float64:
				if b.Float() > a.Float() {
					return true
				}
			}
		}
	}
	return false
}

// PreviewFreeGroupImpact samples at most 1000 accounts. Live IP/fingerprint and
// future usage cannot be predicted; the response explicitly labels that limit.
func PreviewFreeGroupImpact(ctx context.Context, config model.FreeGroupConfig) (map[string]any, error) {
	var total int64
	if err := model.DB.WithContext(ctx).Model(&model.User{}).Count(&total).Error; err != nil {
		return nil, err
	}
	var ids []int
	if err := model.DB.WithContext(ctx).Model(&model.User{}).Order("id").Limit(1000).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	before, after, changed := 0, 0, 0
	current := GetFreeGroupConfig().Config
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		oldErr, newErr := freeGroupEligibility(current, id), freeGroupEligibility(config, id)
		for _, err := range []error{oldErr, newErr} {
			if err != nil && !errors.Is(err, ErrFreeGroupDisabled) && !errors.Is(err, ErrFreeGroupForbidden) {
				return nil, err
			}
		}
		if oldErr == nil {
			before++
		}
		if newErr == nil {
			after++
		}
		if (oldErr == nil) != (newErr == nil) {
			changed++
		}
	}
	estimate := int64(0)
	if len(ids) > 0 {
		estimate = int64(math.Round(float64(changed) * float64(total) / float64(len(ids))))
	}
	return map[string]any{"total_users": total, "sampled_users": len(ids), "eligible_before": before, "eligible_after": after, "estimated_eligibility_changes": estimate, "limits_changed": !reflect.DeepEqual(current, config), "high_risk": FreeGroupConfigHighRisk(config) || FreeGroupRelaxed(current, config), "strength_score": FreeGroupStrength(config), "runtime_limits_not_estimated": true}, nil
}
