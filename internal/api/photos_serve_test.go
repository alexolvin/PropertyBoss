package api

// Тесты раздачи локальных копий фотографий (TZ v4 §4.4): /photos/<объект>/
// <NN>_<размер>.webp из photos.dir, кэширование, обход путей невозможен.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"propertyboss/internal/config"
)

func testPhotosHandler(t *testing.T, photosDir string) http.Handler {
	t.Helper()
	cfg := &config.Config{}
	cfg.Photos.Dir = photosDir
	s := &Server{Cfg: cfg}
	return s.Handler()
}

func TestPhotosServing(t *testing.T) {
	base := t.TempDir()
	photos := filepath.Join(base, "photos")
	if err := os.MkdirAll(filepath.Join(photos, "946"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(photos, "946", "1_card.webp"),
		[]byte("WEBPCARD"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Файл РЯДОМ с корнем фото — обход через «..» не должен его отдать.
	secret := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(secret, []byte("SECRET-DATA"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := testPhotosHandler(t, photos)
	get := func(p string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		return rec
	}

	// Файл — содержимое + кэш + тип.
	rec := get("/photos/946/1_card.webp")
	if rec.Code != http.StatusOK || rec.Body.String() != "WEBPCARD" {
		t.Fatalf("/photos/946/1_card.webp: code=%d body=%q", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=86400" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/webp" {
		t.Errorf("Content-Type = %q (ждали image/webp)", ct)
	}

	// Несуществующий файл — 404, не фолбэк.
	if rec := get("/photos/946/99_card.webp"); rec.Code != http.StatusNotFound {
		t.Errorf("несуществующее фото: code=%d (ждали 404)", rec.Code)
	}

	// Обход «..» — содержимое файла выше каталога фото не выдаётся
	// ни при каком ответе (mux к тому же редиректит нечистый путь на
	// чистый — туда фото-хендлер уже не доходит).
	for _, p := range []string{"/photos/../secret.txt", "/photos/946/../../secret.txt"} {
		if rec := get(p); rec.Code == http.StatusOK && rec.Body.String() == "SECRET-DATA" {
			t.Errorf("%s: выдано содержимое файла выше каталога фото", p)
		}
	}
	// Чистый путь мимо каталога объекта — 404.
	if rec := get("/photos/secret.txt"); rec.Code != http.StatusNotFound {
		t.Errorf("/photos/secret.txt: code=%d (ждали 404)", rec.Code)
	}
}

func TestPhotosDisabled(t *testing.T) {
	// photos.dir не задан — /photos не существует: 404 из API-мультекса.
	cfg := &config.Config{}
	s := &Server{Cfg: cfg}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/photos/946/1_card.webp", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/photos без photos.dir: code=%d (ждали 404)", rec.Code)
	}
}
