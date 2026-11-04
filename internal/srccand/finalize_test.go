package srccand

import (
	"encoding/json"
	"testing"
)

func TestClassifyArtifactTwoRunSkipped(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"class": "pending", "class_reason": "наблюдение 1/2 класса dead: timeout",
		"domain": "x.cz",
	})
	if _, _, ok := ClassifyArtifact(raw, ""); ok {
		t.Fatal("two-run must skip")
	}
}

func TestClassifyArtifactCeskeRealityMirror(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"class": "pending", "class_reason": "не direct: P7 n=0", "domain": "reality-cheb.cz",
		"field_sample": map[string]any{"coverage": map[string]int{"n": 0}},
		"steps":        []any{},
	})
	html := stringsRepeat("ceskereality ", 8)
	c, r, ok := ClassifyArtifact(raw, html)
	if !ok || c != "mirror" {
		t.Fatalf("got %s %s ok=%v", c, r, ok)
	}
}

func TestClassifyArtifactDirectP7(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"class": "pending", "class_reason": "не direct P6=false", "domain": "realingo.cz",
		"channel": "embedded_json",
		"field_sample": map[string]any{"coverage": map[string]int{
			"n": 30, "id": 30, "url": 30, "price_or_flag": 30, "deal_type": 30, "property_type": 30,
		}},
		"steps": []any{map[string]string{"id": "P6", "status": "not_measured"}},
	})
	c, _, ok := ClassifyArtifact(raw, "")
	if !ok || c != "direct" {
		t.Fatalf("got %s ok=%v", c, ok)
	}
}

func stringsRepeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
