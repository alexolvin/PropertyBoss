package valuation

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/config"
)

// ModelVersion — версия модели (-valuations.model_version; параметры
// прогона видны в deviation_null_reason и в отчёте).
const ModelVersion = "hedonic-ridge-v1"

// RunReport — результат прогона оценки для (страна, тип сделки).
type RunReport struct {
	Country          string
	DealType         string
	TotalActive      int // активных объектов с ценой в валюте рынка
	ExcludedCurrency int // объектов без валюты или с валютой, отличной от рыночной
	InSample         int // с ценой и площадью
	Rejected         bool
	Reason           string
	ModelVersion     string
	SampleSize       int
	Params           int
	Lambda           float64
	RSquared         float64
	Valued           int // строк с отклонением, не NULL
	Nulls            int // строк с NULL (с причиной)
	Wrote            int // строк, записано в valuations
}

// Run — оценивает все активные объекты (страна, тип сделки): строит
// модель (или честно отклоняет по правилам ТЗ §7.3) и записывает строки
// valuations — по одной на объект. NULL с причиной — тоже результат
// (ТЗ §7.3: «не выдаёт результат, а возвращает NULL + причину»).
func Run(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, country, dealType string) (*RunReport, error) {
	country = strings.ToUpper(strings.TrimSpace(country))
	dealType = strings.TrimSpace(dealType)
	if len(country) != 2 {
		return nil, fmt.Errorf("valuation: country: код из двух букв, получено %q", country)
	}
	marketCur, ok := cfg.Dashboard.MarketCurrencies[country]
	if !ok {
		return nil, fmt.Errorf("valuation: страна %s не в dashboard.countries — модель строится отдельно для каждой страны (ТЗ §7.2)", country)
	}
	dtOK := false
	for _, dt := range cfg.Dashboard.DealTypes {
		if dt == dealType {
			dtOK = true
			break
		}
	}
	if !dtOK {
		return nil, fmt.Errorf("valuation: deal_type %q не в dashboard.deal_types", dealType)
	}
	v := &cfg.Valuation

	rep := &RunReport{Country: country, DealType: dealType, ModelVersion: ModelVersion}

	// Активные объекты страны/типа сделки.
	// currency — *string: в objects.currency бывают NULL (столбец
	// nullable); NULL → честная причина no_currency, а не падение скана.
	type objRow struct {
		id         int64
		currency   *string
		priceMinor *int64
		areaSQM    *float64
		zoneID     *int64
		month      int
		attrsRaw   []byte
	}
	rows, err := pool.Query(ctx, `
		SELECT o.id, o.currency, o.current_price_minor, o.area_sqm, o.zone_id,
		       to_char(o.last_seen_at AT TIME ZONE 'UTC', 'MM')::int,
		       o.attributes
		FROM objects o
		WHERE o.country = $1 AND o.deal_type = $2 AND o.status = 'active'
		ORDER BY o.id`, country, dealType)
	if err != nil {
		return nil, fmt.Errorf("valuation: загрузка объектов: %w", err)
	}
	var objs []objRow
	zoneIDs := []int64{}
	for rows.Next() {
		var o objRow
		if err := rows.Scan(&o.id, &o.currency, &o.priceMinor, &o.areaSQM, &o.zoneID, &o.month, &o.attrsRaw); err != nil {
			rows.Close()
			return nil, fmt.Errorf("valuation: чтение объектов: %w", err)
		}
		if o.zoneID != nil {
			zoneIDs = append(zoneIDs, *o.zoneID)
		}
		objs = append(objs, o)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("valuation: чтение объектов: %w", err)
	}
	rows.Close()

	// Реестр ценовых атрибутов страны (ТЗ §7.2: one-hot — из
	// attribute_registry, а не из захардкоженного списка).
	type regRow struct {
		key      string
		dataType string
		allowed  []byte
	}
	rrows, err := pool.Query(ctx, `
		SELECT key, data_type, allowed_values
		FROM attribute_registry
		WHERE country = $1 AND used_in_pricing = true
		ORDER BY key`, country)
	if err != nil {
		return nil, fmt.Errorf("valuation: реестр атрибутов: %w", err)
	}
	var reg []regRow
	for rrows.Next() {
		var r regRow
		if err := rrows.Scan(&r.key, &r.dataType, &r.allowed); err != nil {
			rrows.Close()
			return nil, fmt.Errorf("valuation: чтение реестра атрибутов: %w", err)
		}
		reg = append(reg, r)
	}
	if err := rrows.Err(); err != nil {
		rrows.Close()
		return nil, fmt.Errorf("valuation: чтение реестра атрибутов: %w", err)
	}
	rrows.Close()

	attrs := []AttrSpec{}
	for _, r := range reg {
		switch r.dataType {
		case "bool":
			vals := []string{}
			if len(r.allowed) > 0 && string(r.allowed) != "null" && string(r.allowed) != "[]" {
				if err := json.Unmarshal(r.allowed, &vals); err != nil {
					return nil, fmt.Errorf("valuation: allowed_values атрибута %s: %w", r.key, err)
				}
			}
			if len(vals) == 0 {
				vals = []string{"false", "true"}
			}
			attrs = append(attrs, AttrSpec{Key: r.key, Kind: "onehot", Values: vals})
		case "enum":
			vals := []string{}
			if len(r.allowed) > 0 && string(r.allowed) != "null" {
				if err := json.Unmarshal(r.allowed, &vals); err != nil {
					return nil, fmt.Errorf("valuation: allowed_values атрибута %s: %w", r.key, err)
				}
			}
			if len(vals) == 0 {
				// ТЗ §0.2: enum без allowed_values — one-hot не построить,
				// молча пропускать атрибут нельзя.
				return nil, fmt.Errorf("valuation: атрибут %s (enum) без allowed_values — реестр неполон", r.key)
			}
			attrs = append(attrs, AttrSpec{Key: r.key, Kind: "onehot", Values: vals})
		case "int", "float":
			attrs = append(attrs, AttrSpec{Key: r.key, Kind: "numeric"})
		default:
			return nil, fmt.Errorf("valuation: неизвестный data_type %q атрибута %s (реестр)", r.dataType, r.key)
		}
	}

	// Родители зон (для zone_fallback, ТЗ §7.3).
	zoneParent := map[int64]*int64{}
	if len(zoneIDs) > 0 {
		zrows, err := pool.Query(ctx, `SELECT id, parent_id FROM zones WHERE id = ANY($1)`, zoneIDs)
		if err != nil {
			return nil, fmt.Errorf("valuation: родители зон: %w", err)
		}
		for zrows.Next() {
			var id int64
			var parent *int64
			if err := zrows.Scan(&id, &parent); err != nil {
				zrows.Close()
				return nil, fmt.Errorf("valuation: чтение родителей зон: %w", err)
			}
			zoneParent[id] = parent
		}
		if err := zrows.Err(); err != nil {
			zrows.Close()
			return nil, fmt.Errorf("valuation: чтение родителей зон: %w", err)
		}
		zrows.Close()
	}

	// Выборка (цена и площадь) и причины NULL для остального.
	// Порядок причин: цена → валюта → площадь. Цена — зависимая
	// переменная модели: если её нет, no_price — главная причина,
	// даже если валюта тоже отсутствует.
	var sample []Observation
	nullReason := map[int64]string{}
	for i := range objs {
		o := &objs[i]
		if o.priceMinor == nil || *o.priceMinor <= 0 {
			nullReason[o.id] = "no_price"
			continue
		}
		if o.currency == nil {
			rep.ExcludedCurrency++
			nullReason[o.id] = "no_currency"
			continue
		}
		if *o.currency != marketCur {
			rep.ExcludedCurrency++
			nullReason[o.id] = "currency_mismatch"
			continue
		}
		rep.TotalActive++
		if o.areaSQM == nil || *o.areaSQM <= 0 {
			nullReason[o.id] = "no_area"
			continue
		}
		var attrsMap map[string]string
		if len(o.attrsRaw) > 0 {
			var raw map[string]any
			if err := json.Unmarshal(o.attrsRaw, &raw); err != nil {
				return nil, fmt.Errorf("valuation: attributes объекта %d: %w", o.id, err)
			}
			attrsMap = make(map[string]string, len(raw))
			for k, val := range raw {
				attrsMap[k] = normAttrValue(val)
			}
		}
		sample = append(sample, Observation{
			ObjectID:   o.id,
			PriceMinor: *o.priceMinor,
			AreaSQM:    *o.areaSQM,
			ZoneID:     o.zoneID,
			AttrValues: attrsMap,
			Month:      o.month,
		})
	}
	rep.InSample = len(sample)

	// Модель (ТЗ §7.2–7.3): или строится, или честно отклоняется.
	fit, err := Fit(&ModelInput{
		MinObsPerParam: v.MinObsPerParam,
		MinObsPerZone:  v.MinObsPerZone,
		MaxMissingRate: v.MaxMissingRate,
		KFold:          v.KFold,
		LambdaGrid:     v.LambdaGrid,
		Attrs:          attrs,
		Observations:   sample,
		ZoneParent:     zoneParent,
	})
	if err != nil {
		return nil, fmt.Errorf("valuation: модель: %w", err)
	}
	rep.Rejected = fit.Rejected
	rep.Reason = fit.Reason
	rep.SampleSize = fit.SampleSize
	rep.Params = fit.Params
	rep.Lambda = fit.Lambda
	rep.RSquared = fit.RSquared

	// Строки valuations: по одной на каждый активный объект.
	var vals []valRow
	if fit.Rejected {
		for _, s := range sample {
			vals = append(vals, valRow{objectID: s.ObjectID, nullReason: fit.Reason, sampleSize: fit.SampleSize})
		}
	} else {
		for _, s := range sample {
			p := fit.Predictions[s.ObjectID]
			vr := valRow{objectID: s.ObjectID, sampleSize: fit.SampleSize, zoneFB: p.ZoneFallback}
			if p.PriceDeviation != nil {
				vr.deviation = p.PriceDeviation
				pr, lo, hi := p.PredictedMinor, p.IntervalLowMinor, p.IntervalHighMinor
				r2v := fit.RSquared
				vr.predicted, vr.intLow, vr.intHigh, vr.r2 = &pr, &lo, &hi, &r2v
				rep.Valued++
			} else {
				vr.nullReason = p.NullReason
				if vr.nullReason == "" {
					vr.nullReason = "unknown"
				}
				rep.Nulls++
			}
			vals = append(vals, vr)
		}
	}
	for id, reason := range nullReason {
		vals = append(vals, valRow{objectID: id, nullReason: reason, sampleSize: fit.SampleSize})
		rep.Nulls++
	}

	at := time.Now().UTC()
	err = withTx(ctx, pool, func(tx pgx.Tx) error {
		n, err := insertVals(ctx, tx, ModelVersion, at, vals)
		if err != nil {
			return err
		}
		rep.Wrote = n
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("valuation: запись valuations: %w", err)
	}
	return rep, nil
}

// valRow — строка valuations в процессе записи.
type valRow struct {
	objectID   int64
	deviation  *float64
	nullReason string
	predicted  *int64
	intLow     *int64
	intHigh    *int64
	sampleSize int
	r2         *float64
	zoneFB     bool
}

// insertVals — вставка строк valuations пачками по 100.
func insertVals(ctx context.Context, tx pgx.Tx, modelVersion string, at time.Time, vals []valRow) (int, error) {
	if len(vals) == 0 {
		return 0, nil
	}
	const batchSize = 100
	written := 0
	const cols = "(object_id, model_version, price_deviation, deviation_null_reason, predicted_price_minor, interval_low_minor, interval_high_minor, sample_size, r_squared, zone_fallback, computed_at)"
	for start := 0; start < len(vals); start += batchSize {
		end := start + batchSize
		if end > len(vals) {
			end = len(vals)
		}
		chunk := vals[start:end]
		ph := make([]string, len(chunk))
		args := make([]any, 0, len(chunk)*11)
		for i, v := range chunk {
			base := i * 11
			ph[i] = fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
				base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10, base+11)
			var nr *string
			if v.nullReason != "" {
				nr = &v.nullReason
			}
			args = append(args,
				v.objectID, modelVersion, v.deviation, nr,
				v.predicted, v.intLow, v.intHigh,
				v.sampleSize, v.r2, v.zoneFB, at)
		}
		q := "INSERT INTO valuations " + cols + " VALUES " + strings.Join(ph, ",")
		if _, err := tx.Exec(ctx, q, args...); err != nil {
			return written, err
		}
		written += len(chunk)
	}
	return written, nil
}

// withTx — транзакция: begin → fn → commit; при ошибке fn — откат.
func withTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("valuation: tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// normAttrValue — значение атрибута из JSONB в строку (формат one-hot
// реестра: строковые enum, "true"/"false" для bool).
func normAttrValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	default:
		return fmt.Sprint(t)
	}
}
