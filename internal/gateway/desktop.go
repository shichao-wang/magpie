package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Claude Desktop, pointed at a third-party gateway, keeps a model only when
// its id reads as Anthropic's: it says claude, sonnet, opus, haiku, fable,
// mythos or anthropic, and nowhere names another vendor's model. Its check
// (qo in its app.asar, 2.7032) is
//
//	Vxe.test(id) ? false : Ko.test(id) || ["claude",…,"anthropic"].some(w => id.includes(w))
//
// on the lowercased id, with Vxe the list below. It runs on /v1/models rows,
// on Models entries typed in by hand ("model routing must reference an
// Anthropic model") and on the model a session is started with, so
// anthropic/deepseek/deepseek-flash is turned away for its "deepseek".
var (
	desktopDenied = regexp.MustCompile(`ark-code|astron|command-r|deepseek|doubao|gemini|gemma|glm|gpt|grok|hermes|hy3|kimi|lfm|\bling\b|llama|longcat|mimo|minimax|mistral|mixtral|moonshot|nemotron|openai|phi-|qianfan|qwen|tc-code|\bunic\b|yi-|stepfun|step-3|seed-|bytedance|hunyuan|granite|amazon\.nova|nova-|devstral|ministral|ernie|codex|arcee|trinity|abab|phi\d|\bk2\.|\bm2\.|jamba|arctic|solar|mercury|zamba|kat-coder|\bds-|dpsk`)
	desktopTier   = regexp.MustCompile(`^(sonnet|opus|haiku|fable|mythos)(-[\d.]+)?$`)
	desktopWords  = []string{"claude", "sonnet", "opus", "haiku", "fable", "mythos", "anthropic"}
)

// desktopAccepts is Claude Desktop's check of a gateway model id.
func desktopAccepts(id string) bool {
	l := strings.ToLower(id)
	if desktopTier.MatchString(l) {
		return true
	}
	if desktopDenied.MatchString(l) {
		return false
	}
	for _, w := range desktopWords {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// Aliases issued by earlier Desktop catalogs still resolve.
const (
	desktopAlias       = "anthropic/magpie-"
	desktopEffortAlias = "mythos-magpie-"
	desktopClaudeInfix = ".anthropic."
)

func aliasNumber(id string) string {
	h := fnv.New64a()
	h.Write([]byte(id))
	return fmt.Sprintf("%010d", h.Sum64()%1e10)
}

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
	served := provider.Served()
	var shown []provider.Entry
	names, narrowed := provider.VisibleTo("claude-desktop")
	for _, e := range served {
		if (e.Group != "" || !e.Provider.Unlisted) && (!narrowed || provider.Shows(names, e)) {
			shown = append(shown, e)
		}
	}
	configured := map[string]string{}
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
			name := routed
			for _, model := range served {
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

func desktopTierOf(id string) string { return DesktopTier(id) }

// Fixed tier ids always route; full turns on other served legacy models keep their model.
func desktopFixedTier(id string) bool {
	id = strings.TrimSuffix(strings.ToLower(id), "[1m]")
	switch id {
	case "opus", "sonnet", "haiku", "fable", "mythos-magpie-opus", "mythos-magpie-sonnet", "claude-haiku-magpie", "mythos-magpie-fable", "claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5", "claude-fable-5":
		return true
	}
	return false
}

func desktopName(e provider.Entry) string {
	if e.Name != "" {
		return e.Name
	}
	return e.ID
}

// aliased is the catalog id an alias Claude Desktop was given stands for:
// anthropic/magpie-<number> (as it was listed before too),
// mythos-magpie-<number> or magpie-<number>.anthropic.<claude model>, with
// or without Claude Code's "[1m]".
func aliased(id string) (string, bool) {
	id = strings.TrimSuffix(id, "[1m]")
	var number string
	if n, ok := strings.CutPrefix(id, desktopAlias); ok {
		number = n
	} else if n, ok := strings.CutPrefix(id, desktopEffortAlias); ok {
		number = n
	} else if rest, ok := strings.CutPrefix(id, "magpie-"); ok {
		if i := strings.Index(rest, desktopClaudeInfix); i > 0 {
			number = rest[:i]
		}
	}
	if len(number) != 10 {
		return "", false
	}
	for _, e := range provider.Catalog() {
		if aliasNumber(e.ID) == number {
			return e.ID, true
		}
	}
	return "", false
}

// isClaudeDesktop is a request from Claude Desktop's own gateway client: its
// Electron session's User-Agent ("Mozilla/5.0 … Claude/2.7032.0 Chrome/…"),
// which says nothing of it before the first slash.
func isClaudeDesktop(r *http.Request) bool {
	ua := r.Header.Get("User-Agent")
	return strings.HasPrefix(ua, "Mozilla/") && strings.Contains(ua, " Claude/")
}

// Desktop's four tier ids always use their own configuration. The last full
// turn is kept only as a fallback for older sessions' auxiliary model ids.
var desktopPicked struct {
	sync.Mutex
	model string
	from  string // the file it was read from
}

func desktopPickedPath() string { return filepath.Join(settings.Dir(), "claude-desktop.model") }

func desktopStandIn(model string) string {
	if StandIn == nil {
		return ""
	}
	model = strings.ToLower(strings.TrimSuffix(model, "[1m]"))
	if tier := desktopTierOf(model); tier != "" {
		model = tier
	}
	if m := StandIn("claude-desktop", model); m != "" && m != model {
		return m
	}
	return ""
}

// desktopSelection holds the lock only for selection state and its file, never provider resolution.
func desktopSelection(remember string) string {
	desktopPicked.Lock()
	defer desktopPicked.Unlock()
	path := desktopPickedPath()
	if desktopPicked.from != path {
		b, _ := os.ReadFile(path)
		desktopPicked.model, desktopPicked.from = strings.TrimSpace(string(b)), path
	}
	picked := desktopPicked.model
	if remember != "" {
		desktopPicked.model = remember
		if os.MkdirAll(settings.Dir(), 0o755) == nil {
			os.WriteFile(path, []byte(remember+"\n"), 0o600)
		}
	}
	return picked
}

func desktopSessionModel(picked string) string {
	if desktopTierOf(picked) != "" && (desktopFixedTier(picked) || unserved(picked)) {
		return desktopDefault(picked)
	}
	return picked
}

// desktopTurn resolves a generation request and remembers full turns carrying tools.
func desktopTurn(asked string, body []byte) string {
	return desktopResolve(asked, body, true)
}

// Counting uses the same resolution without remembering a selection.
func desktopResolve(asked string, body []byte, remember bool) string {
	if asked == "" {
		return asked
	}
	tools := hasTools(body)
	tier := desktopTierOf(asked)
	if tier != "" && (desktopFixedTier(asked) || unserved(asked) || !tools && small(body)) {
		if remember && tools {
			desktopSelection(asked)
		}
		if m := desktopDefault(asked); m != "" {
			return m
		}
		return asked
	}
	picked := desktopSelection("")
	if asked == picked {
		return asked
	}
	if unserved(asked) || !tools && small(body) && desktopAccepts(asked) {
		if m := desktopStandIn(asked); m != "" {
			return m
		}
		if picked != "" {
			if m := desktopSessionModel(picked); m != "" {
				return m
			}
		}
		if m := desktopFirst(asked); m != "" {
			return m
		}
		return asked
	}
	if remember && tools {
		desktopSelection(asked)
	}
	return asked
}

func desktopDefault(asked string) string {
	if m := desktopStandIn(asked); m != "" {
		return m
	}
	return desktopFirst(asked)
}

func desktopFirst(asked string) string {
	shown, _ := provider.CatalogFor("claude-desktop")
	if len(shown) == 0 || shown[0].ID == asked {
		return ""
	}
	return shown[0].ID
}

// small: the request asks for a short answer (max_tokens at most 4096), as
// Desktop's title does, not a session's turn.
func small(body []byte) bool {
	var v struct {
		MaxTokens int `json:"max_tokens"`
	}
	return json.Unmarshal(body, &v) == nil && v.MaxTokens > 0 && v.MaxTokens <= 4096
}

// hasTools: the request offers the model tools (Anthropic's, Chat's and
// Responses' "tools" alike).
func hasTools(body []byte) bool {
	if !bytes.Contains(body, []byte(`"tools"`)) {
		return false
	}
	var v struct {
		Tools []json.RawMessage `json:"tools"`
	}
	return json.Unmarshal(body, &v) == nil && len(v.Tools) > 0
}
