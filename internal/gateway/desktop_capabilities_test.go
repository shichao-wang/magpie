package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestClaudeDesktopHaikuCapabilities(t *testing.T) {
	up := setup(t, provider.Anthropic, &fake{})
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Anthropic: up.URL, Models: []string{"m1", "claude-haiku-4-5", "effort-model"}}); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"fake/claude-haiku-4-5", "fake/effort-model", "fake/private-effort"} {
		if err := provider.SetModelEfforts(ref, []string{"low", "high"}); err != nil {
			t.Fatal(err)
		}
	}
	before := DesktopTiers
	t.Cleanup(func() { DesktopTiers = before })
	for _, c := range []struct {
		ref, id string
		picker  bool
	}{
		{"fake/claude-haiku-4-5", "claude-haiku-magpie", false},
		{"fake/effort-model", "mythos-magpie-haiku", true},
		{"fake/m1", "claude-haiku-magpie", false},
		{"fake/private-model", "claude-haiku-magpie", false},
		{"fake/private-effort", "mythos-magpie-haiku", true},
		{"", "claude-haiku-magpie", false},
	} {
		DesktopTiers = func() map[string]string { return map[string]string{"haiku": c.ref} }
		models := desktopModels()
		m := models[2]
		if len(models) != 4 || m["id"] != c.id || m["anthropic_family_tier"] != "haiku" || m["reasoning"] != c.picker {
			t.Fatalf("%s: %v", c.ref, models)
		}
		for _, id := range []string{c.id, c.id + "[1m]", "anthropic/" + c.id} {
			if DesktopTier(id) != "haiku" || !desktopAccepts(id) || desktopPicker(strings.TrimPrefix(id, "anthropic/")) != c.picker {
				t.Errorf("%s: wrong client or routing capabilities", id)
			}
		}
		if desktopCatalogKey(c.id) == "claude-haiku-4-5" || !strings.HasSuffix(m["display_name"].(string), " · Haiku") || !desktopSmallFast(c.id) {
			t.Fatalf("alias lost custom name or small_fast: %v", m)
		}
	}
}

func TestClaudeDesktopHaikuGroupFallback(t *testing.T) {
	fresh(t)
	var bodies [][]byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		if modelOf(body) == "claude-opus-5" {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"error":{"message":"unavailable"}}`)
			return
		}
		io.WriteString(w, `{"id":"m","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Anthropic: up.URL, Models: []string{"claude-opus-5", "claude-haiku-4-5"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "reasoning", Name: "Reasoning", Members: []string{"fake/claude-opus-5", "fake/claude-haiku-4-5"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	before := StandIn
	StandIn = func(agent, model string) string { return "group/reasoning" }
	t.Cleanup(func() { StandIn = before })
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"mythos-magpie-haiku","max_tokens":32000,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", TokenFor("claude-desktop"))
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || len(bodies) != 2 {
		t.Fatalf("%d %s; %d attempts", rec.Code, rec.Body, len(bodies))
	}
	for i, body := range bodies {
		var got struct {
			Thinking struct {
				Type   string
				Budget int `json:"budget_tokens"`
			}
			OutputConfig map[string]any `json:"output_config"`
		}
		json.Unmarshal(body, &got)
		if i == 0 && (got.Thinking.Type != "adaptive" || got.OutputConfig["effort"] != "high") || i == 1 && (got.Thinking.Type != "enabled" || got.Thinking.Budget < 1024 || got.OutputConfig["effort"] != nil) {
			t.Fatalf("attempt %d: %s", i, body)
		}
	}
}

func TestClaudeDesktopHaikuThinkingActualModel(t *testing.T) {
	f := &fake{reply: `{"id":"m","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`, ctype: "application/json"}
	up := setup(t, provider.Anthropic, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Anthropic: up.URL, Models: []string{"claude-haiku-4-5", "claude-3-5-haiku-20241022", "claude-opus-5", "claude-opus-5-5", "claude-sonnet-5-5", "effort-model"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelEfforts("fake/effort-model", []string{"low", "high"}); err != nil {
		t.Fatal(err)
	}
	before := StandIn
	t.Cleanup(func() { StandIn = before })
	for _, c := range []struct {
		model, thinking, effort, wantType, wantEffort string
		wantBudget                                    int
	}{
		{"claude-haiku-4-5", `{"type":"enabled","budget_tokens":4096}`, "high", "enabled", "", 4096},
		{"claude-haiku-4-5", `{"type":"adaptive"}`, "low", "enabled", "", 4096},
		{"claude-haiku-4-5", `{"type":"disabled"}`, "high", "disabled", "", 0},
		{"claude-3-5-haiku-20241022", `{"type":"adaptive"}`, "high", "", "", 0},
		{"claude-opus-5", `{"type":"enabled","budget_tokens":4096}`, "high", "adaptive", "high", 0},
		{"claude-opus-5-5", `{"type":"disabled"}`, "high", "", "high", 0},
		{"claude-sonnet-5-5", `{"type":"disabled"}`, "max", "between_tools", "high", 0},
		{"effort-model", `{"type":"adaptive"}`, "max", "adaptive", "high", 0},
	} {
		t.Run(c.model+"/"+c.wantType+"/"+c.effort, func(t *testing.T) {
			StandIn = func(agent, tier string) string { return "fake/" + c.model }
			body := `{"model":"claude-haiku-magpie","max_tokens":32000,"thinking":` + c.thinking + `,"output_config":{"effort":"` + c.effort + `","format":{"type":"json_schema","schema":{"type":"object"}}},"metadata":{"user_id":"test"},"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`
			for _, alias := range []string{"claude-haiku-magpie", "mythos-magpie-haiku[1m]"} {
				body = string(rewriteModel([]byte(body), alias))
				for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
					req := httptest.NewRequest("POST", path, strings.NewReader(body))
					req.Header.Set("x-api-key", TokenFor("claude-desktop"))
					rec := httptest.NewRecorder()
					New().Handler().ServeHTTP(rec, req)
					var got struct {
						Model    string
						Thinking struct {
							Type   string
							Budget int `json:"budget_tokens"`
						}
						OutputConfig    map[string]json.RawMessage `json:"output_config"`
						Metadata        map[string]string
						Tools, Messages []json.RawMessage
					}
					if rec.Code != 200 || json.Unmarshal(f.got, &got) != nil {
						t.Fatalf("%d %s upstream %s", rec.Code, rec.Body, f.got)
					}
					var effort string
					json.Unmarshal(got.OutputConfig["effort"], &effort)
					if got.Model != c.model || got.Thinking.Type != c.wantType || got.Thinking.Budget != c.wantBudget || effort != c.wantEffort || len(got.OutputConfig["format"]) == 0 || got.Metadata["user_id"] != "test" || len(got.Tools) != 1 || len(got.Messages) != 1 {
						t.Fatalf("unexpected request: %s", f.got)
					}
				}
			}
		})
	}
}
