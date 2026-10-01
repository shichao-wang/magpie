package catalog

import "testing"

func TestThinkingOf(t *testing.T) {
	writeCatalog(t, `{
		"anthropic":{"models":{"claude-haiku-4-5":{"reasoning_options":[{"type":"budget_tokens"}]}}},
		"maker":{"models":{"switch":{"reasoning_options":[{"type":"toggle"}]},"plain":{},"levels":{"reasoning_options":[{"type":"effort","values":["low","high"]}]}}},
		"relay":{"models":{"plain":{"reasoning_options":[{"type":"effort","values":["high"]}]}}}
	}`)
	for _, c := range []struct {
		id, control string
		known       bool
	}{
		{"anthropic/Claude-Haiku-4-5", "budget_tokens", true},
		{"switch:free", "toggle", true},
		{"levels(high)", "effort", true},
		{"plain", "", true},
		{"unknown", "", false},
	} {
		got, known := ThinkingOf([]string{"anthropic", "maker", "relay"}, c.id)
		if got != c.control || known != c.known {
			t.Errorf("%s: %q %v, want %q %v", c.id, got, known, c.control, c.known)
		}
	}
}
