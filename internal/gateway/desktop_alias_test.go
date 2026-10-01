package gateway

import (
	"regexp"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// desktopClaude is a model id that is Anthropic's own Claude model, as tIt
// knows it: claude-<tier>-<version>, a date taken off.
var (
	desktopClaude = regexp.MustCompile(`^claude-(?:opus|sonnet|haiku|fable|mythos)-\d+(?:-\d+)?$`)
	desktopDated  = regexp.MustCompile(`-\d{8}$`)
)

// Legacy generators are test fixtures for aliases already issued to Desktop.
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
