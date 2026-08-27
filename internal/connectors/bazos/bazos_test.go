package bazos

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"propertyboss/internal/scan"
)

// samplePage — структура страницы категории reality.bazos.cz (проверено
// 2026-08-27 на реальном HTML /prodam/byt/): счётчик «z N» и две
// карточки — с ценой и с «V textu».
const samplePage = `
<div class="inzeraty inzeratyflex">
<div class="inzeratynadpis"><a href="/inzerat/222871645/prodej-bytu-3kk-barrandov.php"><img src="https://www.bazos.cz/img/1t/645/222871645.jpg" class="obrazek" alt="Prodej bytu 3kk Barrandov"></a>
<h2 class=nadpis><a href="/inzerat/222871645/prodej-bytu-3kk-barrandov.php">Prodej bytu 3kk Barrandov</a></h2><span class=velikost10> - <span title="TOP 21x" class="ztop">TOP</span> - [27.8. 2026]</span><br>
<div class=popis>BYT 3+kk 80 m2 S VELKORYSOU TERASOU 21 m², SKLEPEM A PARKOVACÍM STÁNÍM

Nabízíme k prodeji příjemný byt 3+kk o podlahové ploše 80 m² ...</div><br><br>
</div>
<div class="inzeratycena"><b><span translate="no"> 14 500 000 Kč</span></b></div>
<div class="inzeratylok">Praha 5<br>152 00</div>
<div class="inzeratyview">2229 x</div>
</div>

<div class="inzeraty inzeratyflex">
<div class="inzeratynadpis"><a href="/inzerat/222453921/byt-2kk-balkon-top.php"><img src="https://www.bazos.cz/img/1t/921/222453921.jpg" class="obrazek" alt="Byt 2kk balkon"></a>
<h2 class=nadpis><a href="/inzerat/222453921/byt-2kk-balkon-top.php">Byt 2kk balkon TOP</a></h2><span class=velikost10> - [5.8. 2026]</span><br>
</div>
<div class="inzeratycena"><b><span translate="no">V textu</span></b></div>
<div class="inzeratylok">Svitavy<br>570 01</div>
<div class="inzeratyview">17 x</div>
</div>
` +
	`<div>Zobrazeno 1-20 inzerátů z 2</div>`

func TestParseTotal(t *testing.T) {
	if n, ok := parseTotal(samplePage); !ok || n != 2 {
		t.Fatalf("parseTotal(samplePage) = %d, %v; ждали 2, true", n, ok)
	}
	// Реальный формат со счётчиком, разделённым пробелом.
	if n, ok := parseTotal(`<div>Zobrazeno 1-20 inzerátů z 7 678</div>`); !ok || n != 7678 {
		t.Fatalf("parseTotal(7 678) = %d, %v; ждали 7678, true", n, ok)
	}
	if _, ok := parseTotal("<p>никакого счётчика</p>"); ok {
		t.Fatal("parseTotal без счётчика должен вернуть false")
	}
}

func TestParseCards(t *testing.T) {
	listings, problems := parseCards(samplePage)
	if len(problems) != 0 {
		t.Fatalf("problems = %v, ждали ни одной", problems)
	}
	if len(listings) != 2 {
		t.Fatalf("карточек %d, ждали 2", len(listings))
	}

	a := listings[0]
	if a.ExternalID != "222871645" {
		t.Errorf("ExternalID = %q, ждали 222871645", a.ExternalID)
	}
	if a.URL != "https://reality.bazos.cz/inzerat/222871645/prodej-bytu-3kk-barrandov.php" {
		t.Errorf("URL = %q", a.URL)
	}
	if a.PriceMinor == nil || *a.PriceMinor != 1450000000 { // 14 500 000 Kč × 100 (CZK exponent 2)
		t.Errorf("PriceMinor = %v, ждали 1450000000", a.PriceMinor)
	}
	if a.Currency == nil || *a.Currency != "CZK" {
		t.Errorf("Currency = %v, ждали CZK", a.Currency)
	}
	wantDate := time.Date(2026, 8, 27, 0, 0, 0, 0, time.Local)
	if a.PostedAt == nil || !a.PostedAt.Equal(wantDate) {
		t.Errorf("PostedAt = %v, ждали %v", a.PostedAt, wantDate)
	}
	if a.Address == nil || *a.Address != "Praha 5, 152 00" {
		t.Errorf("Address = %v, ждали \"Praha 5, 152 00\"", a.Address)
	}
	if a.Description == nil || !strings.Contains(*a.Description, "BYT 3+kk 80 m2") {
		t.Errorf("Description = %v, ждали выдержку с \"BYT 3+kk 80 m2\"", a.Description)
	}
	// Карточка не несёт координат/площади/комнат (ТЗ §0.4 — nil).
	if a.Lat != nil || a.Lng != nil || a.AreaSqM != nil || a.Rooms != nil {
		t.Error("координаты/площадь/комнаты в карточке не публикуются — ждали nil")
	}

	b := listings[1]
	if b.ExternalID != "222453921" {
		t.Errorf("b.ExternalID = %q, ждали 222453921", b.ExternalID)
	}
	if b.PriceMinor != nil || b.Currency != nil {
		t.Errorf("«V textu»: PriceMinor/Currency = %v/%v, ждали nil", b.PriceMinor, b.Currency)
	}
	if b.Address == nil || *b.Address != "Svitavy, 570 01" {
		t.Errorf("b.Address = %v, ждали \"Svitavy, 570 01\"", b.Address)
	}
	if b.PostedAt == nil || !b.PostedAt.Equal(time.Date(2026, 8, 5, 0, 0, 0, 0, time.Local)) {
		t.Errorf("b.PostedAt = %v (однозначный день)", b.PostedAt)
	}
	if b.Description != nil {
		t.Errorf("у второй карточки нет div=popis — ждали nil, получили %q", *b.Description)
	}
}

func TestAnnotate(t *testing.T) {
	listings, _ := parseCards(samplePage)
	pt := "flat"
	cfg := scan.SearchConfig{PropertyType: &pt}
	annotate(listings, cfg)
	for i, l := range listings {
		if l.PropertyType == nil || *l.PropertyType != "flat" {
			t.Errorf("listings[%d].PropertyType = %v, ждали flat", i, l.PropertyType)
		}
		if l.LanguageOriginal == nil || *l.LanguageOriginal != "cs" {
			t.Errorf("listings[%d].LanguageOriginal = %v, ждали cs", i, l.LanguageOriginal)
		}
	}
}

func TestParseCardPriceUnknownFormat(t *testing.T) {
	// Цена в неизвестном формате — не молчим: проблема на уровне скана
	// (layout_change, ТЗ §8.2.2).
	seg := `<div class="inzeratynadpis"><a href="/inzerat/1/sekundarni.php">x</a>
<div class="inzeratycena"><b><span onclick="x()" class="paction">Cena</span></b></div>`
	l, prob := parseCard(seg)
	if l.ExternalID != "1" {
		t.Fatalf("ExternalID = %q, ждали 1", l.ExternalID)
	}
	if prob == "" {
		t.Fatal("не распознанный формат цены должен дать problem")
	}
	if l.PriceMinor != nil {
		t.Errorf("PriceMinor = %v, ждали nil", l.PriceMinor)
	}
}

func TestParseCardNoPriceTokens(t *testing.T) {
	// Состояния «числовой цены нет» на источнике (полный сбор по
	// /prodam/byt/, 384 страницы, 2026-08-27) — PriceMinor = nil
	// (честный NULL, ТЗ §0.4), не проблема.
	for _, tok := range []string{"V textu", "Dohodou", "Nabídněte", "Zdarma", "Nerozhoduje"} {
		seg := fmt.Sprintf(`<div class="inzeratynadpis"><a href="/inzerat/1/x.php">x</a>
<div class="inzeratycena"><b><span translate="no">%s</span></b></div>`, tok)
		l, prob := parseCard(seg)
		if prob != "" {
			t.Errorf("%s: prob = %q, ждали без проблемы", tok, prob)
		}
		if l.PriceMinor != nil || l.Currency != nil {
			t.Errorf("%s: PriceMinor/Currency = %v/%v, ждали nil", tok, l.PriceMinor, l.Currency)
		}
	}
}

func TestParseCardMissingPriceDiv(t *testing.T) {
	// Div цены отсутствует — смена вёрстки, не молчим (ТЗ §8.2.2).
	seg := `<div class="inzeratynadpis"><a href="/inzerat/1/x.php">x</a>
<div class="inzeratylok">Praha 1<br>110 00</div>`
	l, prob := parseCard(seg)
	if l.ExternalID != "1" {
		t.Fatalf("ExternalID = %q, ждали 1", l.ExternalID)
	}
	if prob == "" {
		t.Fatal("отсутствующий div цены должен дать problem")
	}
	if l.PriceMinor != nil {
		t.Errorf("PriceMinor = %v, ждали nil", l.PriceMinor)
	}
}

func TestScanValidation(t *testing.T) {
	c := &Connector{} // сеть не затрагивается: валидация до запросов
	ctx := context.Background()
	pt := "flat"

	cases := map[string]scan.SearchConfig{
		"фильтр цены":  {ID: 1, DealType: "sale", PropertyType: &pt, MinPriceMinor: func() *int64 { v := int64(1); return &v }()},
		"фильтр площади": {ID: 1, DealType: "sale", PropertyType: &pt, MinAreaSqM: func() *string { s := "50"; return &s }()},
		"нет property_type": {ID: 1, DealType: "sale"},
		"неизвестный deal_type":  {ID: 1, DealType: "buy", PropertyType: &pt},
		"неизвестный property_type": {ID: 1, DealType: "sale", PropertyType: func() *string { s := "chateau"; return &s }()},
	}
	for name, want := range cases {
		if _, _, err := c.Scan(ctx, want); err == nil {
			t.Errorf("%s: ждали явную ошибку, получили nil", name)
		}
	}
}
