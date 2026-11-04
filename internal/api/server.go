// Package api — REST API дашборда (ТЗ, этап 2).
//
// Правила денег (ТЗ §5): сумма в JSON — целое число минорных единиц + валюта,
// float для денег не используется. Конвертированное значение для отображения
// отдаётся отдельным полем с пометкой derived и метаданными курса.
package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/config"
	"propertyboss/internal/fx"
	"propertyboss/internal/money"
)

// Server — зависимости API.
type Server struct {
	Pool *pgxpool.Pool
	Cfg  *config.Config
}

// New — конструктор.
func New(pool *pgxpool.Pool, cfg *config.Config) *Server {
	return &Server{Pool: pool, Cfg: cfg}
}

// Routes — маршруты API.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/meta", s.handleMeta)
	mux.HandleFunc("GET /api/sources", s.handleListSources)
	mux.HandleFunc("GET /api/attribute-registry", s.handleListAttributeRegistry)
	mux.HandleFunc("GET /api/search-configs", s.handleListSearchConfigs)
	mux.HandleFunc("GET /api/search-configs/{id}", s.handleGetSearchConfig)
	mux.HandleFunc("POST /api/search-configs", s.handleCreateSearchConfig)
	mux.HandleFunc("PUT /api/search-configs/{id}", s.handleUpdateSearchConfig)
	mux.HandleFunc("DELETE /api/search-configs/{id}", s.handleDeleteSearchConfig)
	mux.HandleFunc("GET /api/objects", s.handleListObjects)
	// U30: литеральный сегмент «map» приоритетнее wild-card {id} (Go 1.22 mux).
	mux.HandleFunc("GET /api/objects/map", s.handleMapObjects)
	mux.HandleFunc("GET /api/objects/{id}", s.handleGetObject)
	mux.HandleFunc("GET /api/liquidity", s.handleGetLiquidity)
	mux.HandleFunc("GET /api/models", s.handleModels)
	mux.HandleFunc("GET /api/zones", s.handleListZones)
	mux.HandleFunc("GET /api/zones/geojson", s.handleZonesGeoJSON)
	mux.HandleFunc("GET /api/zones/{id}", s.handleGetZone)
	return mux
}

// Handler — API-маршруты + (если задан dashboard.ui) статическая раздача
// собранного фронтенда тем же портом, что и API, + (если задан
// photos.dir) локальные копии фотографий под /photos (TZ v4 §4.4).
func (s *Server) Handler() http.Handler {
	h := s.Routes()
	if dir := s.Cfg.Dashboard.UI; dir != "" {
		h = withStatic(h, dir)
	}
	if dir := s.Cfg.Photos.Dir; dir != "" {
		h = withPhotos(h, dir)
	}
	return h
}

// withStatic — дашборд на одном порту: пути, не начинающиеся с /api/,
// — файлы каталога (web/dist); путь без файла — index.html (SPA-фолбэк
// для клиентских маршрутов). Каталоги листингом не отдаются.
// Статика отдаётся gzip (TZ_UI §4 — TTI на медленных сетях) с
// кэшированием: ассеты с hash в имени — immutable, index.html — no-cache.
func withStatic(api http.Handler, dir string) http.Handler {
	fsys := http.Dir(dir)
	files := http.FileServer(fsys)
	cache := newGzipCache()
	openInfo := func(name string) fs.FileInfo {
		f, err := fsys.Open(name)
		if err != nil {
			return nil
		}
		defer f.Close()
		fi, _ := f.Stat()
		return fi
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			api.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			api.ServeHTTP(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html" // корень — оболочка приложения
		}
		if fi := openInfo(name); fi != nil && !fi.IsDir() {
			serveStaticFile(w, r, fsys, files, name, fi, cache)
			return
		}
		// Путь без файла — SPA-фолбэк: клиентский маршрут отдаёт
		// оболочку приложения (URL переписывается, чтобы FileServer
		// не ушёл в 404 на несуществующий путь).
		rr := *r
		u := *r.URL
		u.Path = "/"
		rr.URL = &u
		if fi := openInfo("index.html"); fi != nil && !fi.IsDir() {
			serveStaticFile(w, &rr, fsys, files, "index.html", fi, cache)
			return
		}
		files.ServeHTTP(w, &rr)
	})
}

// withPhotos — локальные копии фотографий (TZ v4 §4.4):
// /photos/<objID>/<NN>_<kind>.webp из photos.dir. Путь не уходит
// выше каталога: path.Clean разрешает «..» до обращения к файлам
// (как в withStatic). Прочие пути — дальше по цепочке (API/статика).
func withPhotos(next http.Handler, dir string) http.Handler {
	fsys := http.Dir(dir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		rel := strings.TrimPrefix(path.Clean(r.URL.Path), "/photos/")
		if rel == "" || rel == "/" || strings.HasPrefix(rel, "/") {
			next.ServeHTTP(w, r)
			return
		}
		f, err := fsys.Open(rel)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil || fi.IsDir() {
			http.NotFound(w, r)
			return
		}
		// Копия привязана к объявлению и пишется один раз — кэш на
		// сутки (фото с площадки не меняются; новые ссылки — новые
		// файлы других объявлений).
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("Last-Modified", fi.ModTime().UTC().Format(http.TimeFormat))
		if ct := mime.TypeByExtension(path.Ext(rel)); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		_, _ = io.Copy(w, f)
	})
}

// gzipCache — результат сжатия файла, ключ (имя, mtime): пересборка
// UI меняет mtime и кэш не отдаёт устаревшее. Размер ограничен —
// в dist Vite единицы файлов.
type gzipCache struct {
	mu    sync.Mutex
	byKey map[string][]byte
}

func newGzipCache() *gzipCache {
	return &gzipCache{byKey: make(map[string][]byte)}
}

func (c *gzipCache) get(name string, mod time.Time) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.byKey[name+"|"+mod.UTC().Format(time.RFC3339Nano)]
	return b, ok
}

func (c *gzipCache) put(name string, mod time.Time, b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.byKey) >= 32 {
		c.byKey = make(map[string][]byte)
	}
	c.byKey[name+"|"+mod.UTC().Format(time.RFC3339Nano)] = b
}

// maxGzipSource — файлы крупнее этого gzip-мятся на лету не будут
// (FileServer их и так отдаёт потоком).
const maxGzipSource = 8 << 20

// gzippableExt — расширения, для которых gzip даёт выигрыш
// (текстовые артефакты сборки Vite).
var gzippableExt = map[string]bool{
	".js": true, ".css": true, ".html": true, ".svg": true, ".json": true, ".map": true,
}

func gzippable(name string) bool {
	return gzippableExt[strings.ToLower(path.Ext(name))]
}

func acceptsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

func gzipStatic(data []byte) []byte {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.DefaultCompression)
	_, _ = zw.Write(data)
	_ = zw.Close()
	return buf.Bytes()
}

// serveStaticFile — раздача одного файла статики: заголовки кэширования
// + gzip (если клиент принял и файл сжимаемый). Прочие случаи —
// штатный FileServer.
func serveStaticFile(w http.ResponseWriter, r *http.Request, fsys http.Dir, files http.Handler, name string, fi fs.FileInfo, cache *gzipCache) {
	switch {
	case name == "index.html":
		// SPA-оболочка меняется при пересборке — ре-валидация.
		w.Header().Set("Cache-Control", "no-cache")
	default:
		// Имена ассетов Vite содержат hash — файл неизменяем до пересборки.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	w.Header().Set("Last-Modified", fi.ModTime().UTC().Format(http.TimeFormat))
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if !gzippable(name) || !acceptsGzip(r) {
		files.ServeHTTP(w, r)
		return
	}
	if b, ok := cache.get(name, fi.ModTime()); ok {
		writeGzip(w, b)
		return
	}
	f, err := fsys.Open(name)
	if err != nil {
		files.ServeHTTP(w, r)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxGzipSource+1))
	if err != nil || len(data) > maxGzipSource {
		files.ServeHTTP(w, r)
		return
	}
	b := gzipStatic(data)
	cache.put(name, fi.ModTime(), b)
	writeGzip(w, b)
}

func writeGzip(w http.ResponseWriter, b []byte) {
	h := w.Header()
	h.Set("Content-Encoding", "gzip")
	h.Set("Vary", "Accept-Encoding")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}

// Serve запускает HTTP-сервер; блокируется до ctx.Done().
func (s *Server) Serve(ctx context.Context, listen string) error {
	srv := &http.Server{
		Addr:              listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if s.Cfg.Dashboard.UI != "" {
		log.Printf("api: UI (статическая раздача): %s", s.Cfg.Dashboard.UI)
	}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("api: слушаю %s", listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

// --- вспомогательные функции ---

type apiError struct {
	Status int    `json:"-"`
	Msg    string `json:"error"`
}

func httpError(status int, format string, args ...any) *apiError {
	return &apiError{Status: status, Msg: fmt.Sprintf(format, args...)}
}

// Error — *apiError является error, чтобы передаваться через writeErr/return.
func (e *apiError) Error() string { return e.Msg }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		writeJSON(w, ae.Status, ae)
		return
	}
	log.Printf("api: внутренняя ошибка: %v", err)
	writeJSON(w, http.StatusInternalServerError, apiError{Status: 500, Msg: "внутренняя ошибка"})
}

// readJSON — чтение JSON; UseNumber держит числа в json.Number,
// чтобы валидация не проходила через float64 (ТЗ §5 — по духу:
// точность до явной проверки, а не молчаливая потеря).
func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return httpError(http.StatusBadRequest, "некорректный JSON: %v", err)
	}
	return nil
}

func queryInt(r *http.Request, name string, def, max int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 || v > max {
		return 0, httpError(http.StatusBadRequest, "параметр %s: целое число 1..%d", name, max)
	}
	return v, nil
}

// currencyRef — валюта из справочника (код + экспонента), не из кода.
func (s *Server) currencyRef(ctx context.Context, code string) (money.Currency, error) {
	var exponent int
	err := s.Pool.QueryRow(ctx, "SELECT exponent FROM currencies WHERE code = $1", code).Scan(&exponent)
	if err != nil {
		return money.Currency{}, httpError(http.StatusBadRequest, "неизвестная валюта %q (не в справочнике currencies)", code)
	}
	return money.Currency{Code: code, Exponent: exponent}, nil
}

// displayConverter — фабрика конвертеров для отображения цен.
// ТЗ §5: курс фиксируется на дату наблюдения — для объекта это last_seen_at,
// а не «сегодня». Кэш по (валюта, дата) на время запроса.
func (s *Server) displayConverter(ctx context.Context, to money.Currency) func(minor int64, from money.Currency, onDate time.Time) (int64, *fx.RateLookup, error) {
	cache := map[string]*fx.RateLookup{}
	return func(minor int64, from money.Currency, onDate time.Time) (int64, *fx.RateLookup, error) {
		if from.Code == to.Code {
			return minor, nil, nil
		}
		lookup := func(quote string) (*fx.RateLookup, error) {
			key := quote + "|" + onDate.Format("2006-01-02")
			if rl, ok := cache[key]; ok {
				return rl, nil
			}
			rl, err := fx.LookupEURRate(ctx, s.Pool, quote, onDate)
			if err != nil {
				return nil, err
			}
			cache[key] = rl
			return rl, nil
		}
		return fx.ConvertMinor(minor, from, to, lookup)
	}
}
