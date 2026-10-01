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

// desktopAlias is the prefix of the id a model is listed by to Claude
// Desktop when its own id names no Claude model: anthropic/magpie-<number>,
// the number a hash of magpie's id, so it stays the model's while the
// catalog changes around it. Desktop's list of other vendors' names grows
// from release to release, so no part of such a model's id is shown to it;
// its display_name and description carry the model's name and magpie id.
const desktopAlias = "anthropic/magpie-"

// Claude Desktop offers a thinking-effort picker only for a model it knows:
// in a gateway's mode it reads no effort from /v1/models (IIt keeps id,
// display_name, description, supports_1m and anthropic_family_tier), and
// its signed model catalog can't be a gateway's, so its levels come from
// tIt in its app.asar (index.chunk-D3OyLXgG.js, 2.7032):
//
//	let t=IC(e), n=HFt[t] ?? (UFt.test(t) ? VFt : void 0)
//
// HFt its table of Claude models (claude-sonnet-4-6, claude-opus-4-8, …),
// UFt /^(?:claude-)?(?:fable|mythos)(?:-|$)/ with VFt low, medium, high,
// xhigh and max (high recommended, thinking always on), and IC the id
// lowercased with a Bedrock-style "<profile>.anthropic." prefix and a date
// taken off. No provider/model id is either, so no model magpie served had
// the picker (ARNO). A model with reasoning levels is listed by one of
// these instead:
//
//   - a Claude model (claude-opus-4-8 at any provider):
//     magpie-<number>.anthropic.claude-opus-4-8, which IC reads as
//     claude-opus-4-8 and Claude Code as Claude Opus 4.8, as before;
//   - any other: mythos-magpie-<number>, which UFt matches. Nothing in it
//     says haiku, sonnet or opus, so Desktop's small_fast pick is as it
//     was, and Claude Code (which knows claude-mythos-… only) takes it for
//     a model it doesn't know and sends its effort as output_config.effort.
//
// The effort chosen reaches the gateway as thinking plus
// output_config.effort and is fitted to the model's own levels there.
const (
	desktopEffortAlias = "mythos-magpie-"
	desktopClaudeInfix = ".anthropic."
)

// desktopClaude is a model id that is Anthropic's own Claude model, as tIt
// knows it: claude-<tier>-<version>, a date taken off.
var (
	desktopClaude = regexp.MustCompile(`^claude-(?:opus|sonnet|haiku|fable|mythos)-\d+(?:-\d+)?$`)
	desktopDated  = regexp.MustCompile(`-\d{8}$`)
)

func aliasNumber(id string) string {
	h := fnv.New64a()
	h.Write([]byte(id))
	return fmt.Sprintf("%010d", h.Sum64()%1e10)
}

func aliasFor(id string) string { return desktopAlias + aliasNumber(id) }

// claudeModel is the Claude model an entry is, lowercased and without a
// date or a vendor's "anthropic/" in front, or "" when it is none.
func claudeModel(e provider.Entry) string {
	m := strings.ToLower(e.Model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	m = desktopDated.ReplaceAllString(m, "")
	if desktopClaude.MatchString(m) {
		return m
	}
	return ""
}

// claudeLooking is a model's id as Claude Desktop is shown it: one that
// gets its effort picker when the model has reasoning levels, else as it is
// when it already reads as a Claude model's, else its alias (unprefixed
// serves each again).
func claudeLooking(e provider.Entry) string {
	if len(e.Efforts) > 0 {
		if m := claudeModel(e); m != "" {
			return "magpie-" + aliasNumber(e.ID) + desktopClaudeInfix + m
		}
		return desktopEffortAlias + aliasNumber(e.ID)
	}
	if desktopAccepts(e.ID) && !strings.HasPrefix(e.ID, desktopAlias) {
		return e.ID
	}
	return aliasFor(e.ID)
}

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
	for _, tier := range tiers {
		e := provider.Entry{ID: tier.id, Model: tier.id, Name: tier.name}
		routed := desktopDefault(tier.id)
		if routed != "" {
			name := routed
			for _, model := range served {
				if model.ID == routed {
					name = desktopName(model)
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

func desktopTierOf(id string) string {
	id = strings.ToLower(strings.TrimSuffix(id, "[1m]"))
	switch id {
	case "opus", "claude-opus-5", "mythos-magpie-opus":
		return "opus"
	case "sonnet", "claude-sonnet-5", "mythos-magpie-sonnet":
		return "sonnet"
	case "haiku", "claude-haiku-4-5", "claude-haiku-magpie":
		return "haiku"
	case "fable", "claude-fable-5", "mythos-magpie-fable":
		return "fable"
	}
	return ""
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

func desktopSessionModel(picked string) string {
	if desktopTierOf(picked) != "" {
		if m := desktopStandIn(picked); m != "" {
			return m
		}
		return desktopDefault(picked)
	}
	return picked
}

// desktopTurn is the model a Claude Desktop request for asked is served by.
func desktopTurn(asked string, body []byte) string {
	if asked == "" {
		return asked
	}
	tools := hasTools(body)
	desktopPicked.Lock()
	defer desktopPicked.Unlock()
	if path := desktopPickedPath(); desktopPicked.from != path {
		b, _ := os.ReadFile(path)
		desktopPicked.model, desktopPicked.from = strings.TrimSpace(string(b)), path
	}
	picked := desktopPicked.model
	if desktopTierOf(asked) != "" {
		if tools {
			desktopPicked.model = asked
			if os.MkdirAll(settings.Dir(), 0o755) == nil {
				os.WriteFile(desktopPickedPath(), []byte(asked+"\n"), 0o600)
			}
		}
		if m := desktopStandIn(asked); m != "" {
			return m
		}
		if m := desktopDefault(asked); m != "" {
			return m
		}
		return asked
	}
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
		if m := desktopDefault(asked); m != "" {
			return m
		}
		return asked
	}
	if tools {
		desktopPicked.model = asked
		if os.MkdirAll(settings.Dir(), 0o755) == nil {
			os.WriteFile(desktopPickedPath(), []byte(asked+"\n"), 0o600)
		}
	}
	return asked
}

// desktopDefault uses a configured stand-in, else the first model in Desktop's
// available provider catalog. It returns empty when no different model exists.
func desktopDefault(asked string) string {
	if m := desktopStandIn(asked); m != "" {
		return m
	}
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
