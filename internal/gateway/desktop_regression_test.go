package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

func TestClaudeDesktopTierCapacitySnapshot(t *testing.T) {
	up := setup(t, provider.Anthropic, &fake{})
	if err := catalog.SaveLive("fake", up.URL, []catalog.Model{{ID: "m1", Name: "Large", Context: 1_000_000, Output: 32768}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "large", Name: "Large group", Members: []string{"fake/m1"}, Context: 2_000_000}); err != nil {
		t.Fatal(err)
	}
	before := DesktopTiers
	calls := 0
	DesktopTiers = func() map[string]string {
		calls++
		return map[string]string{"opus": "fake/m1", "sonnet": "group/large"}
	}
	t.Cleanup(func() { DesktopTiers = before })
	for _, path := range []string{"/v1/models", "/v1/models/mythos-magpie-opus"} {
		calls = 0
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("x-api-key", TokenFor("claude-desktop"))
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 || calls != 1 {
			t.Fatalf("%s: status %d, %d configuration reads", path, rec.Code, calls)
		}
		var data struct {
			Data []map[string]any `json:"data"`
		}
		if path == "/v1/models" {
			json.Unmarshal(rec.Body.Bytes(), &data)
		} else {
			var model map[string]any
			json.Unmarshal(rec.Body.Bytes(), &model)
			data.Data = append(data.Data, model)
		}
		for _, m := range data.Data {
			want := float64(1_000_000)
			if m["id"] == "mythos-magpie-sonnet" || m["id"] == "claude-haiku-magpie" || m["id"] == "mythos-magpie-fable" {
				want = 2_000_000
			}
			for _, key := range []string{"context_window", "context_length", "max_input_tokens"} {
				if m[key] != want {
					t.Errorf("%s %s = %v, want %v", m["id"], key, m[key], want)
				}
			}
			if m["id"] == "mythos-magpie-opus" && m["max_output_tokens"] != float64(32768) {
				t.Errorf("output capacity: %v", m)
			}
		}
	}
}

func TestClaudeDesktopThinkingActualModel(t *testing.T) {
	f := &fake{reply: `{"id":"m","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`, ctype: "application/json"}
	up := setup(t, provider.Anthropic, f)
	models := []string{"claude-sonnet-4-5", "claude-opus-4-1", "claude-sonnet-4-6", "claude-opus-5"}
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Anthropic: up.URL, Models: models}); err != nil {
		t.Fatal(err)
	}
	before := StandIn
	t.Cleanup(func() { StandIn = before })
	for _, model := range models {
		StandIn = func(agent, tier string) string { return "fake/" + model }
		for _, cap := range []int{32000, 2048, 1024} {
			body := `{"model":"mythos-magpie-opus","max_tokens":CAP,"thinking":{"type":"adaptive"},"output_config":{"effort":"high","format":{"type":"json_schema","schema":{"type":"object"}}},"metadata":{"user_id":"test"},"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`
			body = strings.Replace(body, "CAP", strconv.Itoa(cap), 1)
			f.refuse = func(b []byte) (int, string) {
				var v struct {
					Thinking struct {
						Type   string `json:"type"`
						Budget int    `json:"budget_tokens"`
					} `json:"thinking"`
					OutputConfig map[string]any `json:"output_config"`
					Max          int            `json:"max_tokens"`
				}
				json.Unmarshal(b, &v)
				if !adaptiveOnly(model) && (v.Thinking.Type == "adaptive" || v.OutputConfig["effort"] != nil || v.Thinking.Type == "enabled" && (v.Thinking.Budget < 1024 || v.Thinking.Budget >= v.Max)) {
					return 400, `{"error":{"message":"invalid thinking parameters"}}`
				}
				return 0, ""
			}
			req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
			req.Header.Set("x-api-key", TokenFor("claude-desktop"))
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("%s cap %d: %d %s", model, cap, rec.Code, rec.Body)
			}
			var v struct {
				Model    string
				Thinking struct {
					Type   string `json:"type"`
					Budget int    `json:"budget_tokens"`
				} `json:"thinking"`
				OutputConfig map[string]json.RawMessage `json:"output_config"`
				Metadata     map[string]string
				Tools        []json.RawMessage
				Messages     []json.RawMessage
				Max          int `json:"max_tokens"`
			}
			if err := json.Unmarshal(f.got, &v); err != nil {
				t.Fatal(err)
			}
			wantType := "adaptive"
			if !adaptiveOnly(model) {
				wantType = "enabled"
				if cap <= 1024 {
					wantType = "disabled"
				}
			}
			if v.Model != model || v.Thinking.Type != wantType || v.Max != cap || len(v.OutputConfig["format"]) == 0 || v.Metadata["user_id"] != "test" || len(v.Tools) != 1 || len(v.Messages) != 1 {
				t.Fatalf("%s cap %d: %s", model, cap, f.got)
			}
		}
	}
}
