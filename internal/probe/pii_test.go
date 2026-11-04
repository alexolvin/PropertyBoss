package probe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactPhonesAndEmailInHTML(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "pii_listing.html"))
	if err != nil {
		t.Fatal(err)
	}
	out := Redact(raw, "text/html; charset=utf-8")
	s := string(out)
	if reEmail.FindString(s) != "" {
		t.Errorf("e-mail остался: %s", reEmail.FindString(s))
	}
	if strings.Contains(s, "+420") || strings.Contains(s, "777 123 456") {
		t.Error("телефон остался")
	}
	if !strings.Contains(s, redacted) {
		t.Error("ожидался [REDACTED]")
	}
	if !strings.Contains(s, "Prodej bytu 3+1") {
		t.Error("неконтактный текст не должен пропасть")
	}
	if reEmail.FindString(s) != "" || rePhoneIntl.FindString(s) != "" || rePhoneFmt.FindString(s) != "" || rePhoneBare.FindString(s) != "" {
		t.Errorf("после редактирования остались ПДн")
	}
}

func TestRedactJSONContactKeys(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "pii_api.json"))
	if err != nil {
		t.Fatal(err)
	}
	out := Redact(raw, "application/json")
	s := string(out)
	if reEmail.FindString(s) != "" {
		t.Errorf("e-mail в JSON: %s", reEmail.FindString(s))
	}
	if strings.Contains(s, "608111222") {
		t.Error("телефон в JSON")
	}
	if strings.Contains(s, "jan.novak") {
		t.Error("seller.email значение")
	}
}
