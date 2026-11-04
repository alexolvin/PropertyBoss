package probe

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestDetectNormalBazosListing(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "normal_bazos.html"))
	if err != nil {
		t.Fatal(err)
	}
	if k := Detect(200, http.Header{"Content-Type": []string{"text/html"}}, b); k != KindNone {
		t.Errorf("нормальная страница bazos: %q, ожидалось none (ложное срабатывание)", k)
	}
}

func TestDetectRealChallengesNotMeasured(t *testing.T) {
	if _, err := os.Stat(filepath.Join("testdata", "challenge_real.html")); err != nil {
		t.Skip("НЕ ИЗМЕРЕНО: реальных тел челленджа нет (ТЗ-SRC-CZ-1 G0); тест добавится в этапе 1 при первом обнаружении")
	}
}

func TestDetectCloudflareHeaderNotEnough(t *testing.T) {
	h := http.Header{"Server": []string{"cloudflare"}, "Content-Type": []string{"text/html"}}
	body := []byte("<html><body>Prodej bytu 3+1, 5 000 000 Kč, 78 m²</body></html>")
	if k := Detect(200, h, body); k != KindNone {
		t.Errorf("только server: cloudflare без челленджа: %q", k)
	}
}
