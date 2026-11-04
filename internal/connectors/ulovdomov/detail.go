package ulovdomov

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"propertyboss/internal/probe"
	"propertyboss/internal/ratelimit"
)

// floorsWord — словесная этажность в тексте (třípodlažní, dvoupatrový…).
// Порядок: сначала большие числа, чтобы «desetipodlažní» не съело «deseti».
var floorsWord = []struct {
	re *regexp.Regexp
	n  int
}{
	{regexp.MustCompile(`(?i)deseti(?:podla[žz]n\p{L}*|patrov\p{L}*)`), 10},
	{regexp.MustCompile(`(?i)dev[ěi]ti(?:podla[žz]n\p{L}*|patrov\p{L}*)`), 9},
	{regexp.MustCompile(`(?i)osmi(?:podla[žz]n\p{L}*|patrov\p{L}*)`), 8},
	{regexp.MustCompile(`(?i)sedm[ií]?(?:podla[žz]n\p{L}*|patrov\p{L}*)`), 7},
	{regexp.MustCompile(`(?i)šesti(?:podla[žz]n\p{L}*|patrov\p{L}*)`), 6},
	{regexp.MustCompile(`(?i)pět(?:ipodla[žz]n\p{L}*|patrov\p{L}*)`), 5},
	{regexp.MustCompile(`(?i)čtyř(?:podla[žz]n\p{L}*|patrov\p{L}*)`), 4},
	{regexp.MustCompile(`(?i)t[řr][ií](?:podla[žz]n\p{L}*|patrov\p{L}*)`), 3},
	{regexp.MustCompile(`(?i)dvou(?:podla[žz]n\p{L}*|patrov\p{L}*)`), 2},
	{regexp.MustCompile(`(?i)jedn[oi]?(?:podla[žz]n\p{L}*|patrov\p{L}*)`), 1},
}

var reNextData = regexp.MustCompile(`(?s)<script[^>]+id="__NEXT_DATA__"[^>]*>(.*?)</script>`)

// Detail — разобранная страница объявления (__NEXT_DATA__.offer.data).
type Detail struct {
	Title        string
	Description  string
	Address      string
	Lat, Lng     *float64
	PriceMinor   *int64
	Currency     string
	AreaSqM      *float64
	PlotArea     *float64
	Rooms        *int
	Floors       *int
	Floor        *int
	PropertyType string
	Photos       []string
	SellerType   string // private|agency|developer
	SellerName   string
	Attrs        map[string]any // ключи реестра CZ
	Unmapped     map[string]any
	PriceNote    string
}

type detailOffer struct {
	Title          string `json:"title"`
	Description    string `json:"description"`
	OfferTypeID    string `json:"offerTypeId"`
	PriceNote      string `json:"priceNote"`
	IsNoCommission *bool  `json:"isNoCommission"`
	RentalPrice    *struct {
		Value    float64 `json:"value"`
		Currency string  `json:"currency"`
	} `json:"rentalPrice"`
	GeoCoordinates *struct {
		Lat float64 `json:"lat"`
		Lng float64 `json:"lng"`
	} `json:"geoCoordinates"`
	Street *struct {
		Name string `json:"name"`
	} `json:"street"`
	Village *struct {
		Name string `json:"name"`
	} `json:"village"`
	VillagePart *struct {
		Name string `json:"name"`
	} `json:"villagePart"`
	Photos []struct {
		Path string `json:"path"`
	} `json:"photos"`
	Owner *struct {
		Type      string `json:"type"`
		FirstName string `json:"firstName"`
		Surname   string `json:"surname"`
	} `json:"owner"`
	Parameters map[string]json.RawMessage `json:"parameters"`
}

// ParseDetailHTML — __NEXT_DATA__ детальной страницы (проба 5462328, 2026-09-11).
func ParseDetailHTML(page string) (*Detail, error) {
	m := reNextData.FindStringSubmatch(page)
	if m == nil {
		return nil, fmt.Errorf("ulovdomov: __NEXT_DATA__ не найден")
	}
	var root struct {
		Props struct {
			PageProps struct {
				Offer struct {
					Success bool            `json:"success"`
					Data    json.RawMessage `json:"data"`
				} `json:"offer"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err := json.Unmarshal([]byte(m[1]), &root); err != nil {
		return nil, fmt.Errorf("ulovdomov: NEXT_DATA: %w", err)
	}
	if !root.Props.PageProps.Offer.Success || len(root.Props.PageProps.Offer.Data) == 0 {
		return nil, fmt.Errorf("ulovdomov: offer.success=false")
	}
	var o detailOffer
	if err := json.Unmarshal(root.Props.PageProps.Offer.Data, &o); err != nil {
		return nil, fmt.Errorf("ulovdomov: offer.data: %w", err)
	}
	d := &Detail{
		Title:     strings.TrimSpace(o.Title),
		PriceNote: o.PriceNote,
		Attrs:     map[string]any{},
		Unmapped:  map[string]any{},
	}
	if o.Description != "" {
		d.Description = string(probe.Redact([]byte(o.Description), "text/plain"))
	}
	d.Address = joinUnique(nameOf(o.Street), nameOf(o.VillagePart), nameOf(o.Village))
	if o.GeoCoordinates != nil && o.GeoCoordinates.Lat != 0 {
		lat, lng := o.GeoCoordinates.Lat, o.GeoCoordinates.Lng
		d.Lat, d.Lng = &lat, &lng
	}
	if o.RentalPrice != nil && o.RentalPrice.Value > 0 {
		minor := int64(o.RentalPrice.Value * 100)
		d.PriceMinor = &minor
		d.Currency = strings.ToUpper(o.RentalPrice.Currency)
		if d.Currency == "" {
			d.Currency = "CZK"
		}
	}
	for _, p := range o.Photos {
		if p.Path != "" {
			d.Photos = append(d.Photos, p.Path)
		}
	}
	if o.Owner != nil {
		d.SellerName = strings.TrimSpace(o.Owner.Type)
		d.SellerType = classifySeller(o.Owner.Type)
	}
	if o.IsNoCommission != nil {
		d.Unmapped["commission_included"] = !*o.IsNoCommission
	}
	if o.PriceNote != "" {
		d.Unmapped["price_note"] = o.PriceNote
		low := strings.ToLower(o.PriceNote)
		if strings.Contains(low, "včetně provize") || strings.Contains(low, "vcetne provize") {
			d.Unmapped["commission_in_price"] = true
		}
	}
	applyParams(d, o.Parameters)
	refineFromDescription(d)
	return d, nil
}

// refineFromDescription — уточнение полей по тексту объявления, когда
// структурированный параметр площадки грубее формулировки (вода:
// «vodovod» vs сезонный ряд с опцией постоянного подключения).
func refineFromDescription(d *Detail) {
	if d == nil || d.Description == "" {
		return
	}
	fold := foldCS(d.Description)
	if strings.Contains(fold, "vodovod") {
		seasonal := strings.Contains(fold, "sezonni")
		permOpt := strings.Contains(fold, "trvale") && strings.Contains(fold, "pripojeni")
		switch {
		case seasonal && permOpt:
			d.Unmapped["water_label"] = "sezónní vodovod, možnost trvalého připojení"
		case seasonal:
			d.Unmapped["water_label"] = "sezónní vodovod"
		}
	}
	// Этажность: поле площадки «Počet podlaží» на 5462328 = 2, текст —
	// «třípodlažní» + три названных этажа. Явное слово в описании
	// надёжнее одиночной цифры в форме.
	if n := floorsFromText(d.Description); n > 0 {
		d.Floors = &n
		d.Attrs["floors"] = n
	}
}

func floorsFromText(desc string) int {
	for _, r := range floorsWord {
		if r.re.FindStringIndex(desc) != nil {
			return r.n
		}
	}
	return 0
}

func foldCS(s string) string {
	s = strings.ToLower(s)
	repl := strings.NewReplacer(
		"á", "a", "č", "c", "ď", "d", "é", "e", "ě", "e",
		"í", "i", "ň", "n", "ó", "o", "ř", "r", "š", "s",
		"ť", "t", "ú", "u", "ů", "u", "ý", "y", "ž", "z",
	)
	return repl.Replace(s)
}

func nameOf(n *struct {
	Name string `json:"name"`
}) string {
	if n == nil {
		return ""
	}
	return n.Name
}

func joinUnique(parts ...string) string {
	seen := map[string]bool{}
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return strings.Join(out, ", ")
}

func classifySeller(t string) string {
	s := strings.ToLower(strings.TrimSpace(t))
	if s == "" {
		return ""
	}
	if strings.Contains(s, "private") || s == "osoba" || s == "soukromý" || s == "soukromy" {
		return "private"
	}
	if strings.Contains(s, "develop") {
		return "developer"
	}
	// Century 21, RE/MAX, RK, agency names
	return "agency"
}

func applyParams(d *Detail, params map[string]json.RawMessage) {
	if params == nil {
		return
	}
	get := func(key string) paramVal { return parseParam(params[key]) }
	if p := get("energyEfficiencyRating"); p.id != "" {
		d.Attrs["energy_class"] = strings.ToUpper(p.id)
	} else if p.text != "" {
		d.Attrs["energy_class"] = strings.ToUpper(p.text)
	}
	if p := get("buildingCondition"); p.id != "" {
		d.Attrs["condition"] = mapCondition(p.id)
	}
	if p := get("material"); p.id != "" {
		d.Attrs["building_material"] = mapMaterial(p.id)
	}
	if p := get("ownership"); p.id != "" {
		d.Attrs["ownership_type"] = mapOwnership(p.id)
	}
	if p := get("gas"); p.id != "" || p.text != "" {
		d.Attrs["has_gas"] = isYes(p)
	}
	if p := get("lift"); p.text != "" || p.id != "" {
		d.Attrs["has_elevator"] = isYes(p)
	}
	if p := get("usableArea"); p.text != "" {
		if f := parseM2(p.text); f != nil {
			d.AreaSqM = f
			d.Attrs["area_sqm"] = *f
		}
	}
	if p := get("estateArea"); p.text != "" {
		if f := parseM2(p.text); f != nil {
			d.PlotArea = f
			d.Attrs["plot_area"] = *f
		}
	}
	if p := get("floors"); p.text != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(p.text)); err == nil && n > 0 {
			d.Floors = &n
			d.Attrs["floors"] = n
		}
	}
	if p := get("floorNumber"); p.num != nil {
		n := int(*p.num)
		if n > 0 {
			d.Floor = &n
			d.Attrs["floor"] = n
		}
	}
	if p := get("roomCount"); p.id != "" {
		if n := roomsFromID(p.id); n > 0 {
			d.Rooms = &n
			d.Attrs["rooms"] = n
		}
	}
	if p := get("propertyType"); p.id != "" {
		d.PropertyType = p.id
	}
	if p := get("yearBuilt"); p.num != nil && *p.num > 1800 {
		d.Attrs["year_built"] = int(*p.num)
	}
	if p := get("objectAge"); p.num != nil && *p.num > 1800 {
		d.Attrs["year_built"] = int(*p.num)
	}
	// вне реестра — как есть, человекочитаемые подписи
	for _, key := range []string{
		"heating", "water", "gully", "telecommunication", "objectType", "houseType",
		"buildingType", "objectLocation", "furnished", "priceNegotiation",
		"lowEnergy", "roadType", "surroundingType", "protection",
	} {
		p := get(key)
		if p.id == "" && p.text == "" && len(p.ids) == 0 {
			continue
		}
		if len(p.ids) > 0 {
			d.Unmapped[key] = p.ids
			if len(p.texts) > 0 {
				d.Unmapped[key+"_label"] = strings.Join(p.texts, ", ")
			}
			continue
		}
		if p.text != "" {
			d.Unmapped[key] = p.text
		} else {
			d.Unmapped[key] = p.id
		}
	}
}

type paramVal struct {
	id    string
	text  string
	ids   []string
	texts []string
	num   *float64
}

func parseParam(raw json.RawMessage) paramVal {
	var p paramVal
	if len(raw) == 0 || string(raw) == "null" {
		return p
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return p
	}
	if v, ok := m["value"]; ok {
		switch t := v.(type) {
		case string:
			p.text = t
		case float64:
			p.num = &t
			p.text = strconv.FormatFloat(t, 'f', -1, 64)
		case bool:
			if t {
				p.text = "Ano"
			} else {
				p.text = "Ne"
			}
		}
	}
	if opts, ok := m["options"].([]any); ok {
		for _, o := range opts {
			om, _ := o.(map[string]any)
			if om == nil {
				continue
			}
			id, _ := om["id"].(string)
			title, _ := om["title"].(string)
			if id != "" {
				p.ids = append(p.ids, id)
			}
			if title != "" {
				p.texts = append(p.texts, title)
			}
		}
		if len(p.ids) == 1 {
			p.id = p.ids[0]
		}
		if len(p.texts) == 1 && p.text == "" {
			p.text = p.texts[0]
		}
	}
	return p
}

func isYes(p paramVal) bool {
	s := strings.ToLower(p.text + " " + p.id)
	return strings.Contains(s, "ano") || s == "yes" || s == "true"
}

func parseM2(s string) *float64 {
	s = strings.ToLower(strings.ReplaceAll(s, ",", "."))
	var n strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' || r == '.' {
			n.WriteRune(r)
		} else if n.Len() > 0 {
			break
		}
	}
	f, err := strconv.ParseFloat(n.String(), 64)
	if err != nil || f <= 0 {
		return nil
	}
	return &f
}

func mapCondition(id string) string {
	switch id {
	case "veryGood":
		return "very_good"
	case "good":
		return "good"
	case "average":
		return "average"
	case "poor":
		return "poor"
	case "renovated":
		return "renovated"
	default:
		return id
	}
}

func mapMaterial(id string) string {
	switch id {
	case "brick":
		return "cihlova"
	case "panel":
		return "panelova"
	case "wood", "wooden":
		return "drevena"
	case "stone":
		return "kamenna"
	default:
		return "jina"
	}
}

func mapOwnership(id string) string {
	switch id {
	case "personal", "private":
		return "osobni"
	case "cooperative":
		return "druzstevni"
	default:
		return id
	}
}

func roomsFromID(id string) int {
	switch id {
	case "oneRoom":
		return 1
	case "twoRooms":
		return 2
	case "threeRooms":
		return 3
	case "fourRooms":
		return 4
	case "fiveRooms":
		return 5
	}
	return 0
}

// FetchDetail — GET HTML объявления, WaitSource, разбор NEXT_DATA.
func FetchDetail(ctx context.Context, rawURL string) (*Detail, []byte, error) {
	if err := ratelimit.WaitSource(ctx); err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Referer", "https://www.ulovdomov.cz/")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, body, fmt.Errorf("ulovdomov: HTTP 429")
	}
	if resp.StatusCode/100 != 2 {
		return nil, body, fmt.Errorf("ulovdomov: HTTP %d", resp.StatusCode)
	}
	d, err := ParseDetailHTML(string(body))
	return d, body, err
}
