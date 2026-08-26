// Package api — REST API дашборда (ТЗ, этап 2).
//
// Правила денег (ТЗ §5): сумма в JSON — целое число минорных единиц + валюта,
// float для денег не используется. Конвертированное значение для отображения
// отдаётся отдельным полем с пометкой derived и метаданными курса.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
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
	mux.HandleFunc("GET /api/objects/{id}", s.handleGetObject)
	return mux
}

// Serve запускает HTTP-сервер; блокируется до ctx.Done().
func (s *Server) Serve(ctx context.Context, listen string) error {
	srv := &http.Server{
		Addr:              listen,
		Handler:           s.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
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
