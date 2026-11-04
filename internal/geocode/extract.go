package geocode

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Извлечение улицы из текста объявления. Площадка (bazos) в карточке
// отдаёт город+PSČ; улица — только в описании (C10). Шаблоны
// консервативные: ложное срабатывание = выдуманный адрес (ТЗ §0.4).
//
// Порядок: сначала «улица + номер дома» (шанс building), затем явные
// маркеры ulici/ulice/ul.
var (
	// Имена улиц: заглавная буква обязательна (не (?i) на имя — иначе
	// «v ulici Rokycanova na …» захватывает «na»).
	reStreetSlash = regexp.MustCompile(`(?:^|[^\p{L}])([A-ZÁ-Ž][\p{L}\-]{2,24})\s+(\d{1,4}/\d{1,4})\b`)
	reVUlici      = regexp.MustCompile(`(?i)(?:v|na)\s+ulici\s+(?-i)([A-ZÁ-Ž][\p{L}\-]{0,30}(?:\s+[A-ZÁ-Ž][\p{L}\-]{1,20})?)(?:\s+(\d{1,4}(?:/\d{1,4})?))?`)
	reUliceWord   = regexp.MustCompile(`(?i)\bulice\s+(?-i)([A-ZÁ-Ž][\p{L}\-]{0,30}(?:\s+[A-ZÁ-Ž][\p{L}\-]{1,20})?)(?:\s+(\d{1,4}(?:/\d{1,4})?))?`)
	reUlAbbr      = regexp.MustCompile(`(?i)\bul\.?\s+(?-i)([A-ZÁ-Ž][\p{L}\-]{0,30}(?:\s+[A-ZÁ-Ž][\p{L}\d\-]{1,20})?)(?:\s+(\d{1,4}(?:/\d{1,4})?))?`)
	rePostcode    = regexp.MustCompile(`\b(\d{3})\s?(\d{2})\b`)
)

// streetStop — чешские предлоги; одно слово без второго — не улица.
var streetStop = map[string]bool{
	"pod": true, "nad": true, "u": true, "na": true, "za": true,
	"při": true, "pri": true, "ke": true, "do": true, "od": true, "k": true,
}

// ExtractStreet — улица (и опционально номер) из описания/заголовка.
// city — чтобы «Praha» из «Praha 5» не принималась за улицу.
func ExtractStreet(text, city string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	city = strings.TrimSpace(city)
	take := func(name, num string) string {
		name = strings.TrimSpace(name)
		num = strings.TrimSpace(num)
		if name == "" {
			return ""
		}
		full := name
		if num != "" {
			full = name + " " + num
		}
		if strings.EqualFold(name, city) || strings.EqualFold(full, city) {
			return ""
		}
		if i := strings.IndexAny(city, " \t"); i > 0 && strings.EqualFold(name, city[:i]) {
			return ""
		}
		// Предлог без второго слова — не улица («Ulice Pod …» без «Tlustou»).
		if num == "" && !strings.Contains(name, " ") {
			if streetStop[strings.ToLower(name)] {
				return ""
			}
			if utf8.RuneCountInString(name) < 3 {
				return ""
			}
		}
		return full
	}
	// Явный маркер ulici/ul. — раньше slash: «ul. Na Robinsonce 1643/5»
	// иначе slash берёт «Robinsonce 1643/5».
	if m := reVUlici.FindStringSubmatch(text); m != nil {
		num := ""
		if len(m) > 2 {
			num = m[2]
		}
		if s := take(m[1], num); s != "" {
			return s
		}
	}
	if m := reUliceWord.FindStringSubmatch(text); m != nil {
		num := ""
		if len(m) > 2 {
			num = m[2]
		}
		if s := take(m[1], num); s != "" {
			return s
		}
	}
	if m := reUlAbbr.FindStringSubmatch(text); m != nil {
		num := ""
		if len(m) > 2 {
			num = m[2]
		}
		if s := take(m[1], num); s != "" {
			return s
		}
	}
	if m := reStreetSlash.FindStringSubmatch(text); m != nil {
		if s := take(m[1], m[2]); s != "" {
			return s
		}
	}
	return ""
}

// ParseCityPostcode — город и PSČ из адреса карточки («Praha 5, 150 00»).
// Не улица: в карточке bazos её нет.
func ParseCityPostcode(addr string) (city, postcode string) {
	addr = normSpace(addr)
	if addr == "" {
		return "", ""
	}
	if m := rePostcode.FindStringSubmatch(addr); m != nil {
		postcode = m[1] + " " + m[2]
	}
	// Город — первый сегмент до запятой, если это не сам индекс.
	seg := addr
	if i := strings.Index(addr, ","); i >= 0 {
		seg = strings.TrimSpace(addr[:i])
	}
	if postcode != "" && strings.ReplaceAll(seg, " ", "") == strings.ReplaceAll(postcode, " ", "") {
		return "", postcode
	}
	return seg, postcode
}
