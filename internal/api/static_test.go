package api

// Тесты статической раздачи дашборда (dashboard.ui): один порт на API
// и UI, SPA-фолбэк, /api не перехватывается, листингов каталогов нет.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"propertyboss/internal/config"
)

// testUI — временный «собранный» фронтенд: index.html + assets/app.js.
func testUI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"),
		[]byte("<html>PB-UI</html>"), 0o644); err != nil {
		t.Fatalf("index.html: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatalf("assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"),
		[]byte("console.log(1)"), 0o644); err != nil {
		t.Fatalf("app.js: %v", err)
	}
	return dir
}

func testUIHandler(t *testing.T, dir string) http.Handler {
	t.Helper()
	cfg := &config.Config{}
	cfg.Dashboard.Listen = "127.0.0.1:0"
	cfg.Dashboard.UI = dir
	s := &Server{Cfg: cfg}
	return s.Handler()
}

func TestStaticServing(t *testing.T) {
	h := testUIHandler(t, testUI(t))
	get := func(p string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		return rec.Code, rec.Body.String()
	}

	// Корень — оболочка приложения.
	if code, body := get("/"); code != 200 || !strings.Contains(body, "PB-UI") {
		t.Fatalf("/: code=%d body=%q (ждали index.html)", code, body)
	}
	// Файл ассетов — его содержимое.
	if code, body := get("/assets/app.js"); code != 200 || body != "console.log(1)" {
		t.Fatalf("/assets/app.js: code=%d body=%q", code, body)
	}
	// Клиентский маршрут без файла — SPA-фолбэк в index.html.
	if code, body := get("/liquidity"); code != 200 || !strings.Contains(body, "PB-UI") {
		t.Fatalf("/liquidity: code=%d body=%q (ждали SPA-фолбэк)", code, body)
	}
	// Каталог — без листинга (SPA-фолбэк, а не содержимое каталога).
	if code, body := get("/assets/"); code != 200 || !strings.Contains(body, "PB-UI") {
		t.Fatalf("/assets/: code=%d body=%q (ждали без листинга)", code, body)
	}
	// /api/* не перехватывается статикой: 404 из API-мультекса, не index.
	if code, _ := get("/api/nope"); code != 404 {
		t.Fatalf("/api/nope: code=%d (ждали 404 из API)", code)
	}
	// Обход путей через .. — в любом случае не содержимое выше каталога UI.
	// (Реальный net/http-сервер к тому же нормализует путь; у голых
	// хендлеров path.Clean в withStatic не даёт уйти выше fsys.)
	if _, body := get("/../etc/passwd"); strings.Contains(body, "root:") {
		t.Fatalf("/../etc/passwd: выдано содержимое каталога выше UI")
	}
}

func TestStaticDisabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.Dashboard.Listen = "127.0.0.1:0"
	s := &Server{Cfg: cfg}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 404 {
		t.Fatalf("/ без dashboard.ui: code=%d (ждали 404 из API-мультекса)", rec.Code)
	}
}
