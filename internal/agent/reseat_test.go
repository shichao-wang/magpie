package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// reseatHome is a sandbox HOME with two providers, one of them "cop" in the
// place of Copilot, and Claude Code and Hermes Agent there.
func reseatHome(t *testing.T) string {
	t.Helper()
	home := effortHome(t)
	t.Setenv("PATH", t.TempDir()) // no agent's binary is found, or run
	for _, p := range []provider.Provider{
		{ID: "cop", Name: "Cop", Chat: "https://cop.example/v1", Key: "k", Models: []string{"gpt-5", "claude-sonnet-4.5", "only-here"}},
		{ID: "ds", Name: "DS", Chat: "https://ds.example/v1", Key: "k", Models: []string{"gpt-5", "claude-sonnet-4-5-20250929"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.MkdirAll(filepath.Join(home, ".hermes"), 0o755)
	return home
}

func mustFind(t *testing.T, id string) *Agent {
	t.Helper()
	a, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func mustApply(t *testing.T, a *Agent, key, v string) {
	t.Helper()
	if err := a.Apply(key, v); err != nil {
		t.Fatalf("%s %s=%s: %v", a.ID, key, v, err)
	}
	if got := a.Field(key).Get(); got != v {
		t.Fatalf("%s %s: %q, want %q", a.ID, key, got, v)
	}
}

// #200: a provider switched off takes its models away from the agents on
// them — to the same model from a provider still on, the same model spelt
// another way, else the agent's own default — not left for the next
// request to be refused.
func TestReseatOff(t *testing.T) {
	reseatHome(t)
	c, h := mustFind(t, "claude"), mustFind(t, "hermes")
	mustApply(t, c, "model", "cop/gpt-5")
	mustApply(t, c, "opus", "cop/claude-sonnet-4.5")
	mustApply(t, c, "haiku", "cop/only-here")
	mustApply(t, c, "sonnet", "ds/gpt-5")
	mustApply(t, h, "model", "magpie/cop/only-here")

	moves, err := Reseat(func() error { return provider.SetOff("cop", true) })
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"model": "ds/gpt-5", "opus": "ds/claude-sonnet-4-5-20250929",
		"haiku": "", "sonnet": "", "fable": ""} // haiku follows the main model; sonnet was ds/gpt-5 already
	for k, w := range want {
		if got := c.Field(k).Get(); got != w {
			t.Errorf("claude %s: %q, want %q", k, got, w)
		}
	}
	if got := h.Field("model").Get(); got != "" {
		t.Errorf("hermes: %q, want its own default", got)
	}
	got := map[string]Move{}
	for _, m := range moves {
		got[m.Agent+" "+m.Field] = m
	}
	for k, w := range map[string]Move{
		"Claude Code model":  {Agent: "Claude Code", Field: "model", From: "cop/gpt-5", To: "ds/gpt-5"},
		"Claude Code opus":   {Agent: "Claude Code", Field: "opus", From: "cop/claude-sonnet-4.5", To: "ds/claude-sonnet-4-5-20250929"},
		"Claude Code haiku":  {Agent: "Claude Code", Field: "haiku", From: "cop/only-here"}, // follows the main model
		"Hermes Agent model": {Agent: "Hermes Agent", Field: "model", From: "cop/only-here"},
	} {
		if got[k] != w {
			t.Errorf("%s: %+v, want %+v", k, got[k], w)
		}
	}
	if len(moves) != 4 {
		t.Errorf("moves: %+v", moves)
	}

	// switched on again, nothing is moved back or elsewhere
	moves, err = Reseat(func() error { return provider.SetOff("cop", false) })
	if err != nil || len(moves) != 0 {
		t.Fatalf("on again: %+v %v", moves, err)
	}
}

// A provider removed moves its agents as one switched off does; one no
// agent is on moves nothing.
func TestReseatDelete(t *testing.T) {
	reseatHome(t)
	c := mustFind(t, "claude")
	mustApply(t, c, "model", "cop/only-here")
	moves, err := Reseat(func() error { return provider.Delete("ds") })
	if err != nil || len(moves) != 0 {
		t.Fatalf("an unused provider: %+v %v", moves, err)
	}
	if moves, err = Reseat(func() error { return provider.Delete("cop") }); err != nil || len(moves) != 1 {
		t.Fatalf("delete: %+v %v", moves, err)
	}
	// nothing else serves it: Claude Code as installed
	if got := c.Field("model").Get(); got != "" {
		t.Fatalf("claude: %q", got)
	}
}

func TestReseatClaudeDesktopTiers(t *testing.T) {
	home := reseatHome(t)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	p := desktopPathsOf(desktopDirs(runtime.GOOS, home, os.Getenv))
	os.MkdirAll(p.dir, 0o755)
	a := mustFind(t, "claude-desktop")
	mustApply(t, a, "provider", "magpie")
	mustApply(t, a, "opus", "cop/claude-sonnet-4.5")
	mustApply(t, a, "haiku", "cop/only-here")
	mustApply(t, a, "sonnet", "ds/gpt-5")

	moves, err := Reseat(func() error { return provider.SetOff("cop", true) })
	if err != nil {
		t.Fatal(err)
	}
	for tier, want := range map[string]string{"opus": "ds/claude-sonnet-4-5-20250929", "haiku": "", "sonnet": "ds/gpt-5"} {
		if got := a.Field(tier).Get(); got != want {
			t.Errorf("%s: %q, want %q", tier, got, want)
		}
	}
	got := map[string]Move{}
	for _, m := range moves {
		got[m.Agent+" "+m.Field] = m
	}
	if got["Claude Desktop opus"].To != "ds/claude-sonnet-4-5-20250929" || got["Claude Desktop haiku"].To != "" {
		t.Fatalf("Desktop tiers weren't reseated: %+v", moves)
	}
}

func TestModelKey(t *testing.T) {
	for a, b := range map[string]string{
		"claude-sonnet-4.5":         "claude-sonnet-4-5-20250929",
		"anthropic/claude-opus-4.6": "claude-opus-4-6",
		"gpt-5.1-codex":             "GPT-5.1-codex",
		"gemini-2.5-pro-2025-06-17": "gemini-2.5-pro",
	} {
		if modelKey(a) != modelKey(b) {
			t.Errorf("%s ≠ %s", a, b)
		}
	}
	if modelKey("gpt-5") == modelKey("gpt-5-mini") {
		t.Error("gpt-5 = gpt-5-mini")
	}
}
