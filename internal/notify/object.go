package notify

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ObjectSnapshot — снимок объекта для оператора (pb notify object,
// этап 8): цена, срок на рынке, последняя оценка (ТЗ §7) и вероятность
// ухода с рынка (ТЗ §9.3). Тот же контракт, что и у очереди:
// модельные выходы — с интервалом, размером выборки и причиной NULL,
// а не голое число (критерий этапа 8).
type ObjectSnapshot struct {
	ID           int64
	Country      string
	DealType     string
	Status       string
	PriceMinor   *int64
	Currency     *string
	CurrExponent *int
	DaysOnMarket *int

	Valuation *ValuationInfo
	Hazard    *HazardInfo
}

// ValuationInfo — последняя строка valuations объекта.
type ValuationInfo struct {
	DeviationPct    *float64
	DeviationReason string // обязателен, если DeviationPct == nil
	IntervalLow     *int64
	IntervalHigh    *int64
	SampleSize      int
	RSquared        *float64
	ZoneFallback    bool
	ModelVersion    string
}

// HazardInfo — последняя строка liquidity_estimates объекта.
type HazardInfo struct {
	Probability   *float64
	NullReason    string // обязателен, если Probability == nil
	HorizonDays   int
	ModelVersion  string
	EventsInTrain int
}

// ObjectSnapshotFor — текущее состояние объекта: последняя оценка и
// последняя оценка ликвидности (по computed_at). Объект не найден —
// pgx.ErrNoRows.
func ObjectSnapshotFor(ctx context.Context, pool *pgxpool.Pool, id int64) (*ObjectSnapshot, error) {
	var (
		s         ObjectSnapshot
		price     *int64
		currency  *string
		exponent  *int
		firstSeen time.Time
		lastSeen  time.Time
		delisted  *time.Time

		valDev    *float64
		valReason *string
		valLow    *int64
		valHigh   *int64
		valN      *int
		valR2     *float64
		valFall   *bool
		valVer    *string

		hazP      *float64
		hazReason *string
		hazHor    *int
		hazVer    *string
		hazEv     *int
	)
	err := pool.QueryRow(ctx, `
		SELECT o.id, o.country, o.deal_type, o.status,
		       o.current_price_minor, o.currency, c.exponent,
		       o.first_seen_at, o.last_seen_at, o.delisted_at,
		       v.price_deviation, v.deviation_null_reason,
		       v.interval_low_minor, v.interval_high_minor,
		       v.sample_size, v.r_squared, v.zone_fallback, v.model_version,
		       h.hazard_probability, h.null_reason, h.horizon_days,
		       h.model_version, h.events_in_training
		FROM objects o
		LEFT JOIN currencies c ON c.code = o.currency
		LEFT JOIN LATERAL (
			SELECT * FROM valuations
			WHERE object_id = o.id ORDER BY computed_at DESC LIMIT 1
		) v ON true
		LEFT JOIN LATERAL (
			SELECT * FROM liquidity_estimates
			WHERE object_id = o.id ORDER BY computed_at DESC LIMIT 1
		) h ON true
		WHERE o.id = $1`, id).Scan(
		&s.ID, &s.Country, &s.DealType, &s.Status,
		&price, &currency, &exponent,
		&firstSeen, &lastSeen, &delisted,
		&valDev, &valReason, &valLow, &valHigh, &valN, &valR2, &valFall, &valVer,
		&hazP, &hazReason, &hazHor, &hazVer, &hazEv,
	)
	if err != nil {
		return nil, err
	}
	s.PriceMinor = price
	s.Currency = currency
	s.CurrExponent = exponent

	if price != nil {
		// Дней на рынке: от первого sightings до текущего (active)
		// либо до снятия (delisted); delisted_at пустой — last_seen_at.
		end := time.Now().UTC()
		if s.Status == "delisted" {
			if delisted != nil {
				end = *delisted
			} else {
				end = lastSeen
			}
		}
		d := int(end.Sub(firstSeen).Hours() / 24)
		if d < 0 {
			d = 0
		}
		s.DaysOnMarket = &d
	}

	if valDev != nil || valReason != nil {
		v := &ValuationInfo{ModelVersion: derefStr(valVer)}
		v.DeviationPct = valDev
		v.IntervalLow, v.IntervalHigh = valLow, valHigh
		v.RSquared = valR2
		if valN != nil {
			v.SampleSize = *valN
		}
		if valFall != nil {
			v.ZoneFallback = *valFall
		}
		if valReason != nil {
			v.DeviationReason = *valReason
		}
		s.Valuation = v
	}
	if hazP != nil || hazReason != nil {
		h := &HazardInfo{ModelVersion: derefStr(hazVer)}
		h.Probability = hazP
		if hazHor != nil {
			h.HorizonDays = *hazHor
		}
		if hazEv != nil {
			h.EventsInTrain = *hazEv
		}
		if hazReason != nil {
			h.NullReason = *hazReason
		}
		s.Hazard = h
	}
	return &s, nil
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Payload — payload для Render(kind='object_snapshot').
func (s *ObjectSnapshot) Payload() map[string]any {
	p := map[string]any{
		"object_id":         s.ID,
		"country":           s.Country,
		"deal_type":         s.DealType,
		"status":            s.Status,
		"price_minor":       s.PriceMinor,
		"currency":          s.Currency,
		"currency_exponent": s.CurrExponent,
		"days_on_market":    s.DaysOnMarket,
	}
	if v := s.Valuation; v != nil {
		p["valuation"] = map[string]any{
			"deviation_pct":       v.DeviationPct,
			"deviation_reason":    v.DeviationReason,
			"interval_low_minor":  v.IntervalLow,
			"interval_high_minor": v.IntervalHigh,
			"sample_size":         v.SampleSize,
			"r_squared":           v.RSquared,
			"zone_fallback":       v.ZoneFallback,
			"model_version":       v.ModelVersion,
		}
	} else {
		p["valuation"] = nil
	}
	if h := s.Hazard; h != nil {
		p["hazard"] = map[string]any{
			"probability":        h.Probability,
			"null_reason":        h.NullReason,
			"horizon_days":       h.HorizonDays,
			"model_version":      h.ModelVersion,
			"events_in_training": h.EventsInTrain,
		}
	} else {
		p["hazard"] = nil
	}
	return p
}
