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

// These aliases were issued by earlier Desktop catalogs and still resolve for
// existing sessions. New catalogs use the four tier ids in desktop_tiers.go.
//
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
// the picker (ARNO). Earlier catalogs listed a model with reasoning levels
// by one of these instead:
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

func aliasNumber(id string) string {
	h := fnv.New64a()
	h.Write([]byte(id))
	return fmt.Sprintf("%010d", h.Sum64()%1e10)
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

// Claude Desktop sends some requests on a model of its own choosing rather
// than the one its session is on. A session's title (and branch name) is
// asked for by one tool-less request whose model is its "small_fast" pick
// from the gateway's list — the first id with haiku in it, else sonnet,
// else opus (_$n in its app.asar, 2.7032), the session's model only when
// none has one — so {"model":"claude-sonnet-5-thinking","max_tokens":200,
// "system":"You write short session titles. …"} went to a Claude model the
// user never picked. Claude Code in its Code tab asks for its own
// claude-haiku-… by name for small tasks too.
//
// Desktop's four tier ids and historical tier requests use their own
// configuration, or the first visible catalog model when unconfigured.
// For older sessions using other ids, turns carrying tools remember the
// selected model as a fallback for unserved or small tool-less requests.
// It is kept on disk so these auxiliary requests can still resolve before
// the first full turn after a restart; before any selection, they use
// desktopDefault.
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
	if tier := DesktopTier(model); tier != "" {
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
	if DesktopTier(picked) != "" && (desktopFixedTier(picked) || unserved(picked)) {
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
	tier := DesktopTier(asked)
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

// desktopDefault uses the configured replacement for asked, else the first
// model shown to Desktop. A new session's first message is titled before it
// is sent (ARNO), so nothing may be picked yet when the title is asked for.
// Desktop starts new sessions on the first unrestricted /v1/models row
// (resolveDefaultSessionModel in its app.asar); unconfigured tiers use the
// first visible model from magpie's catalog. "" when that is asked itself
// or magpie shows Desktop no model.
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
