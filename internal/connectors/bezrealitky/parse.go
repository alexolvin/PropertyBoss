package bezrealitky

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"propertyboss/internal/scan"
)

var reNextData = regexp.MustCompile(`(?s)<script[^>]+id="__NEXT_DATA__"[^>]*>(.*?)</script>`)

// ParseListingPage — объявления из __NEXT_DATA__.apolloCache (проба 20260911T041041Z).
func ParseListingPage(page, origin string) ([]scan.Listing, error) {
	m := reNextData.FindStringSubmatch(page)
	if m == nil {
		return nil, fmt.Errorf("bezrealitky: __NEXT_DATA__ не найден")
	}
	var root struct {
		Props struct {
			PageProps struct {
				ApolloCache map[string]json.RawMessage `json:"apolloCache"`
				Filter      struct {
					Page   int `json:"page"`
					Limit  int `json:"limit"`
					Offset int `json:"offset"`
				} `json:"filter"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err := json.Unmarshal([]byte(m[1]), &root); err != nil {
		return nil, fmt.Errorf("bezrealitky: разбор NEXT_DATA: %w", err)
	}
	var out []scan.Listing
	for k, raw := range root.Props.PageProps.ApolloCache {
		if !strings.HasPrefix(k, "Advert:") {
			continue
		}
		ls, err := parseAdvert(raw, origin)
		if err != nil {
			continue
		}
		if ls != nil {
			out = append(out, *ls)
		}
	}
	return out, nil
}

func parseAdvert(raw json.RawMessage, origin string) (*scan.Listing, error) {
	var a map[string]any
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	id, _ := a["id"].(string)
	uri, _ := a["uri"].(string)
	if id == "" || uri == "" {
		return nil, fmt.Errorf("нет id/uri")
	}
	lat, lng := gpsOf(a["gps"])
	ls := scan.Listing{
		ExternalID: id,
		URL:        strings.TrimRight(origin, "/") + "/nemovitosti-byty-domy/" + uri,
	}
	if p, cur, ok := priceOf(a["price"], a["currency"]); ok {
		ls.PriceMinor = &p
		ls.Currency = &cur
	}
	if s := numStr(a["surface"]); s != "" {
		ls.AreaSqM = &s
	}
	pt := mapEstate(str(a["estateType"]))
	if pt != "" {
		ls.PropertyType = &pt
	}
	if lat != nil {
		ls.Lat, ls.Lng = lat, lng
	}
	if addr := strKeyed(a, "address"); addr != "" {
		ls.Address = &addr
	}
	ls.Attributes = map[string]any{}
	if d := str(a["disposition"]); d != "" {
		ls.Attributes["disposition"] = d
	}
	if t := str(a["offerType"]); t != "" {
		ls.Attributes["offer_type"] = t
	}
	if t := str(a["type"]); t != "" {
		ls.Attributes["seller_type_raw"] = t
	}
	lang := "cs"
	ls.LanguageOriginal = &lang
	return &ls, nil
}

// InCZbbox — объявление в грубом bbox ЧР (проба vypis: смесь DE/CZ/SK).
func InCZbbox(lat, lng *float64) bool {
	if lat == nil || lng == nil {
		return false
	}
	return *lat >= 48.5 && *lat <= 51.1 && *lng >= 12.0 && *lng <= 19.0
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func strKeyed(m map[string]any, prefix string) string {
	if s, ok := m[prefix].(string); ok {
		return s
	}
	for k, v := range m {
		if strings.HasPrefix(k, prefix) {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

func numStr(v any) string {
	switch t := v.(type) {
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	case string:
		return t
	}
	return ""
}

func priceOf(price, currency any) (minor int64, cur string, ok bool) {
	cur = strings.ToUpper(str(currency))
	if cur == "" {
		cur = "CZK"
	}
	var major float64
	switch t := price.(type) {
	case float64:
		major = t
	case json.Number:
		major, _ = t.Float64()
	default:
		return 0, "", false
	}
	if major <= 0 {
		return 0, "", false
	}
	return int64(major * 100), cur, true
}

func gpsOf(v any) (*float64, *float64) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, nil
	}
	lat, ok1 := asFloat(m["lat"])
	lng, ok2 := asFloat(m["lng"])
	if !ok1 || !ok2 {
		return nil, nil
	}
	return &lat, &lng
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	}
	return 0, false
}

func mapEstate(s string) string {
	switch strings.ToUpper(s) {
	case "BYT":
		return "flat"
	case "DUM", "DŮM":
		return "house"
	case "POZEMEK":
		return "land"
	default:
		if s == "" {
			return ""
		}
		return strings.ToLower(s)
	}
}

func mapDeal(s string) string {
	switch strings.ToUpper(s) {
	case "PRODEJ":
		return "sale"
	case "PRONAJEM", "PRONÁJEM":
		return "rent"
	}
	return ""
}
