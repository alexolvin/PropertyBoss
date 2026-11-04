package robots

import (
	"os"
	"path/filepath"
	"testing"
)

func load(t *testing.T, name string) *File {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return Parse(string(b))
}

const ua = "PropertyBoss/0.3 (real-estate price monitoring)"

func TestBazosAllowListingDenySearch(t *testing.T) {
	f := load(t, "bazos.cz.robots.txt")
	if !f.Allowed(ua, "https://reality.bazos.cz/prodam/byt/") {
		t.Error("категория /prodam/byt/ должна быть разрешена для *")
	}
	if f.Allowed(ua, "https://reality.bazos.cz/search.php") {
		t.Error("/search.php запрещён")
	}
	if f.Allowed(ua, "https://reality.bazos.cz/prodam/byt/?hledat=praha") {
		t.Error("/*hledat= запрещён")
	}
	if f.Allowed(ua, "https://reality.bazos.cz/prodam/byt/?cenaod=100") {
		t.Error("/*cenaod= запрещён")
	}
}

func TestSrealityStarDisallowAll(t *testing.T) {
	f := load(t, "sreality.cz.robots.txt")
	if f.Allowed(ua, "https://www.sreality.cz/") {
		t.Error("User-agent: * Disallow: / — главная запрещена нашему UA")
	}
	if f.Allowed(ua, "https://www.sreality.cz/hledani/prodej") {
		t.Error("листинг тоже под Disallow: /")
	}
	if f.Allowed(ua, "https://www.sreality.cz/api/cs/v2/estates") {
		t.Error("/api под Disallow: / для *")
	}
	// Googlebot — отдельная группа Allow: /
	if !f.Allowed("Googlebot/2.1", "https://www.sreality.cz/hledani/prodej") {
		t.Error("Googlebot Allow: / — листинг разрешён")
	}
}

func TestSbazarDollarAllowVsPrefixDisallow(t *testing.T) {
	f := load(t, "sbazar.cz.robots.txt")
	// Allow: /ulozene-nabidky$  vs Disallow: /ulozene-nabidky
	if !f.Allowed("Googlebot", "https://www.sbazar.cz/ulozene-nabidky") {
		t.Error("точный путь /ulozene-nabidky — Allow $ должен выиграть при равной длине")
	}
	if f.Allowed("Googlebot", "https://www.sbazar.cz/ulozene-nabidky/x") {
		t.Error("префикс /ulozene-nabidky/x — Disallow")
	}
	if len(f.Sitemaps) == 0 {
		t.Error("ожидался Sitemap:")
	}
}

func TestBezrealitkySearchDisallowListingAllow(t *testing.T) {
	f := load(t, "bezrealitky.cz.robots.txt")
	if !f.Allowed(ua, "https://www.bezrealitky.cz/") {
		t.Error("главная разрешена")
	}
	if !f.Allowed(ua, "https://www.bezrealitky.cz/nemovitosti-byty-domy") {
		t.Error("каталог без /search* разрешён")
	}
	if f.Allowed(ua, "https://www.bezrealitky.cz/search?q=praha") {
		t.Error("/search* запрещён")
	}
	if f.Allowed(ua, "https://www.bezrealitky.cz/vyhledat/byty") {
		t.Error("/vyhledat* запрещён")
	}
	if len(f.Sitemaps) != 1 {
		t.Errorf("Sitemap: got %v", f.Sitemaps)
	}
}

func TestIdnesAllowRoot(t *testing.T) {
	f := load(t, "reality.idnes.cz.robots.txt")
	if !f.Allowed(ua, "https://reality.idnes.cz/") {
		t.Error("Allow: / — корень разрешён")
	}
	if f.Allowed(ua, "https://reality.idnes.cz/s/foo?estateType=1") {
		t.Error("*estateType* запрещён")
	}
}

func TestUlovdomovAllowAll(t *testing.T) {
	f := load(t, "ulovdomov.cz.robots.txt")
	if !f.Allowed(ua, "https://www.ulovdomov.cz/pronajem") {
		t.Error("Allow: /")
	}
	if len(f.Sitemaps) != 1 {
		t.Errorf("Sitemap: %v", f.Sitemaps)
	}
}

func TestStarAndSpecific(t *testing.T) {
	f := Parse("User-agent: *\nDisallow: /\n\nUser-agent: PropertyBoss\nAllow: /\n")
	if !f.Allowed(ua, "/foo") {
		t.Error("специфичная группа PropertyBoss Allow: /")
	}
}
