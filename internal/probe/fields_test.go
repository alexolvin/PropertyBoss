package probe

import "testing"

func TestParseSampleIDFromHyphenSlug(t *testing.T) {
	u := "https://www.bezrealitky.cz/nemovitosti-byty-domy/1065728-nabidka-pronajem-bytu-x"
	sm := parseSample(u, []byte(`Prodej bytu 3+1 64 m² Praha, 2 290 000 Kč`))
	if sm.ExternalID != "1065728" {
		t.Fatalf("id=%q", sm.ExternalID)
	}
	if !sm.HasPrice || sm.DealType == "" || sm.PropertyType == "" {
		t.Fatalf("%+v", sm)
	}
}
