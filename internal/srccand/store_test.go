package srccand

import "testing"

func TestNormalizeDomain(t *testing.T) {
	cases := map[string]string{
		"https://www.Bezrealitky.cz/foo": "bezrealitky.cz",
		"WWW.SREALITY.CZ":                "sreality.cz",
		"reality.idnes.cz":               "reality.idnes.cz",
	}
	for in, want := range cases {
		if got := NormalizeDomain(in); got != want {
			t.Errorf("NormalizeDomain(%q)=%q want %q", in, got, want)
		}
	}
}

func TestValidVia(t *testing.T) {
	if !validVia("seed") || !validVia("owner") || !validVia("link:a.cz") {
		t.Fatal("valid via rejected")
	}
	if validVia("link:") || validVia("other") {
		t.Fatal("invalid via accepted")
	}
}
