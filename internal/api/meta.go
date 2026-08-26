package api

import (
	"net/http"
	"time"
)

// GET /api/health — жив ли сервис и БД.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.Pool.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db_down"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GET /api/meta — справочные данные для UI:
// валюты из БД, страны/валюты рынков и типы сделок из конфига
// (данные с источником, не хардкод — ТЗ §0.1).
func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var currencies []map[string]any
	rows, err := s.Pool.Query(ctx, "SELECT code, exponent FROM currencies ORDER BY code")
	if err != nil {
		writeErr(w, err)
		return
	}
	for rows.Next() {
		var code string
		var exponent int
		if err := rows.Scan(&code, &exponent); err != nil {
			rows.Close()
			writeErr(w, err)
			return
		}
		currencies = append(currencies, map[string]any{"code": code, "exponent": exponent})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	// Последний загруженный курс — для прозрачности («на каком курсе мы»)
	var lastRateDate *time.Time
	_ = s.Pool.QueryRow(ctx, "SELECT max(rate_date) FROM fx_rates").Scan(&lastRateDate)

	writeJSON(w, http.StatusOK, map[string]any{
		"currencies":        currencies,
		"countries":         s.Cfg.Dashboard.Countries,
		"market_currencies": s.Cfg.Dashboard.MarketCurrencies,
		"deal_types":        s.Cfg.Dashboard.DealTypes,
		"fx_last_rate_date": lastRateDate,
	})
}
