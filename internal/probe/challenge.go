package probe

import (
	"net/http"
	"strings"
)

// Kind — класс защиты из P4.
type Kind string

const (
	KindNone     Kind = ""
	KindCaptcha  Kind = "excluded_captcha"
	KindChallenge Kind = "excluded_challenge"
	KindLogin    Kind = "excluded_login"
)

// Detect — P4: челлендж/CAPTCHA/consent wall вместо данных.
// CDN без челленджа (только server: cloudflare) защитой не считается.
func Detect(status int, hdr http.Header, body []byte) Kind {
	s := strings.ToLower(string(body))
	ct := strings.ToLower(hdr.Get("Content-Type"))

	// CAPTCHA-виджет на странице с данными объявления — не стена (P4:
	// защита вместо данных). Стена — капча без листинга.
	if looksCaptcha(s) && !looksListing(s) {
		return KindCaptcha
	}
	if looksChallenge(status, hdr, s) {
		return KindChallenge
	}
	if looksLogin(s, ct) && !looksListing(s) {
		return KindLogin
	}
	if looksConsentWall(s) && !looksListing(s) {
		return KindChallenge
	}
	return KindNone
}

func looksCaptcha(s string) bool {
	for _, n := range []string{
		"g-recaptcha", "h-captcha", "hcaptcha", "recaptcha/api",
		"cf-turnstile", "data-sitekey", "captcha-widget",
	} {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func looksChallenge(status int, hdr http.Header, s string) bool {
	if hdr.Get("cf-mitigated") == "challenge" {
		return true
	}
	if strings.Contains(strings.ToLower(hdr.Get("Server")), "ddos-guard") && status == 403 {
		return true
	}
	for _, n := range []string{
		"just a moment", "checking your browser", "attention required",
		"cf-browser-verification", "cf-challenge-running",
		"datadome", "_datadome", "enable javascript and cookies",
	} {
		if strings.Contains(s, n) {
			return true
		}
	}
	if status == 403 && strings.Contains(s, "access denied") {
		return true
	}
	return false
}

func looksLogin(s, _ string) bool {
	return strings.Contains(s, "type=\"password\"") &&
		(strings.Contains(s, "login") || strings.Contains(s, "přihlá") || strings.Contains(s, "prihlas"))
}

func looksConsentWall(s string) bool {
	return (strings.Contains(s, "cookiebot") || strings.Contains(s, "onetrust") ||
		strings.Contains(s, "id=\"didomi\"") || strings.Contains(s, "consent-overlay")) &&
		!looksListing(s)
}

func looksListing(s string) bool {
	n := 0
	for _, w := range []string{"inzerát", "inzerat", "nemovit", "prodej", "pronájem", "pronajem", "m²", "m2", "kč", "kc"} {
		if strings.Contains(s, w) {
			n++
		}
	}
	return n >= 2
}
