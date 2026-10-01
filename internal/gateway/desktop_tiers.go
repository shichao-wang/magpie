package gateway

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// DesktopTiers supplies one configuration snapshot per discovery request. Set by main.
var DesktopTiers func() map[string]string

// desktopModels lists four stable routing aliases. Standard Claude ids have their
// display names replaced by Desktop's model catalog, even with display_name set.
// Mythos aliases keep its effort picker without matching a catalog model; the
// explicit anthropic_family_tier identifies the tier independently of the alias.
func desktopModels() []map[string]any {
	tiers := []struct{ id, name, tier string }{
		{"mythos-magpie-opus", "Claude Opus", "opus"},
		{"mythos-magpie-sonnet", "Claude Sonnet", "sonnet"},
		{"claude-haiku-magpie", "Claude Haiku", "haiku"},
		{"mythos-magpie-fable", "Claude Fable", "fable"},
	}
	data := make([]map[string]any, 0, len(tiers))
	shown, hidden := provider.CatalogFor("claude-desktop")
	entries := append(shown[:len(shown):len(shown)], hidden...)
	configured := map[string]string{}
	loadedServed := false
	if DesktopTiers != nil {
		configured = DesktopTiers()
	} else {
		for _, tier := range tiers {
			configured[tier.tier] = desktopStandIn(tier.tier)
		}
	}
	for _, tier := range tiers {
		e := provider.Entry{ID: tier.id, Model: tier.id, Name: tier.name}
		routed := configured[tier.tier]
		if routed == "" && len(shown) > 0 {
			routed = shown[0].ID
		}
		if routed != "" {
			if !loadedServed && !slices.ContainsFunc(entries, func(e provider.Entry) bool { return e.ID == routed }) {
				entries = provider.Served()
				loadedServed = true
			}
			name := routed
			for _, model := range entries {
				if model.ID == routed {
					name = desktopName(model)
					e.Context, e.Output = model.Context, model.Output
					break
				}
			}
			e.Name = name + " · " + strings.TrimPrefix(tier.name, "Claude ")
		}
		if tier.tier != "haiku" {
			e.Efforts = []string{"low", "medium", "high", "xhigh", "max"}
		}
		m := modelObject(e)
		m["owned_by"], m["anthropic_family_tier"] = "anthropic", tier.tier
		if routed != "" {
			m["description"] = routed + " in magpie"
		}
		data = append(data, m)
	}
	return data
}

var desktopTierName = regexp.MustCompile(`^(?:claude-(?:\d+(?:[-.]\d+)*-)?)?(opus|sonnet|haiku|fable)(?:[-.]\d+)*$`)

// DesktopTier identifies routing aliases and historical Claude tiers for agents and the gateway.
func DesktopTier(id string) string {
	id = strings.TrimSuffix(strings.ToLower(id), "[1m]")
	id = strings.TrimPrefix(id, "anthropic/")
	switch id {
	case "mythos-magpie-opus":
		return "opus"
	case "mythos-magpie-sonnet":
		return "sonnet"
	case "claude-haiku-magpie":
		return "haiku"
	case "mythos-magpie-fable":
		return "fable"
	}
	if tier := desktopTierName.FindStringSubmatch(id); tier != nil {
		return tier[1]
	}
	return ""
}

// Fixed tier ids always route; full turns on other served legacy models keep their model.
func desktopFixedTier(id string) bool {
	id = strings.TrimSuffix(strings.ToLower(id), "[1m]")
	switch id {
	case "opus", "sonnet", "haiku", "fable", "mythos-magpie-opus", "mythos-magpie-sonnet", "claude-haiku-magpie", "mythos-magpie-fable", "claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5", "claude-fable-5":
		return true
	}
	return false
}

// desktopThinking adapts Desktop's adaptive request to an older Claude model, preserving other fields.
func desktopThinking(body []byte, model string) []byte {
	if !claudeVersion.MatchString(strings.ToLower(model)) || adaptiveOnly(model) {
		return body
	}
	var v struct {
		Thinking struct {
			Type string `json:"type"`
		} `json:"thinking"`
		OutputConfig map[string]json.RawMessage `json:"output_config"`
		MaxTokens    int                        `json:"max_tokens"`
	}
	if json.Unmarshal(body, &v) != nil {
		return body
	}
	if v.Thinking.Type != "adaptive" {
		return body
	}
	budget := budgetOf(requestEffort(provider.Anthropic, body))
	maxTokens := v.MaxTokens
	if maxTokens <= 0 {
		maxTokens = max(16384, budget+4096)
	}
	fields := map[string]any{"max_tokens": maxTokens}
	if maxTokens <= 1024 {
		fields["thinking"] = map[string]any{"type": "disabled"}
	} else {
		fields["thinking"] = map[string]any{"type": "enabled", "budget_tokens": min(budget, max(1024, maxTokens-1024))}
	}
	delete(v.OutputConfig, "effort")
	if len(v.OutputConfig) > 0 {
		fields["output_config"] = v.OutputConfig
	} else {
		body = withoutFields(body, "output_config")
	}
	return withFields(body, fields)
}
