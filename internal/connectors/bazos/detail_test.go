// detail_test.go — тесты парсера страницы объявления.
//
// Фикстуры в testdata — синтетические реплики структуры реальных
// страниц reality.bazos.cz (структура проверена 2026-09-06 на четырёх
// реальных объявлениях; сами страницы содержат личные данные
// продавцов и в репозиторий не коммитятся).
package bazos

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return string(b)
}

// mustFloat — *Attr → float64 (тест: значение не nil).
func mustFloat(t *testing.T, a *Attr, what string) float64 {
	t.Helper()
	if a == nil {
		t.Fatalf("%s: ожидалось значение, получено nil", what)
	}
	v, ok := a.Value.(float64)
	if !ok {
		t.Fatalf("%s: ожидался float64, получено %T (%v)", what, a.Value, a.Value)
	}
	return v
}

// mustInt — *Attr → int.
func mustInt(t *testing.T, a *Attr, what string) int {
	t.Helper()
	if a == nil {
		t.Fatalf("%s: ожидалось значение, получено nil", what)
	}
	v, ok := a.Value.(int)
	if !ok {
		t.Fatalf("%s: ожидался int, получено %T (%v)", what, a.Value, a.Value)
	}
	return v
}

// mustString — *Attr → string.
func mustString(t *testing.T, a *Attr, what string) string {
	t.Helper()
	if a == nil {
		t.Fatalf("%s: ожидалось значение, получено nil", what)
	}
	v, ok := a.Value.(string)
	if !ok {
		t.Fatalf("%s: ожидался string, получено %T (%v)", what, a.Value, a.Value)
	}
	return v
}

func TestParseDetailFull(t *testing.T) {
	d, err := ParseDetail(loadFixture(t, "full.html"))
	if err != nil {
		t.Fatalf("ParseDetail: %v", err)
	}
	if d.Title != "Prodej bytu 3kk Testov" {
		t.Errorf("Title = %q", d.Title)
	}
	if d.PriceMinor == nil || *d.PriceMinor != 1390000000 {
		t.Errorf("PriceMinor = %v, ожидалось 1390000000", d.PriceMinor)
	}
	if d.Currency != "CZK" {
		t.Errorf("Currency = %q", d.Currency)
	}
	if d.Postcode != "152 00" {
		t.Errorf("Postcode = %q", d.Postcode)
	}
	if d.City != "Testov" {
		t.Errorf("City = %q", d.City)
	}
	if d.SellerName != "Jan Novák" {
		t.Errorf("SellerName = %q", d.SellerName)
	}
	if d.SellerUserID != "111111" {
		t.Errorf("SellerUserID = %q", d.SellerUserID)
	}
	if d.SourceLat == nil || *d.SourceLat != 50.037352 {
		t.Errorf("SourceLat = %v", d.SourceLat)
	}
	if d.SourceLng == nil || *d.SourceLng != 14.382048 {
		t.Errorf("SourceLng = %v", d.SourceLng)
	}
	if d.SourceZoom == nil || *d.SourceZoom != 12 {
		t.Errorf("SourceZoom = %v", d.SourceZoom)
	}
	if d.Views == nil || *d.Views != 5379 {
		t.Errorf("Views = %v", d.Views)
	}
	if len(d.Photos) != 3 {
		t.Errorf("Photos = %v (ожидалось 3 полных)", d.Photos)
	} else if d.Photos[0] != "https://www.bazos.cz/img/1/111/111111111.jpg?t=1" {
		t.Errorf("Photos[0] = %q", d.Photos[0])
	}
	// Структурированный блок (source_field).
	if d.Block["Plocha"] != "Užitná plocha 80 m²" {
		t.Errorf("Block[Plocha] = %q", d.Block["Plocha"])
	}
	if d.Block["Příslušenství"] != "Terasa o ploše 21 m²; Sklep" {
		t.Errorf("Block[Příslušenství] = %q", d.Block["Příslušenství"])
	}
	if d.Block["Stavba"] != "Cihlová, Jednopodlažní, Ve velmi dobrém stavu, 2. podlaží" {
		t.Errorf("Block[Stavba] = %q", d.Block["Stavba"])
	}
	if d.Block["Celková cena"] != "14500000 Kč" {
		t.Errorf("Block[Celková cena] = %q", d.Block["Celková cena"])
	}
}

func TestDeriveFull(t *testing.T) {
	d, err := ParseDetail(loadFixture(t, "full.html"))
	if err != nil {
		t.Fatalf("ParseDetail: %v", err)
	}
	rc := RuleConfidence{Title: 0.9, Description: 0.8}
	r := Derive(d, rc)

	if v := mustFloat(t, r.Area, "Area"); v != 80 {
		t.Errorf("Area = %v", v)
	}
	if r.Area.Method != "source_field" || r.Area.Confidence != 1.0 {
		t.Errorf("Area provenance = %s/%v", r.Area.Method, r.Area.Confidence)
	}
	if v := mustInt(t, r.Rooms, "Rooms"); v != 3 {
		t.Errorf("Rooms = %v", v)
	}
	if r.Rooms.Confidence != 0.9 { // из заголовка
		t.Errorf("Rooms conf = %v", r.Rooms.Confidence)
	}
	if v := mustInt(t, r.Floor, "Floor"); v != 2 {
		t.Errorf("Floor = %v", v)
	}
	if r.Floor.Method != "source_field" {
		t.Errorf("Floor method = %s", r.Floor.Method)
	}
	if v := mustInt(t, r.Floors, "Floors"); v != 1 { // Jednopodlažní
		t.Errorf("Floors = %v", v)
	}
	if s := mustString(t, r.Material, "Material"); s != "cihlova" {
		t.Errorf("Material = %q", s)
	}
	if s := mustString(t, r.EnergyClass, "EnergyClass"); s != "usporne" {
		t.Errorf("EnergyClass = %q", s)
	}
	if s := mustString(t, r.Ownership, "Ownership"); s != "osobni" {
		t.Errorf("Ownership = %q", s)
	}
	if s := mustString(t, r.Condition, "Condition"); s != "very_good" {
		t.Errorf("Condition = %q", s)
	}
	// Честные nil: на странице нет.
	if r.HasElevator != nil {
		t.Errorf("HasElevator = %v, ожидалось nil", r.HasElevator.Value)
	}
	if r.NewBuild != nil || r.YearBuilt != nil || r.MonthlyCZK != nil || r.PlotArea != nil {
		t.Errorf("ожидались nil: NewBuild=%v YearBuilt=%v Monthly=%v Plot=%v",
			r.NewBuild, r.YearBuilt, r.MonthlyCZK, r.PlotArea)
	}
	if r.TotalArea != nil {
		t.Errorf("TotalArea = %v, ожидалось nil", r.TotalArea.Value)
	}
	if r.Street != "Testovací" {
		t.Errorf("Street = %q", r.Street)
	}
}

func TestDeriveTitleArea(t *testing.T) {
	d, err := ParseDetail(loadFixture(t, "title-area.html"))
	if err != nil {
		t.Fatalf("ParseDetail: %v", err)
	}
	rc := RuleConfidence{Title: 0.9, Description: 0.8}
	r := Derive(d, rc)

	if d.PriceMinor == nil || *d.PriceMinor != 620000000 {
		t.Errorf("PriceMinor = %v", d.PriceMinor)
	}
	if d.SourceZoom == nil || *d.SourceZoom != 15 {
		t.Errorf("SourceZoom = %v", d.SourceZoom)
	}
	// Площадь только в заголовке: «82,01 m2» (чешская десятичная запятая).
	if v := mustFloat(t, r.Area, "Area"); v != 82.01 {
		t.Errorf("Area = %v", v)
	}
	if r.Area.Method != "rule_markup" || r.Area.Confidence != 0.9 {
		t.Errorf("Area provenance = %s/%v (ожидалось rule_markup/0.9 из заголовка)", r.Area.Method, r.Area.Confidence)
	}
	if v := mustFloat(t, r.TotalArea, "TotalArea"); v != 82.01 {
		t.Errorf("TotalArea = %v", v)
	}
	if v := mustInt(t, r.Rooms, "Rooms"); v != 2 { // «2+KK»
		t.Errorf("Rooms = %v", v)
	}
	if v := mustInt(t, r.Floor, "Floor"); v != 2 { // «ve druhém patře» (порядковое)
		t.Errorf("Floor = %v", v)
	}
	if v := mustInt(t, r.Floors, "Floors"); v != 3 { // «třipodlažního» (дом, не квартира)
		t.Errorf("Floors = %v", v)
	}
	if s := mustString(t, r.EnergyClass, "EnergyClass"); s != "A" { // «třídě A»
		t.Errorf("EnergyClass = %q", s)
	}
	if r.EnergyClass.Method != "rule_markup" || r.EnergyClass.Confidence != 0.8 {
		t.Errorf("Energy provenance = %s/%v", r.EnergyClass.Method, r.EnergyClass.Confidence)
	}
	if v, ok := r.NewBuild.Value.(bool); !ok || !v {
		t.Errorf("NewBuild = %v, ожидалось true (úplně nový)", r.NewBuild)
	}
	if v := mustFloat(t, r.MonthlyCZK, "MonthlyCZK"); v != 2000 { // диапазон → нижняя граница
		t.Errorf("MonthlyCZK = %v", v)
	}
	if r.Material != nil || r.Condition != nil || r.Ownership != nil || r.HasElevator != nil {
		t.Errorf("ожидались nil: Material=%v Condition=%v Ownership=%v Elevator=%v",
			r.Material, r.Condition, r.Ownership, r.HasElevator)
	}
	if r.Street != "" {
		t.Errorf("Street = %q, ожидалось пусто", r.Street)
	}
}

func TestDeriveNoMap(t *testing.T) {
	d, err := ParseDetail(loadFixture(t, "no-map.html"))
	if err != nil {
		t.Fatalf("ParseDetail: %v", err)
	}
	// Честные nil: ссылки на карту нет — координат и PSČ на странице нет.
	if d.SourceLat != nil || d.SourceLng != nil || d.SourceZoom != nil {
		t.Errorf("ожидались nil координаты: %v %v %v", d.SourceLat, d.SourceLng, d.SourceZoom)
	}
	if d.Postcode != "" || d.City != "" {
		t.Errorf("ожидались пустые: Postcode=%q City=%q", d.Postcode, d.City)
	}
	rc := RuleConfidence{Title: 0.9, Description: 0.8}
	r := Derive(d, rc)
	if v := mustFloat(t, r.Area, "Area"); v != 28 { // «28 m²» в заголовке
		t.Errorf("Area = %v", v)
	}
	if v := mustInt(t, r.Rooms, "Rooms"); v != 1 { // «1+kk»
		t.Errorf("Rooms = %v", v)
	}
	if v := mustInt(t, r.Floor, "Floor"); v != 4 { // «4. nadzemním podlaží»
		t.Errorf("Floor = %v", v)
	}
	if v, ok := r.HasElevator.Value.(bool); !ok || !v { // «s výtahem»
		t.Errorf("HasElevator = %v", r.HasElevator)
	}
	if s := mustString(t, r.Ownership, "Ownership"); s != "osobni" { // «OV» в заголовке
		t.Errorf("Ownership = %q", s)
	}
	if r.Street != "Přadlácká" {
		t.Errorf("Street = %q", r.Street)
	}
}

// TestDeriveRenovated — модель реального объявления 223028684:
// «původně 3+1, nyní 4+1» (последнее вхождение N+1 выигрывает),
// «kompletně zrekonstruovaný» (renovated), «čtyřpodlažního»
// (этажность из словесного числительного).
func TestDeriveRenovated(t *testing.T) {
	d, err := ParseDetail(loadFixture(t, "renovated.html"))
	if err != nil {
		t.Fatalf("ParseDetail: %v", err)
	}
	if d.Title != "Prodáme náš zrekonstruovaný byt v Děčíně–Bynově" {
		t.Errorf("Title = %q", d.Title)
	}
	if d.PriceMinor == nil || *d.PriceMinor != 424900000 {
		t.Errorf("PriceMinor = %v, ожидалось 424900000", d.PriceMinor)
	}
	if d.SellerName != "Jana Malá" {
		t.Errorf("SellerName = %q", d.SellerName)
	}
	if d.Postcode != "405 05" || d.City != "Děčín" {
		t.Errorf("Postcode = %q City = %q", d.Postcode, d.City)
	}
	if len(d.Photos) != 2 {
		t.Errorf("Photos = %v (ожидалось 2 полных)", d.Photos)
	}
	rc := RuleConfidence{Title: 0.9, Description: 0.8}
	r := Derive(d, rc)
	if v := mustFloat(t, r.Area, "Area"); v != 84 { // «má 84 m2»
		t.Errorf("Area = %v", v)
	}
	if v := mustInt(t, r.Rooms, "Rooms"); v != 4 { // «3+1 … nyní 4+1» → последнее
		t.Errorf("Rooms = %v", v)
	}
	if v := mustInt(t, r.Floor, "Floor"); v != 4 { // «ve 4. patře»
		t.Errorf("Floor = %v", v)
	}
	if v := mustInt(t, r.Floors, "Floors"); v != 4 { // «čtyřpodlažního»
		t.Errorf("Floors = %v", v)
	}
	if s := mustString(t, r.Material, "Material"); s != "panelova" {
		t.Errorf("Material = %q", s)
	}
	if s := mustString(t, r.Condition, "Condition"); s != "renovated" {
		t.Errorf("Condition = %q", s)
	}
	if v, ok := r.HasElevator.Value.(bool); !ok || !v { // «s výtahem»
		t.Errorf("HasElevator = %v", r.HasElevator)
	}
	if s := mustString(t, r.Ownership, "Ownership"); s != "osobni" {
		t.Errorf("Ownership = %q", s)
	}
	if r.Street != "" {
		t.Errorf("Street = %q, ожидалось пусто", r.Street)
	}
}

func TestParseDetailLayoutChange(t *testing.T) {
	_, err := ParseDetail(loadFixture(t, "layout-change.html"))
	if err == nil {
		t.Fatal("ожидалась ошибка layout change, получено nil")
	}
	if !strings.Contains(err.Error(), "layout change") {
		t.Errorf("ошибка не помечена layout change: %v", err)
	}
}

func TestParseDetailZeroWidthPrice(t *testing.T) {
	// Площадка вставляет zero-width-символы в цену — снимаются.
	page := "<div class=\"inzeratydetnadpis\"><H1 class=nadpisdetail>T</H1></div>\n<div class=popisdetail>Pops</div>\n<table><tr><td>Cena:</td><td colspan=2><br><b><span translate=\"no\"> 13\u200b 900\u200b 000 K\u010d</span></b></td></tr></table>"
	d, err := ParseDetail(page)
	if err != nil {
		t.Fatalf("ParseDetail: %v", err)
	}
	if d.PriceMinor == nil || *d.PriceMinor != 1390000000 {
		t.Errorf("PriceMinor = %v, ожидалось 1390000000 (zero-width сняты)", d.PriceMinor)
	}
}

func TestParseCZNum(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"82,01", 82.01, true},
		{"1.234", 1234, true},
		{"1.234,56", 1234.56, true},
		{"13 900 000", 13900000, true},
		{"4,6", 4.6, true},
		{"", 0, false},
		{"abc", 0, false},
		{"80", 80, true},
	}
	for _, c := range cases {
		v, ok := ParseCZNum(c.in)
		if ok != c.ok || (ok && v != c.want) {
			t.Errorf("ParseCZNum(%q) = %v, %v; ожидалось %v, %v", c.in, v, ok, c.want, c.ok)
		}
	}
}

// TestFetchDelistingSignals — классификация ответов DetailClient.Fetch
// (ТЗ §8.2, паттерны проверены на реальном сайте 07.09: удалённые
// объявления отвечают 301 на страницу раздела /inzeraty/<slug>/):
//   - живая страница (200, без редиректа) — страница без ошибки;
//   - 301 на раздел — ErrListingGone ('unknown');
//   - 301 на другое объявление того же домена — ErrListingRelisted;
//   - 404/410 — ErrListingGone;
//   - 429 — обычная ошибка, НЕ сигнал делистинга (повторить позже).
func TestFetchDelistingSignals(t *testing.T) {
	const detailPage = "<div class=inzeratydetnadpis><H1 class=nadpisdetail>T</H1></div>"

	mux := http.NewServeMux()
	mux.HandleFunc("/inzerat/1/live.php", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, detailPage)
	})
	// Удалённое объявление: 301 на раздел (реальный паттерн bazos).
	mux.HandleFunc("/inzerat/2/gone.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/inzeraty/prodej-bytu/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/inzeraty/prodej-bytu/", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "<html><body>inzerce</body></html>")
	})
	// Перезалист: 301 на другое объявление того же домена.
	mux.HandleFunc("/inzerat/3/old.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/inzerat/99/new.php", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/inzerat/99/new.php", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, detailPage)
	})
	mux.HandleFunc("/inzerat/4/404.php", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/inzerat/5/410.php", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	})
	mux.HandleFunc("/inzerat/6/429.php", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewDetailClient(10 * time.Second)
	ctx := context.Background()

	cases := []struct {
		name string
		path string
		want string // ok | gone | relisted | other
	}{
		{"живая страница — без ошибки", "/inzerat/1/live.php", "ok"},
		{"301 на раздел — gone", "/inzerat/2/gone.php", "gone"},
		{"301 на другое объявление — relisted", "/inzerat/3/old.php", "relisted"},
		{"404 — gone", "/inzerat/4/404.php", "gone"},
		{"410 — gone", "/inzerat/5/410.php", "gone"},
		{"429 — rate limit, не делистинг (D13)", "/inzerat/6/429.php", "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := c.Fetch(ctx, srv.URL+tc.path)
			switch tc.want {
			case "ok":
				if err != nil {
					t.Fatalf("ожидалась страница без ошибки, получено %v", err)
				}
				if !strings.Contains(page, "nadpisdetail") {
					t.Fatalf("страница не возвращена: %q", page)
				}
			case "gone":
				if !errors.Is(err, ErrListingGone) {
					t.Fatalf("ожидался ErrListingGone, получено %v", err)
				}
			case "relisted":
				if !errors.Is(err, ErrListingRelisted) {
					t.Fatalf("ожидался ErrListingRelisted, получено %v", err)
				}
			case "other":
				if err == nil {
					t.Fatal("ожидалась ошибка, получена страница")
				}
				if errors.Is(err, ErrListingGone) || errors.Is(err, ErrListingRelisted) {
					t.Fatalf("429 не должен быть сигналом делистинга: %v", err)
				}
			}
		})
	}
}

// TestFetchRateLimited — 429 (ТЗ §10.4, D13): сигнал с жёстким
// лимитом, а не «повторить позже»: ErrRateLimited + Retry-After
// (секунды / HTTP-дата / отсутствие заголовка).
func TestFetchRateLimited(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/inzerat/10/sec.php", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	mux.HandleFunc("/inzerat/11/noheader.php", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	mux.HandleFunc("/inzerat/12/date.php", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", time.Now().Add(150*time.Second).UTC().Format(time.RFC1123))
		w.WriteHeader(http.StatusTooManyRequests)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewDetailClient(10 * time.Second)
	ctx := context.Background()

	// 429 + Retry-After (секунды).
	_, err := c.Fetch(ctx, srv.URL+"/inzerat/10/sec.php")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("ожидался ErrRateLimited, получено %v", err)
	}
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != 120*time.Second {
		t.Fatalf("RateLimitError: ожидался RetryAfter=120s, получено %v", err)
	}

	// 429 без заголовка — сигнал, RetryAfter нулевой.
	if _, err := c.Fetch(ctx, srv.URL+"/inzerat/11/noheader.php"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("ожидался ErrRateLimited, получено %v", err)
	}

	// 429 + Retry-After (HTTP-дата) — примерно 150 с.
	if _, err := c.Fetch(ctx, srv.URL+"/inzerat/12/date.php"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("ожидался ErrRateLimited, получено %v", err)
	}
	if !errors.As(err, &rl) {
		t.Fatalf("ожидался RateLimitError, получено %v", err)
	}
	if rl.RetryAfter <= 0 || rl.RetryAfter > 160*time.Second {
		t.Fatalf("RetryAfter из HTTP-даты вне разумного диапазона: %v", rl.RetryAfter)
	}
}
