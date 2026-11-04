package api

import (
	"encoding/json"
	"net/http"
	"time"
)

// modelsOut — данные раздела «Модели» (TZ_UI v3 §6.5): состояние каждой
// пары «страна — тип сделки» (ценовые отклонения + уход с рынка),
// показатели качества и источники со временем последнего скана.
// Пары перечисляются из конфига (dashboard.countries × deal_types),
// поэтому в списке есть и пустые рынки — UI показывает «нет объектов»,
// а не просто не знает про них.
type modelsOut struct {
	Pairs   []pairModelsOut `json:"pairs"`
	Sources []sourceOut     `json:"sources"`
}

// pairModelsOut — состояние одной пары «страна — тип сделки».
type pairModelsOut struct {
	Country     string             `json:"country"`
	DealType    string             `json:"deal_type"`
	ObjectCount int64              `json:"object_count"`
	Deviation   *deviationStateOut `json:"deviation,omitempty"`
	Liquidity   *liquidityPairOut  `json:"liquidity,omitempty"`
}

// deviationStateOut — агрегированное состояние ценовых отклонений пары:
// последние оценки каждого объекта. Цифры для разделов «Состояние
// моделей» и «Показатели качества» (TZ_UI v3 §6.5, §7).
type deviationStateOut struct {
	NValued        int64    `json:"n_valued"` // объектов с хотя бы одной оценкой
	NWithDeviation int64    `json:"n_with_deviation"`
	DominantReason string   `json:"dominant_reason,omitempty"` // самая частая причина отсутствия отклонения
	ModelVersion   string   `json:"model_version,omitempty"`
	RSquared       *float64 `json:"r_squared,omitempty"` // максимум по объектам
	SampleSize     int64    `json:"sample_size,omitempty"`
	NZoneFallback  int64    `json:"n_zone_fallback"` // замещение зоны уровнем родителя
}

// liquidityPairOut — последняя версия модели ухода с рынка пары
// (liquidity_models, ТЗ §9.4: метрики валидации хранятся с версией).
// Calibration — тот же формат, что у GET /api/liquidity (массив
// {decile, predicted, actual, n}), чтобы UI рисовал кривую одним кодом.
type liquidityPairOut struct {
	ModelVersion     string          `json:"model_version"`
	Status           string          `json:"status"`
	RejectReason     *string         `json:"reject_reason,omitempty"`
	HorizonDays      int             `json:"horizon_days"`
	MinEvents        int             `json:"min_events"`
	NCompletedEvents int             `json:"n_completed_events"`
	BrierScore       *float64        `json:"brier_score,omitempty"`
	CIndex           *float64        `json:"c_index,omitempty"`
	MaxCalibDev      *float64        `json:"max_calib_dev,omitempty"`
	Calibration      json.RawMessage `json:"calibration,omitempty"`
	ComputedAt       time.Time       `json:"computed_at"`
}

// GET /api/models — состояние моделей для раздела «Модели».
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := modelsOut{Pairs: []pairModelsOut{}}

	// Числа объектов по парам.
	objCounts := map[string]int64{}
	ocRows, err := s.Pool.Query(ctx, "SELECT country, deal_type, count(*) FROM objects GROUP BY 1, 2")
	if err != nil {
		writeErr(w, err)
		return
	}
	for ocRows.Next() {
		var c, dt string
		var n int64
		if err := ocRows.Scan(&c, &dt, &n); err != nil {
			ocRows.Close()
			writeErr(w, err)
			return
		}
		objCounts[c+"|"+dt] = n
	}
	ocRows.Close()
	if err := ocRows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	// Агрегаты ценовых отклонений: последние оценки каждого объекта.
	devRows, err := s.Pool.Query(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (v.object_id)
				o.country, o.deal_type,
				v.model_version, v.price_deviation, v.deviation_null_reason,
				v.r_squared, v.sample_size, v.zone_fallback
			FROM valuations v
			JOIN objects o ON o.id = v.object_id
			ORDER BY v.object_id, v.computed_at DESC, v.id DESC
		)
		SELECT country, deal_type,
		       count(*) AS n_valued,
		       count(price_deviation) AS n_with_deviation,
		       max(r_squared) AS r_squared,
		       max(sample_size) AS sample_size,
		       count(*) FILTER (WHERE zone_fallback) AS n_zone_fallback,
		       max(model_version) AS model_version
		FROM latest
		GROUP BY country, deal_type`)
	if err != nil {
		writeErr(w, err)
		return
	}
	devs := map[string]*deviationStateOut{}
	for devRows.Next() {
		var d deviationStateOut
		var c, dt string
		if err := devRows.Scan(&c, &dt, &d.NValued, &d.NWithDeviation, &d.RSquared, &d.SampleSize, &d.NZoneFallback, &d.ModelVersion); err != nil {
			devRows.Close()
			writeErr(w, err)
			return
		}
		devs[c+"|"+dt] = &d
	}
	devRows.Close()
	if err := devRows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	// Самая частая причина отсутствия отклонения по парам.
	reasonRows, err := s.Pool.Query(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (v.object_id)
				o.country, o.deal_type, v.price_deviation, v.deviation_null_reason
			FROM valuations v
			JOIN objects o ON o.id = v.object_id
			ORDER BY v.object_id, v.computed_at DESC, v.id DESC
		),
		reasons AS (
			SELECT country, deal_type, deviation_null_reason,
			       row_number() OVER (PARTITION BY country, deal_type
			                          ORDER BY count(*) DESC, deviation_null_reason) AS rn
			FROM latest
			WHERE price_deviation IS NULL
			GROUP BY country, deal_type, deviation_null_reason
		)
		SELECT country, deal_type, deviation_null_reason FROM reasons WHERE rn = 1`)
	if err != nil {
		writeErr(w, err)
		return
	}
	for reasonRows.Next() {
		var c, dt, reason string
		if err := reasonRows.Scan(&c, &dt, &reason); err != nil {
			reasonRows.Close()
			writeErr(w, err)
			return
		}
		if d, ok := devs[c+"|"+dt]; ok {
			d.DominantReason = reason
		}
	}
	reasonRows.Close()
	if err := reasonRows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	// Последняя версия модели ухода с рынка на пару.
	liqRows, err := s.Pool.Query(ctx, `
		-- DISTINCT ON требует ключевые выражения в начале списка ВЫБОРА.
		SELECT DISTINCT ON (country, deal_type)
			country, deal_type, model_version, status, reject_reason,
			horizon_days, min_events, n_completed_events,
			brier_score, c_index, max_calib_dev, calibration, computed_at
		FROM liquidity_models
		ORDER BY country, deal_type, computed_at DESC, id DESC`)
	if err != nil {
		writeErr(w, err)
		return
	}
	liqs := map[string]*liquidityPairOut{}
	for liqRows.Next() {
		var l liquidityPairOut
		var c, dt string
		var calJSON []byte
		if err := liqRows.Scan(&c, &dt, &l.ModelVersion, &l.Status, &l.RejectReason,
			&l.HorizonDays, &l.MinEvents, &l.NCompletedEvents, &l.BrierScore,
			&l.CIndex, &l.MaxCalibDev, &calJSON, &l.ComputedAt); err != nil {
			liqRows.Close()
			writeErr(w, err)
			return
		}
		if len(calJSON) > 0 {
			l.Calibration = calJSON
		}
		liqs[c+"|"+dt] = &l
	}
	liqRows.Close()
	if err := liqRows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	// Источники + последний полный скан.
	srcs, err := s.listSources(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	out.Sources = srcs

	// Собираем пары в порядке конфига: сначала страны, внутри — типы сделок.
	for _, c := range s.Cfg.Dashboard.Countries {
		for _, dt := range s.Cfg.Dashboard.DealTypes {
			key := c + "|" + dt
			p := pairModelsOut{Country: c, DealType: dt, ObjectCount: objCounts[key]}
			p.Deviation = devs[key]
			p.Liquidity = liqs[key]
			out.Pairs = append(out.Pairs, p)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
