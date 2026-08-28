package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"propertyboss/internal/fx"
	"propertyboss/internal/money"
)

// priceDisplay — конвертированная сумма для отображения.
// Всегда derived: в БД цена живёт в валюте рынка (ТЗ §5).
type priceDisplay struct {
	Minor     int64  `json:"minor"`
	Currency  string `json:"currency"`
	Derived   bool   `json:"derived"`
	RateDate  string `json:"rate_date"`
	RateStale bool   `json:"rate_stale"`
}

// valuationOut — последняя оценка объекта (valuations, этап 5, ТЗ §7.3):
// число (price_deviation) хранится вместе с интервалом, размером выборки
// и качеством модели; в дашборде число без интервала не показывается.
type valuationOut struct {
	ModelVersion      *string    `json:"model_version"`
	PriceDeviation    *float64   `json:"price_deviation"`
	NullReason        *string    `json:"null_reason,omitempty"`
	PredictedMinor    *int64     `json:"predicted_price_minor"`
	IntervalLowMinor  *int64     `json:"interval_low_minor"`
	IntervalHighMinor *int64     `json:"interval_high_minor"`
	SampleSize        int        `json:"sample_size"`
	RSquared          *float64   `json:"r_squared"`
	ZoneFallback      bool       `json:"zone_fallback"`
	ComputedAt        *time.Time `json:"computed_at"`
}

type objectOut struct {
	ID             int64         `json:"id"`
	Country        string        `json:"country"`
	DealType       string        `json:"deal_type"`
	ZoneID         *int64        `json:"zone_id"`
	ZoneName       *string       `json:"zone_name,omitempty"`
	ZoneLevel      *string       `json:"zone_level,omitempty"`
	ZoneSource     *string       `json:"zone_source,omitempty"`
	Address        *string       `json:"address"`
	AreaSqM        *float64      `json:"area_sqm"`
	Rooms          *int16        `json:"rooms"`
	PropertyType   *string       `json:"property_type"`
	PriceMinor     *int64        `json:"price_minor"`
	Currency       *string       `json:"currency"`
	PriceDisplay   *priceDisplay `json:"price_display,omitempty"`
	Valuation      *valuationOut `json:"valuation,omitempty"`
	Status         string        `json:"status"`
	DelistedReason *string       `json:"delisted_reason"`
	FirstSeenAt    time.Time     `json:"first_seen_at"`
	LastSeenAt     time.Time     `json:"last_seen_at"`
	DelistedAt     *time.Time    `json:"delisted_at"`
}

// objectsSelect/objectsFrom — с LEFT JOIN zones (имя/уровень/источник зоны
// рядом с объектом, ТЗ §13) и LEFT JOIN LATERAL последней оценки
// (ТЗ §7.3: отклонение всегда с интервалом и размерами выборки).
// Квалификация objects.* обязательна: в zones те же имена колонок.
const objectsSelect = `objects.id, objects.country, objects.deal_type, objects.zone_id, objects.address, objects.area_sqm, objects.rooms, objects.property_type,
	objects.current_price_minor, objects.currency, objects.status, objects.delisted_reason, objects.first_seen_at, objects.last_seen_at, objects.delisted_at,
	z.name AS zone_name, z.level AS zone_level, z.source AS zone_source,
	v.model_version, v.price_deviation, v.deviation_null_reason, v.predicted_price_minor,
	v.interval_low_minor, v.interval_high_minor, v.sample_size, v.r_squared, v.zone_fallback, v.computed_at`

const objectsFrom = ` FROM objects
		LEFT JOIN zones z ON z.id = objects.zone_id
		LEFT JOIN LATERAL (
			SELECT v2.* FROM valuations v2 WHERE v2.object_id = objects.id
			ORDER BY v2.computed_at DESC, v2.id DESC
			LIMIT 1
		) v ON true`

// attachValuation — подвешивает последнюю оценку, если она есть
// (LEFT JOIN LATERAL → все v.* NULL, когда valuations пусто).
// sample_size/zone_fallback в таблице NOT NULL, но приходят NULL
// без строки — указатели защищают от падения scan.
func attachValuation(o *objectOut, val *valuationOut, size *int, fb *bool) {
	if val.ModelVersion == nil {
		return
	}
	val.SampleSize = 0
	if size != nil {
		val.SampleSize = *size
	}
	val.ZoneFallback = fb != nil && *fb
	o.Valuation = val
}

// applyDisplay заполняет PriceDisplay суммой в displayTo по курсу на дату
// наблюдения (last_seen_at). Отсутствие курса — цена рынка остаётся,
// производного значения нет (ТЗ §5: не молчаливая подстановка).
func (s *Server) applyDisplay(ctx context.Context, o *objectOut, displayTo *money.Currency,
	conv func(minor int64, from money.Currency, onDate time.Time) (int64, *fx.RateLookup, error)) error {
	if displayTo == nil || conv == nil || o.PriceMinor == nil || o.Currency == nil || *o.Currency == displayTo.Code {
		return nil
	}
	from, err := s.currencyRef(ctx, *o.Currency)
	if err != nil {
		return err
	}
	minor2, meta, err := conv(*o.PriceMinor, from, o.LastSeenAt)
	if err != nil || meta == nil {
		return nil
	}
	o.PriceDisplay = &priceDisplay{
		Minor:     minor2,
		Currency:  displayTo.Code,
		Derived:   true,
		RateDate:  meta.RateDate.Format("2006-01-02"),
		RateStale: meta.Stale,
	}
	return nil
}

// GET /api/objects?country=&status=&page=&per_page=&display_currency=
func (s *Server) handleListObjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	page, err := queryInt(r, "page", 1, 100000)
	if err != nil {
		writeErr(w, err)
		return
	}
	perPage, err := queryInt(r, "per_page", 50, 200)
	if err != nil {
		writeErr(w, err)
		return
	}

	var displayTo *money.Currency
	var conv func(minor int64, from money.Currency, onDate time.Time) (int64, *fx.RateLookup, error)
	if dc := r.URL.Query().Get("display_currency"); dc != "" {
		to, err := s.currencyRef(ctx, dc)
		if err != nil {
			writeErr(w, err)
			return
		}
		displayTo = &to
		conv = s.displayConverter(ctx, to)
	}

	var conds []string
	var args []any
	if c := r.URL.Query().Get("country"); c != "" {
		conds = append(conds, fmt.Sprintf("objects.country = $%d", len(args)+1))
		args = append(args, c)
	}
	if st := r.URL.Query().Get("status"); st != "" {
		if st != "active" && st != "delisted" {
			writeErr(w, httpError(http.StatusBadRequest, "status: active | delisted"))
			return
		}
		conds = append(conds, fmt.Sprintf("objects.status = $%d", len(args)+1))
		args = append(args, st)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	var total int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM objects"+where, args...).Scan(&total); err != nil {
		writeErr(w, err)
		return
	}

	q := "SELECT " + objectsSelect + objectsFrom + where +
		" ORDER BY objects.id DESC LIMIT $" + strconv.Itoa(len(args)+1) + " OFFSET $" + strconv.Itoa(len(args)+2)
	args = append(args, perPage, (page-1)*perPage)

	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	out := []objectOut{}
	for rows.Next() {
		var o objectOut
		var val valuationOut
		var valSize *int
		var valFB *bool
		if err := rows.Scan(&o.ID, &o.Country, &o.DealType, &o.ZoneID, &o.Address,
			&o.AreaSqM, &o.Rooms, &o.PropertyType, &o.PriceMinor, &o.Currency,
			&o.Status, &o.DelistedReason, &o.FirstSeenAt, &o.LastSeenAt, &o.DelistedAt,
			&o.ZoneName, &o.ZoneLevel, &o.ZoneSource,
			&val.ModelVersion, &val.PriceDeviation, &val.NullReason, &val.PredictedMinor,
			&val.IntervalLowMinor, &val.IntervalHighMinor, &valSize, &val.RSquared, &valFB, &val.ComputedAt); err != nil {
			writeErr(w, err)
			return
		}
		attachValuation(&o, &val, valSize, valFB)
		if err := s.applyDisplay(ctx, &o, displayTo, conv); err != nil {
			writeErr(w, err)
			return
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "page": page, "per_page": perPage, "objects": out})
}

// GET /api/objects/{id}
func (s *Server) handleGetObject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, httpError(http.StatusBadRequest, "id: не целое"))
		return
	}
	var o objectOut
	var val valuationOut
	var valSize *int
	var valFB *bool
	err = s.Pool.QueryRow(ctx, "SELECT "+objectsSelect+objectsFrom+" WHERE objects.id = $1", id).
		Scan(&o.ID, &o.Country, &o.DealType, &o.ZoneID, &o.Address, &o.AreaSqM, &o.Rooms,
			&o.PropertyType, &o.PriceMinor, &o.Currency, &o.Status, &o.DelistedReason,
			&o.FirstSeenAt, &o.LastSeenAt, &o.DelistedAt,
			&o.ZoneName, &o.ZoneLevel, &o.ZoneSource,
			&val.ModelVersion, &val.PriceDeviation, &val.NullReason, &val.PredictedMinor,
			&val.IntervalLowMinor, &val.IntervalHighMinor, &valSize, &val.RSquared, &valFB, &val.ComputedAt)
	if err != nil {
		writeErr(w, httpError(http.StatusNotFound, "объект %d не найден", id))
		return
	}
	attachValuation(&o, &val, valSize, valFB)
	var displayTo *money.Currency
	var conv func(minor int64, from money.Currency, onDate time.Time) (int64, *fx.RateLookup, error)
	if dc := r.URL.Query().Get("display_currency"); dc != "" {
		to, err := s.currencyRef(ctx, dc)
		if err != nil {
			writeErr(w, err)
			return
		}
		displayTo = &to
		conv = s.displayConverter(ctx, to)
	}
	if err := s.applyDisplay(ctx, &o, displayTo, conv); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}
