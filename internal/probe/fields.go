package probe

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	rePrice    = regexp.MustCompile(`(?i)([\d][\d\s\x{00A0}\x{202F}.,]{2,})\s*(Kč|CZK|€|EUR)`)
	reNoPrice  = regexp.MustCompile(`(?i)(cena na dotaz|info v rk|dohodou|v textu|nabídněte)`)
	reArea     = regexp.MustCompile(`(?i)([\d][\d\s\x{00A0}]*)\s*(m²|m2|m&sup2;|m<sup>2</sup>)`)
	reDisp     = regexp.MustCompile(`(?i)\b(\d\s*\+\s*(?:kk|\d))\b`)
	reObec     = regexp.MustCompile(`(?i)\b(praha|brno|ostrava|plzeň|liberec|olomouc|české budějovice|hradec králové|ústí nad labem|pardubice|zlín|havířov|kladno|most|opava|frýdek|karviná|jihlava|teplice)\b`)
	rePSC      = regexp.MustCompile(`\b(\d{3}\s?\d{2})\b`)
	reGPSJSON  = regexp.MustCompile(`(?i)"(?:lat|latitude)"\s*:\s*(-?\d+\.\d+)\s*,\s*"(?:lng|lon|longitude)"\s*:\s*(-?\d+\.\d+)`)
	reExtID    = regexp.MustCompile(`/(\d{4,})(?:/|$|\.|-)`)
	reSale     = regexp.MustCompile(`(?i)(prodej|for sale|sale)`)
	reRent     = regexp.MustCompile(`(?i)(pronájem|pronajem|for rent|rent)`)
	reFlat     = regexp.MustCompile(`(?i)(byt|apartment|flat)`)
	reHouse    = regexp.MustCompile(`(?i)(dům|dum|house|rodinný)`)
	reLand     = regexp.MustCompile(`(?i)(pozemek|land|parcela)`)
	reSellerRK = regexp.MustCompile(`(?i)(realitní kancelář|\brk\b|agency|makléř)`)
	reSellerP  = regexp.MustCompile(`(?i)(soukrom|privát|private|owner|majitel)`)
	reDate     = regexp.MustCompile(`\b(\d{1,2}\.\d{1,2}\.\d{4})\b`)
)

// Sample — одно объявление выборки P7 (после редактирования ПДн).
type Sample struct {
	URL          string `json:"url"`
	ExternalID   string `json:"external_id,omitempty"`
	HasPrice     bool   `json:"has_price"`
	PriceFlag    bool   `json:"price_on_request"`
	DealType     string `json:"deal_type,omitempty"`
	PropertyType string `json:"property_type,omitempty"`
	HasArea      bool   `json:"has_area"`
	HasDisp      bool   `json:"has_disposition"`
	HasLocation  bool   `json:"has_location"`
	HasGPS       bool   `json:"has_gps"`
	HasSeller    bool   `json:"has_seller_type"`
	HasDate      bool   `json:"has_date"`
}

func parseSample(pageURL string, body []byte) Sample {
	s := string(body)
	low := strings.ToLower(s)
	sm := Sample{URL: pageURL}
	if m := reExtID.FindStringSubmatch(pageURL); m != nil {
		sm.ExternalID = m[1]
	}
	if reNoPrice.MatchString(s) {
		sm.PriceFlag = true
	}
	if rePrice.MatchString(s) {
		sm.HasPrice = true
	}
	switch {
	case reRent.MatchString(low) && !reSale.MatchString(low):
		sm.DealType = "rent"
	case reSale.MatchString(low):
		sm.DealType = "sale"
	}
	switch {
	case reFlat.MatchString(low):
		sm.PropertyType = "flat"
	case reHouse.MatchString(low):
		sm.PropertyType = "house"
	case reLand.MatchString(low):
		sm.PropertyType = "land"
	}
	if reArea.MatchString(s) {
		sm.HasArea = true
	}
	if reDisp.MatchString(s) {
		sm.HasDisp = true
	}
	if reObec.MatchString(low) || rePSC.MatchString(s) {
		sm.HasLocation = true
	}
	if reGPSJSON.MatchString(s) {
		sm.HasGPS = true
	}
	if reSellerRK.MatchString(s) || reSellerP.MatchString(s) {
		sm.HasSeller = true
	}
	if reDate.MatchString(s) {
		sm.HasDate = true
	}
	return sm
}

type FieldCoverage struct {
	N            int `json:"n"`
	ID           int `json:"id"`
	URL          int `json:"url"`
	PriceOrFlag  int `json:"price_or_flag"`
	DealType     int `json:"deal_type"`
	PropertyType int `json:"property_type"`
	Area         int `json:"area"`
	Disposition  int `json:"disposition"`
	Location     int `json:"location"`
	GPS          int `json:"gps"`
	SellerType   int `json:"seller_type"`
	Dates        int `json:"dates"`
}

func cover(samples []Sample) FieldCoverage {
	c := FieldCoverage{N: len(samples)}
	for _, s := range samples {
		if s.ExternalID != "" {
			c.ID++
		}
		if s.URL != "" {
			c.URL++
		}
		if s.HasPrice || s.PriceFlag {
			c.PriceOrFlag++
		}
		if s.DealType != "" {
			c.DealType++
		}
		if s.PropertyType != "" {
			c.PropertyType++
		}
		if s.HasArea {
			c.Area++
		}
		if s.HasDisp {
			c.Disposition++
		}
		if s.HasLocation {
			c.Location++
		}
		if s.HasGPS {
			c.GPS++
		}
		if s.HasSeller {
			c.SellerType++
		}
		if s.HasDate {
			c.Dates++
		}
	}
	return c
}

func parseTotalHint(html string) *int {
	text := stripTags(html)
	re := regexp.MustCompile(`(?i)(?:inzerát[ůuoe]{0,3}|nabídek|nabidek|výsledk[ůu]|vysledku)\s*(?:z|celkem|:)?\s*([\d\s\x{00A0}]{1,9})`)
	m := re.FindStringSubmatch(text)
	if m == nil {
		re = regexp.MustCompile(`(?i)([\d\s\x{00A0}]{1,7})\s*(?:inzerát|nabídek|výsledk)`)
		m = re.FindStringSubmatch(text)
	}
	if m == nil {
		return nil
	}
	d := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, m[1])
	n, err := strconv.Atoi(d)
	if err != nil || n <= 0 || n > 5_000_000 {
		return nil
	}
	return &n
}

func tosExcerpt(body string) string {
	text := stripTags(string(body))
	low := strings.ToLower(text)
	keys := []string{
		"automatiz", "robot", "crawler", "scrap", "spider",
		"těžba dat", "tezba dat", "data mining", "bez souhlasu",
		"neoprávněn", "neopravnen",
	}
	idx := -1
	for _, k := range keys {
		if i := strings.Index(low, k); i >= 0 && (idx < 0 || i < idx) {
			idx = i
		}
	}
	if idx < 0 {
		return ""
	}
	start := idx - 120
	if start < 0 {
		start = 0
	}
	end := idx + 200
	if end > len(text) {
		end = len(text)
	}
	// idx в low ≈ text, оба из stripTags ASCII/UTF-8; режем по байтам осторожно.
	runes := []rune(text)
	// пересчитаем по рунам
	lowR := []rune(strings.ToLower(text))
	idx = -1
	for _, k := range keys {
		kr := []rune(k)
		for i := 0; i+len(kr) <= len(lowR); i++ {
			if string(lowR[i:i+len(kr)]) == k {
				idx = i
				break
			}
		}
		if idx >= 0 {
			break
		}
	}
	if idx < 0 {
		return ""
	}
	start = idx - 80
	if start < 0 {
		start = 0
	}
	end = idx + 160
	if end > len(runes) {
		end = len(runes)
	}
	return strings.TrimSpace(string(runes[start:end]))
}
