package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
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

	// Версия сборки раздачи (TZ UI v5 U02): UI сверяет со своим бандлом —
	// рассхождение (оператор смотрит старую раздачу) видно на «Настройках».
	var build *uiBuild
	if b := readBuildInfo(s.Cfg.Dashboard.UI); b != nil {
		build = b
	}

	mc := s.Cfg.Dashboard.Map
	writeJSON(w, http.StatusOK, map[string]any{
		"currencies":        currencies,
		"countries":         s.Cfg.Dashboard.Countries,
		"market_currencies": s.Cfg.Dashboard.MarketCurrencies,
		"deal_types":        s.Cfg.Dashboard.DealTypes,
		"fx_last_rate_date": lastRateDate,
		"ui_build":          build,
		"map": map[string]any{
			"style_url":        mc.StyleURL,
			"points_max":       mc.PointsMax,
			"default_lng":      mc.DefaultLng,
			"default_lat":      mc.DefaultLat,
			"default_zoom":     mc.DefaultZoom,
			"zone_color_min_n": mc.ZoneColorMinN,
		},
	})
}

// uiBuild — версия сборки, которой сервер раздаёт static:
// dist/.build-info.json пишет сборщик (TZ UI v5 U02).
type uiBuild struct {
	Commit  string `json:"commit"`
	BuiltAt string `json:"built_at"`
}

// readBuildInfo — dist/.build-info.json из каталога раздачи; nil, если
// файла нет или он не читается (meta отвечает и без версии — сверка
// UI-бандла против сервера просто не выполняется).
func readBuildInfo(uiDir string) *uiBuild {
	if uiDir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(uiDir, ".build-info.json"))
	if err != nil {
		return nil
	}
	var b uiBuild
	if err := json.Unmarshal(data, &b); err != nil || b.Commit == "" {
		return nil
	}
	return &b
}
