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

type objectOut struct {
	ID             int64         `json:"id"`
	Country        string        `json:"country"`
	DealType       string        `json:"deal_type"`
	ZoneID         *int64        `json:"zone_id"`
	Address        *string       `json:"address"`
	AreaSqM        *float64      `json:"area_sqm"`
	Rooms          *int16        `json:"rooms"`
	PropertyType   *string       `json:"property_type"`
	PriceMinor     *int64        `json:"price_minor"`
	Currency       *string       `json:"currency"`
	PriceDisplay   *priceDisplay `json:"price_display,omitempty"`
	Status         string        `json:"status"`
	DelistedReason *string       `json:"delisted_reason"`
	FirstSeenAt    time.Time     `json:"first_seen_at"`
	LastSeenAt     time.Time     `json:"last_seen_at"`
	DelistedAt     *time.Time    `json:"delisted_at"`
}

const objectsSelect = `id, country, deal_type, zone_id, address, area_sqm, rooms, property_type,
	current_price_minor, currency, status, delisted_reason, first_seen_at, last_seen_at, delisted_at`

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
		conds = append(conds, fmt.Sprintf("country = $%d", len(args)+1))
		args = append(args, c)
	}
	if st := r.URL.Query().Get("status"); st != "" {
		if st != "active" && st != "delisted" {
			writeErr(w, httpError(http.StatusBadRequest, "status: active | delisted"))
			return
		}
		conds = append(conds, fmt.Sprintf("status = $%d", len(args)+1))
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

	q := "SELECT " + objectsSelect + " FROM objects" + where +
		" ORDER BY id DESC LIMIT $" + strconv.Itoa(len(args)+1) + " OFFSET $" + strconv.Itoa(len(args)+2)
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
		if err := rows.Scan(&o.ID, &o.Country, &o.DealType, &o.ZoneID, &o.Address,
			&o.AreaSqM, &o.Rooms, &o.PropertyType, &o.PriceMinor, &o.Currency,
			&o.Status, &o.DelistedReason, &o.FirstSeenAt, &o.LastSeenAt, &o.DelistedAt); err != nil {
			writeErr(w, err)
			return
		}
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
	err = s.Pool.QueryRow(ctx, "SELECT "+objectsSelect+" FROM objects WHERE id = $1", id).
		Scan(&o.ID, &o.Country, &o.DealType, &o.ZoneID, &o.Address, &o.AreaSqM, &o.Rooms,
			&o.PropertyType, &o.PriceMinor, &o.Currency, &o.Status, &o.DelistedReason,
			&o.FirstSeenAt, &o.LastSeenAt, &o.DelistedAt)
	if err != nil {
		writeErr(w, httpError(http.StatusNotFound, "объект %d не найден", id))
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
	if err := s.applyDisplay(ctx, &o, displayTo, conv); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}
