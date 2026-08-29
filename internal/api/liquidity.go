package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// liquidityModelOut — строка liquidity_models (ТЗ §9.4): метрики
// валидации сохраняются вместе с версией модели и выводятся в
// дашборде (в т.ч. калибровочная кривая для графика).
type liquidityModelOut struct {
	ModelVersion     string          `json:"model_version"`
	Country          string          `json:"country"`
	DealType         string          `json:"deal_type"`
	Status           string          `json:"status"`
	RejectReason     *string         `json:"reject_reason,omitempty"`
	HorizonDays      int             `json:"horizon_days"`
	MinEvents        int             `json:"min_events"`
	NCompletedEvents int             `json:"n_completed_events"`
	NPersonPeriods   int             `json:"n_person_periods"`
	NParams          *int            `json:"n_params"`
	TrainCutoffAt    *time.Time      `json:"train_cutoff_at"`
	NTrain           int             `json:"n_train"`
	NTest            int             `json:"n_test"`
	Calibration      json.RawMessage `json:"calibration"`
	MaxCalibDev      *float64        `json:"max_calib_dev"`
	BrierScore       *float64        `json:"brier_score"`
	BrierDecomp      json.RawMessage `json:"brier_decomp"`
	CIndex           *float64        `json:"c_index"`
	Params           json.RawMessage `json:"params"`
	ComputedAt       time.Time       `json:"computed_at"`
}

// GET /api/liquidity?country=XX&deal_type=T — последняя версия модели
// ликвидности. Параметры опциональны; без них — последняя строка
// по всем (страна, тип сделки). Пока ни одного прогона не было —
// 200 с model:null (холодный старт, ТЗ §14.5).
func (s *Server) handleGetLiquidity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	var conds []string
	var args []any
	if c := q.Get("country"); c != "" {
		if len(c) != 2 {
			writeErr(w, httpError(http.StatusBadRequest, "country: код страны из двух букв"))
			return
		}
		conds = append(conds, fmt.Sprintf("country = $%d", len(args)+1))
		args = append(args, c)
	}
	if d := q.Get("deal_type"); d != "" {
		ok := false
		for _, dt := range s.Cfg.Dashboard.DealTypes {
			if dt == d {
				ok = true
				break
			}
		}
		if !ok {
			writeErr(w, httpError(http.StatusBadRequest, "deal_type: %v (из конфига dashboard.deal_types)", s.Cfg.Dashboard.DealTypes))
			return
		}
		conds = append(conds, fmt.Sprintf("deal_type = $%d", len(args)+1))
		args = append(args, d)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	var m liquidityModelOut
	var calJSON, decJSON, paramsJSON []byte
	err := s.Pool.QueryRow(ctx, `SELECT model_version, country, deal_type, status, reject_reason,
		horizon_days, min_events, n_completed_events, n_person_periods, n_params,
		train_cutoff_at, n_train, n_test, calibration, max_calib_dev, brier_score,
		brier_decomp, c_index, params, computed_at
		FROM liquidity_models`+where+`
		ORDER BY computed_at DESC, id DESC
		LIMIT 1`, args...).
		Scan(&m.ModelVersion, &m.Country, &m.DealType, &m.Status, &m.RejectReason,
			&m.HorizonDays, &m.MinEvents, &m.NCompletedEvents, &m.NPersonPeriods, &m.NParams,
			&m.TrainCutoffAt, &m.NTrain, &m.NTest, &calJSON, &m.MaxCalibDev, &m.BrierScore,
			&decJSON, &m.CIndex, &paramsJSON, &m.ComputedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, map[string]any{"model": nil})
			return
		}
		writeErr(w, err)
		return
	}
	// Пустые JSONB — null, а не пустые скобки (удобно для дашборда).
	if len(calJSON) > 0 {
		m.Calibration = calJSON
	}
	if len(decJSON) > 0 {
		m.BrierDecomp = decJSON
	}
	if len(paramsJSON) > 0 {
		m.Params = paramsJSON
	}
	writeJSON(w, http.StatusOK, map[string]any{"model": m})
}
