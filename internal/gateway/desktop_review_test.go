package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Every catalog model remains selectable; configured tiers are additional rows.
// A stale effort alias must not send unsupported controls to a plain target.
func TestClaudeDesktopCompleteCatalogAndPlainTiers(t *testing.T) {
	f := &fake{reply: `{"id":"m","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`, ctype: "application/json"}
	up := setup(t, provider.Anthropic, f)
	for _, p := range []provider.Provider{
		{ID: "fake", Name: "Fake A", Key: "k", Anthropic: up.URL, Models: []string{"fa-plain", "fa-x"}},
		{ID: "fb", Name: "Fake B", Key: "k", Anthropic: up.URL, Models: []string{"fb", "fb-x"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
		var models []catalog.Model
		for _, m := range p.Models {
			models = append(models, catalog.Model{ID: m, Name: m})
		}
		if err := catalog.SaveLive(p.ID, up.URL, models); err != nil {
			t.Fatal(err)
		}
	}
	before, beforeTiers := StandIn, DesktopTiers
	chosen := map[string]string{"opus": "fake/fa-plain", "haiku": "fake/fa-x"}
	StandIn = func(agent, model string) string { return chosen[DesktopTier(model)] }
	DesktopTiers = func() map[string]string { return chosen }
	t.Cleanup(func() { StandIn, DesktopTiers = before, beforeTiers })
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("x-api-key", TokenFor("claude-desktop"))
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, req)
	var list struct{ Data []map[string]any }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range list.Data {
		names = append(names, m["display_name"].(string))
	}
	if !reflect.DeepEqual(names, []string{"fa-plain", "fa-x", "fb", "fb-x", "fa-plain · Opus", "fa-x · Haiku"}) {
		t.Fatalf("catalog: %s", rec.Body)
	}
	for _, m := range list.Data[:4] {
		id := m["id"].(string)
		r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"`+id+`","max_tokens":32000,"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`))
		r.Header.Set("x-api-key", TokenFor("claude-desktop"))
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, r)
		if rec.Code != 200 || modelOf(f.got) != m["display_name"] {
			t.Fatalf("direct %s: %d %s upstream %s", id, rec.Code, rec.Body, f.got)
		}
	}
	for _, tier := range []string{"opus", "sonnet", "haiku", "fable"} {
		for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
			body := `{"model":"mythos-magpie-` + tier + `","max_tokens":32000,"thinking":{"type":"adaptive"},"output_config":{"effort":"max","format":{"type":"json_schema","schema":{"type":"object"}}},"messages":[{"role":"user","content":"hi"}]}`
			r := httptest.NewRequest("POST", path, strings.NewReader(body))
			r.Header.Set("x-api-key", TokenFor("claude-desktop"))
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, r)
			var sent struct {
				Model        string
				Thinking     json.RawMessage
				OutputConfig map[string]json.RawMessage `json:"output_config"`
			}
			want := "fa-plain"
			if tier == "haiku" {
				want = "fa-x"
			}
			if rec.Code != 200 || json.Unmarshal(f.got, &sent) != nil || sent.Model != want || len(sent.Thinking) > 0 || len(sent.OutputConfig["effort"]) > 0 || len(sent.OutputConfig["format"]) == 0 {
				t.Fatalf("%s %s: %d %s upstream %s", tier, path, rec.Code, rec.Body, f.got)
			}
		}
	}
	for _, id := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-fable-5-1", "anthropic/claude-opus-5-5[1m]", "claude-3-5-haiku-20241022"} {
		if !desktopFixedTier(id) {
			t.Errorf("new version not routed: %s", id)
		}
	}
	if desktopFixedTier("fake/claude-opus-5-5") {
		t.Fatal("direct reference treated as a tier")
	}
}
