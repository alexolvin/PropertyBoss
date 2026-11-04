package probe

import "testing"

func TestIsAssetURL(t *testing.T) {
	for _, u := range []string{
		"https://www.bezrealitky.cz/_next/static/css/x.css",
		"https://api.bezrealitky.cz/media/cache/record_main/data/images/advert/1.jpg",
	} {
		if !isAssetURL(u) {
			t.Errorf("asset not detected: %s", u)
		}
	}
}

func TestScoreListingVsDetail(t *testing.T) {
	cat := "https://www.bezrealitky.cz/vypis/nabidka-prodej/byt"
	det := "https://www.bezrealitky.cz/nemovitosti-byty-domy/1061838-nabidka-prodej-bytu-sidliste"
	if scoreListingURL(cat) <= 0 {
		t.Errorf("catalog score=%d", scoreListingURL(cat))
	}
	if scoreListingURL(det) != 0 {
		t.Errorf("detail scored as catalog: %d", scoreListingURL(det))
	}
	got := pickDetailURLs("bezrealitky.cz", cat, []string{det, cat, "https://x.cz/a.jpg"}, 10)
	if len(got) != 1 || got[0] != det {
		t.Errorf("details=%v", got)
	}
}

func TestNextDataDetailURLs(t *testing.T) {
	html := `<script id="__NEXT_DATA__" type="application/json">{"uri":"1061838-nabidka-prodej-bytu-x"}</script>`
	hrefs := []string{"https://www.bezrealitky.cz/nemovitosti-byty-domy/1-nabidka-prodej-a"}
	got := nextDataDetailURLs("https://www.bezrealitky.cz", html, hrefs)
	if len(got) != 1 || got[0] != "https://www.bezrealitky.cz/nemovitosti-byty-domy/1061838-nabidka-prodej-bytu-x" {
		t.Errorf("got %v", got)
	}
}
