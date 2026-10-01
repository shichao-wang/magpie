package gateway

import (
	"regexp"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// DesktopTiers supplies one configuration snapshot per discovery request. Set by main.
var DesktopTiers func() map[string]string

// desktopTierModels adds only configured tiers to Desktop's complete catalog.
// Standard Claude ids have their names replaced by Desktop's signed catalog;
// custom aliases keep the routed name. Mythos enables its effort picker only
// when the actual target has levels. Tier suffixes keep equal targets distinct.
func desktopTierModels() []map[string]any {
	shown, hidden := provider.CatalogFor("claude-desktop")
	entries := append(shown[:len(shown):len(shown)], hidden...)
	configured := map[string]string{}
	if DesktopTiers != nil {
		configured = DesktopTiers()
	} else {
		for _, tier := range []string{"opus", "sonnet", "haiku", "fable"} {
			configured[tier] = desktopStandIn(tier)
		}
	}
	var data []map[string]any
	loadedServed := false
	for _, tier := range []string{"opus", "sonnet", "haiku", "fable"} {
		routed := configured[tier]
		if routed == "" {
			continue
		}
		if !loadedServed && !slices.ContainsFunc(entries, func(e provider.Entry) bool { return e.ID == routed }) {
			entries = provider.Served()
			loadedServed = true
		}
		e := provider.Entry{ID: routed, Name: routed}
		if found := slices.IndexFunc(entries, func(e provider.Entry) bool { return e.ID == routed }); found >= 0 {
			e = entries[found]
		} else if p, model, ok := provider.Resolve(routed); ok {
			e.Provider, e.Model, e.Efforts = p, model, p.Efforts(model)
		}
		e.Efforts = desktopEfforts(e)
		name := desktopName(e) + " · " + strings.ToUpper(tier[:1]) + tier[1:]
		id := "claude-" + tier + "-magpie"
		if tier == "fable" {
			// A claude-fable prefix enables Desktop's five levels even without
			// catalog metadata. Keep its family in metadata instead.
			id = "magpie-tier-fable"
		}
		if len(e.Efforts) > 0 {
			id = "mythos-magpie-" + tier
		}
		e.ID = id
		m := modelObject(e)
		m["display_name"] = name
		m["owned_by"], m["anthropic_family_tier"] = "anthropic", tier
		m["description"] = routed + " in magpie"
		data = append(data, m)
	}
	return data
}

var desktopTierName = regexp.MustCompile(`^(?:claude-(?:\d+(?:[-.]\d+)*-)?)?(opus|sonnet|haiku|fable)(?:[-.]\d+)*$`)

// DesktopTier identifies routing aliases and historical Claude tiers for agents and the gateway.
func DesktopTier(id string) string {
	id = strings.TrimSuffix(strings.ToLower(id), "[1m]")
	id = strings.TrimPrefix(id, "anthropic/")
	for _, tier := range []string{"opus", "sonnet", "haiku", "fable"} {
		if id == "mythos-magpie-"+tier || id == "claude-"+tier+"-magpie" || id == "magpie-tier-"+tier {
			return tier
		}
	}
	if tier := desktopTierName.FindStringSubmatch(id); tier != nil {
		return tier[1]
	}
	return ""
}

// Unqualified tier ids always route, including new Claude versions. Qualified
// provider/model references and decoded catalog aliases retain direct selection.
func desktopFixedTier(id string) bool {
	id = strings.TrimPrefix(strings.TrimSuffix(strings.ToLower(id), "[1m]"), "anthropic/")
	return !strings.Contains(id, "/") && DesktopTier(id) != ""
}
