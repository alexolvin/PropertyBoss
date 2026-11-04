package geocode

import (
	"strings"
	"unicode"
)

// Query — нормализованный адрес для геокодера (ТЗ v4 §4.5: адрес
// приводится к нормальному виду, затем определяются координаты).
// Пустые поля не попадают в запрос и в ключ кэша.
type Query struct {
	Street      string
	City        string
	Postcode    string
	Country     string // «Czechia» — в свободный q=
	CountryCode string // CZ — countrycodes= и язык
}

func (q Query) hasStreet() bool { return strings.TrimSpace(q.Street) != "" }
func (q Query) hasPlace() bool {
	return strings.TrimSpace(q.Street) != "" || strings.TrimSpace(q.City) != ""
}

func (q Query) parts() []string {
	out := make([]string, 0, 4)
	for _, p := range []string{q.Street, q.City, q.Postcode, q.Country} {
		if p = normSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// CacheKey — канонический ключ кэша Nominatim (политика провайдера:
// повтор того же адреса не должен идти в сеть).
func (q Query) CacheKey() string {
	return strings.ToLower(strings.Join(q.parts(), "|"))
}

func normSpace(s string) string {
	return strings.TrimSpace(strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " "))
}
