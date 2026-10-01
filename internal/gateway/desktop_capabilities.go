package gateway

import (
	"encoding/json"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// desktopThinkingControl describes the routed model, independently of its tier.
// Prefer a vendor's catalog to resellers; Claude's wire modes are versioned,
// including manual thinking on Haiku 4.5 and no thinking on earlier Haikus.
func desktopThinkingControl(p provider.Provider, model string) (string, bool) {
	lower := strings.ToLower(model)
	if adaptiveOnly(model) || strings.Contains(lower, "claude-fable-") || strings.Contains(lower, "claude-mythos-") {
		return "adaptive", true
	}
	if m := claudeVersion.FindStringSubmatch(lower); m != nil {
		if strings.Contains(m[0], "haiku") {
			if m[1] == "4" && m[2] == "5" {
				return "budget_tokens", true
			}
			return "", true
		}
		if m[1] == "4" {
			return "budget_tokens", true
		}
	}
	if strings.Contains(lower, "claude-3-5-haiku") || strings.Contains(lower, "claude-3-haiku") {
		return "", true
	}
	if strings.Contains(lower, "claude-3-7-sonnet") {
		return "budget_tokens", true
	}
	if len(p.Efforts(model)) > 0 {
		return "effort", true
	}
	sources := p.Catalogs()
	for _, preset := range provider.Presets() {
		if preset.Kind == provider.KindVendor && !preset.Hosts {
			sources = append(sources, (provider.Provider{Catalog: preset.Catalog}).Catalogs()...)
		}
	}
	if control, known := catalog.ThinkingOf(sources, model); known {
		return control, true
	}
	// A live or subscription catalog also names plain models that models.dev
	// doesn't know. Manually entered references remain unknown.
	for _, m := range p.Available() {
		if m.ID == model {
			if m.Reasoning {
				return "reasoning", true
			}
			return "", true
		}
	}
	return "", false
}

// desktopEfforts uses the entry's kept levels, but never advertises effort
// for a manual-budget Claude even when a reseller labels it with levels.
// A private reference can use levels known to its provider or set by the user.
func desktopEfforts(e provider.Entry) []string {
	if len(e.Efforts) == 0 {
		return nil
	}
	if e.Group != "" {
		return e.Efforts // the group's shared or rule-selected levels, not just its first member's
	}
	control, known := desktopThinkingControl(e.Provider, e.Model)
	if known && control != "adaptive" && control != "effort" {
		return nil
	}
	return e.Efforts
}

// desktopModelThinking fits even a stale session alias to the current member.
// It runs before protocol translation, so a group retry and a Chat/Responses
// backend see the current model's controls rather than the alias's defaults.
func desktopModelThinking(body []byte, p provider.Provider, model string) []byte {
	control, known := desktopThinkingControl(p, model)
	if !known {
		return body
	}
	var v struct {
		Thinking struct {
			Type   string `json:"type"`
			Budget int    `json:"budget_tokens"`
		} `json:"thinking"`
		OutputConfig map[string]json.RawMessage `json:"output_config"`
		MaxTokens    int                        `json:"max_tokens"`
	}
	if json.Unmarshal(body, &v) != nil {
		return body
	}
	effort := requestEffort(provider.Anthropic, body)
	if e := fitFor(p, model, bodyEffort(provider.Anthropic, body)); e != "" {
		body = withBodyEffort(provider.Anthropic, body, e)
	}
	fields := map[string]any{}
	if control == "" || control == "budget_tokens" || control == "toggle" || control == "reasoning" {
		delete(v.OutputConfig, "effort")
		if len(v.OutputConfig) > 0 {
			fields["output_config"] = v.OutputConfig
		} else {
			body = withoutFields(body, "output_config")
		}
	}
	switch {
	case control == "":
		body = withoutFields(body, "thinking")
	case v.Thinking.Type == "disabled" && control == "adaptive":
		body = withoutFields(body, "thinking") // let the adaptive model use its own default
	case v.Thinking.Type == "enabled" || v.Thinking.Type == "adaptive":
		switch control {
		case "adaptive":
			var thinking map[string]json.RawMessage
			var original struct {
				Thinking json.RawMessage `json:"thinking"`
			}
			json.Unmarshal(body, &original)
			json.Unmarshal(original.Thinking, &thinking)
			delete(thinking, "budget_tokens")
			thinking["type"] = json.RawMessage(`"adaptive"`)
			fields["thinking"] = thinking
			if e := fitFor(p, model, effort); e != "" {
				body = withBodyEffort(provider.Anthropic, body, e)
			}
		case "budget_tokens":
			budget := v.Thinking.Budget
			if budget < 1024 {
				budget = budgetOf(effort)
			}
			cap := v.MaxTokens
			if cap <= 0 {
				cap = max(16384, budget+4096)
				fields["max_tokens"] = cap
			}
			if cap <= 1024 {
				fields["thinking"] = map[string]any{"type": "disabled"}
			} else {
				fields["thinking"] = map[string]any{"type": "enabled", "budget_tokens": min(budget, max(1024, cap-1024))}
			}
		case "toggle":
			fields["thinking"] = map[string]any{"type": "enabled"}
		}
	}
	return withFields(body, fields)
}
