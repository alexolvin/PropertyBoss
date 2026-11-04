// Редактор ПДн (ТЗ-SRC-CZ-1 A9): телефоны, e-mail и ключи контакта
// удаляются из тел ответов до записи артефакта.
package probe

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	reEmail = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)
	// Не трогаем голые 9 цифр цены (6 500 000). Только +420, 3-3-3 с
	// разделителями, или 9 цифр сразу после «tel/telefon/phone».
	rePhoneIntl = regexp.MustCompile(`\+420[\s\-]?(?:\d[\s\-]?){8}\d`)
	rePhoneFmt  = regexp.MustCompile(`\b[67]\d{2}[\s\-]\d{3}[\s\-]\d{3}\b`)
	rePhoneBare = regexp.MustCompile(`(?i)(?:tel\.?|telefon|phone|mobil)[\s.:]*[67]\d{8}`)
)

var contactKey = regexp.MustCompile(`(?i)^(phone|tel|telefon|mobile|mobil|email|e-mail|mail|contact|kontakt|seller|inzerent|makler|makléř|broker|agent|jmeno|jméno|name|advertiser)$`)

const redacted = "[REDACTED]"

// Redact — правка тела: regex по тексту + удаление контактных ключей JSON.
func Redact(body []byte, contentType string) []byte {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "json") || looksJSON(body) {
		if out, ok := redactJSON(body); ok {
			return out
		}
	}
	return redactText(body)
}

func looksJSON(b []byte) bool {
	s := strings.TrimSpace(string(b))
	return strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[")
}

func redactText(b []byte) []byte {
	s := string(b)
	s = reEmail.ReplaceAllString(s, redacted)
	s = rePhoneIntl.ReplaceAllString(s, redacted)
	s = rePhoneFmt.ReplaceAllString(s, redacted)
	s = rePhoneBare.ReplaceAllString(s, redacted)
	return []byte(s)
}

func redactJSON(b []byte) ([]byte, bool) {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, false
	}
	walkJSON(v)
	out, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	return redactText(out), true
}

func walkJSON(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if contactKey.MatchString(k) {
				t[k] = redacted
				continue
			}
			walkJSON(val)
		}
	case []any:
		for _, item := range t {
			walkJSON(item)
		}
	}
}
