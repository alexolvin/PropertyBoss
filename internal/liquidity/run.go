// run.go — прогон pb liquidity: загрузка → гейт min_events →
// временное разбиение → фит → валидация (ТЗ §9.4) → запись версии
// модели и прогнозов.
//
// Все записи (liquidity_models + liquidity_estimates) — в ОДНОЙ
// транзакции (ТЗ §3.4): сбой обучения не должен оставлять
// «полностью обновлённую» модель.
package liquidity

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

// minTestIntervals — минимум person-period строк во валидационном
// окне: меньше — метрики шум, а не сигнал. Допущение исполнителя: 20.
const minTestIntervals = 20

// minDeciles — минимум непустых децилей калибровочной кривой:
// меньше — «максимальное отклонение» не имеет смысла.
// Допущение исполнителя: 2.
const minDeciles = 2

// estRow — строка liquidity_estimates в процессе записи.
type estRow struct {
	objectID int64
	hazard   *float64 // nil — NULL с причиной
	reason   string
}

// RunReport — результат прогона ликвидности для (страна, тип сделки).
type RunReport struct {
	Country         string
	DealType        string
	ModelVersion    string
	Status          string // published | uncalibrated | insufficient_history
	RejectReason    string // причина при status != 'published'
	ComputedAt      time.Time
	HorizonDays     int
	MinEvents       int
	Objects         int // объектов в выборке (все статусы)
	CompletedEvents int // объектов, ушедших с рынка (надёжный старт)
	NPeriods        int // person-period строк
	Params          int
	TrainCutoff     time.Time // T: обучение до, проверка после
	NTrain          int
	NTest           int
	TrainEvents     int // завершённых событий в обучающей части
	MaxCalibDev     *float64
	Brier           *float64
	BrierDecomp     *BrierDecomp
	CIndex          *float64
	Calibration     []CalibDecile // кривая (для строки модели)
	Estimated       int           // активных с прогнозом, не NULL
	Nulls           int           // прогнозов NULL с причиной
	Wrote           int           // строк liquidity_estimates записано
}

// Run — прогон модели ликвидности для одной (страна, тип сделки) (ТЗ §9).
// Модель публикуется только при проходе калибровочного гейта; иначе
// прогнозы NULL с причиной, а в liquidity_models — строка истории
// прогона со статусом и метриками (ТЗ §9.4).
func Run(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, country, dealType string) (*RunReport, error) {
	country = strings.ToUpper(strings.TrimSpace(country))
	dealType = strings.TrimSpace(dealType)
	if len(country) != 2 {
		return nil, fmt.Errorf("liquidity: country: код из двух букв, получено %q", country)
	}
	if _, ok := cfg.Dashboard.MarketCurrencies[country]; !ok {
		return nil, fmt.Errorf("liquidity: страна %s не в dashboard.countries — модель строится отдельно для каждой страны (ТЗ §7.2, §9)", country)
	}
	dtOK := false
	for _, dt := range cfg.Dashboard.DealTypes {
		if dt == dealType {
			dtOK = true
			break
		}
	}
	if !dtOK {
		return nil, fmt.Errorf("liquidity: deal_type %q не в dashboard.deal_types", dealType)
	}
	q := &cfg.Liquidity
	at := time.Now().UTC()

	rep := &RunReport{
		Country: country, DealType: dealType,
		ModelVersion: "liq-discrete-v1-" + at.Format("20060102-1504"),
		Status:       "published",
		ComputedAt:   at,
		HorizonDays:  q.HorizonDays, MinEvents: q.MinEvents,
	}

	// Гейт min_events (ТЗ §9.3): завершённые наблюдения — объекты,
	// ушедшие с рынка и с надёжным началом экспозиции (ненадёжные
	// исключены из обучения, ТЗ §14.5.1). Дешёвый подсчёт: полная
	// выборка строится, только если гейт пройден.
	var nEvents int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM objects
		WHERE country = $1 AND deal_type = $2
		  AND status = 'delisted' AND posted_date_unreliable = FALSE`,
		country, dealType).Scan(&nEvents); err != nil {
		return nil, fmt.Errorf("liquidity: подсчёт завершённых событий: %w", err)
	}
	rep.CompletedEvents = nEvents

	status, reject := "published", ""
	if nEvents < q.MinEvents {
		// Холодный старт (ТЗ §14.5): первые недели модель честно
		// возвращает NULL — нормальное состояние, а не сбой.
		status, reject = "insufficient_history", ""
	}

	// Реестр атрибутов страны (ТЗ §9.2 — «атрибуты из реестра»;
	// кодирование то же, что у гедонической модели, этап 5).
	attrs, err := loadAttrSpecs(ctx, pool, country)
	if err != nil {
		return nil, err
	}

	// Выборка, разбиение, фит, валидация — только после гейта.
	var ds *Dataset
	var fitModel *Model
	var params map[string]float64
	if status == "published" {
		ds, err = loadDataset(ctx, pool, country, dealType, at)
		if err != nil {
			return nil, err
		}
		rep.Objects = len(ds.Objects)
		rep.NPeriods = len(ds.Periods)

		// Временное разбиение (ТЗ §9.4: случайное запрещено):
		// T = start + holdout_ratio × (end − start) по окну
		// наблюдений; интервалы с End ≤ T — обучение, после — проверка.
		minStart, maxEnd := ds.Objects[0].Start, ds.Objects[0].End
		for _, o := range ds.Objects[1:] {
			if o.Start.Before(minStart) {
				minStart = o.Start
			}
			if o.End.After(maxEnd) {
				maxEnd = o.End
			}
		}
		cutoff := minStart.Add(time.Duration(float64(maxEnd.Sub(minStart)) * q.HoldoutRatio))
		rep.TrainCutoff = cutoff

		var trainIdx, testIdx []int
		for i, p := range ds.Periods {
			if ds.Objects[p.Obj].Unreliable {
				continue // ТЗ §14.5.1: ненадёжный старт — вне обучения
			}
			if !p.End.After(cutoff) {
				trainIdx = append(trainIdx, i)
			} else {
				testIdx = append(testIdx, i)
			}
		}
		rep.NTrain, rep.NTest = len(trainIdx), len(testIdx)
		for _, i := range trainIdx {
			rep.TrainEvents += ds.Periods[i].Target
		}

		switch {
		case rep.TrainEvents == 0:
			// Все уходы — после T: обучать не на чем.
			status, reject = "uncalibrated", "no_events_in_train"
		case len(testIdx) < minTestIntervals:
			status, reject = "uncalibrated", "test_too_small"
		default:
			// Зоны — только из обучающих строк: тест/прогноз видят
			// тот же набор индикаторов (новые зоны — все нули).
			zones := make([]int64, 0)
			zoneSet := make(map[int64]bool)
			for _, i := range trainIdx {
				if z := ds.Objects[ds.Periods[i].Obj].ZoneID; z != nil && !zoneSet[*z] {
					zoneSet[*z] = true
					zones = append(zones, *z)
				}
			}
			fitModel = &Model{Names: colLayout(zones, attrs), Zones: zones, Attrs: attrs}
			x := make([][]float64, len(trainIdx))
			y := make([]int, len(trainIdx))
			for i, idx := range trainIdx {
				x[i] = fitModel.encode(periodFeatRow(ds, ds.Periods[idx]))
				y[i] = ds.Periods[idx].Target
			}
			beta, converged := fitLogistic(x, y)
			if !converged {
				status, reject = "uncalibrated", "fit_not_converged"
				break
			}
			fitModel.Beta = beta
			rep.Params = len(beta)
			params = make(map[string]float64, len(beta))
			for j, name := range fitModel.Names {
				params[name] = beta[j]
			}

			// Валидация на отложенном по времени хольдауте (ТЗ §9.4).
			pred := make([]float64, len(testIdx))
			ty := make([]int, len(testIdx))
			for i, idx := range testIdx {
				pred[i] = fitModel.predict(periodFeatRow(ds, ds.Periods[idx]))
				ty[i] = ds.Periods[idx].Target
			}
			deciles := CalibrationDeciles(pred, ty)
			rep.Calibration = deciles
			maxDev, _ := MaxCalibDev(deciles)
			decomp, brier := BrierDecompose(pred, ty)
			rep.MaxCalibDev = &maxDev
			rep.Brier = &brier
			d := decomp
			rep.BrierDecomp = &d
			if ci, comparable := CIndex(pred, ty); comparable > 0 {
				rep.CIndex = &ci
			}
			// Калибровочный гейт (ТЗ §9.4): публикация только при
			// max |predicted − actual| ≤ порога из конфига.
			nonEmpty := 0
			for _, c := range deciles {
				if c.N > 0 {
					nonEmpty++
				}
			}
			switch {
			case nonEmpty < minDeciles:
				status, reject = "uncalibrated", "calibration_too_few_deciles"
			case maxDev > q.MaxCalibDev:
				status, reject = "uncalibrated", "calibration_failed"
			}
		}
	}
	rep.Status, rep.RejectReason = status, reject

	// Прогнозы: все объекты страны/типа сделки.
	// delisted — NULL 'delisted' (объект уже ушёл с рынка, ТЗ §9.3);
	// active — значение (published) или NULL с причиной.
	var ests []estRow
	nullReason := ""
	switch status {
	case "insufficient_history":
		nullReason = "insufficient_history"
	case "uncalibrated":
		// ТЗ §9.4: «прогноз NULL с причиной calibration_failed».
		nullReason = "calibration_failed"
	}
	addEst := func(id int64, delisted bool, h *float64, reason string) {
		if delisted {
			// Для ушедшего объекта причина всегда 'delisted' —
			// объект уже не на рынке, прогноз не имеет смысла (ТЗ §9.3).
			reason = "delisted"
		}
		ests = append(ests, estRow{objectID: id, hazard: h, reason: reason})
		if h == nil {
			rep.Nulls++
		} else {
			rep.Estimated++
		}
	}
	if ds != nil {
		for _, o := range ds.Objects {
			if o.Status == "delisted" {
				addEst(o.ID, true, nil, nullReason)
				continue
			}
			if status != "published" {
				addEst(o.ID, false, nil, nullReason)
				continue
			}
			// Текущее состояние: предикторы цены на now (ТЗ §9.2),
			// week/month сдвигаются по шагам внутри horizonProb.
			fr := FeatRow{
				Week:   int(o.End.Sub(o.Start) / week),
				Month:  int(at.UTC().Month()),
				ZoneID: o.ZoneID, Attrs: o.Attrs,
			}
			fr.Reductions, fr.DropPct, fr.DaysSince, fr.Increased = priceFeaturesAt(o, at)
			fr.ValDev = valDevAt(o, at)
			h := fitModel.horizonProb(fr, at, q.HorizonDays)
			if h < 0 {
				h = 0
			} else if h > 1 {
				h = 1
			}
			addEst(o.ID, false, &h, "")
		}
	} else {
		// Гейт не пройден: история не строилась — берём id+status.
		ors, err := pool.Query(ctx, `
			SELECT id, status FROM objects
			WHERE country = $1 AND deal_type = $2 ORDER BY id`, country, dealType)
		if err != nil {
			return nil, fmt.Errorf("liquidity: объекты для прогнозов: %w", err)
		}
		for ors.Next() {
			var id int64
			var st string
			if err := ors.Scan(&id, &st); err != nil {
				ors.Close()
				return nil, fmt.Errorf("liquidity: чтение объектов: %w", err)
			}
			addEst(id, st == "delisted", nil, nullReason)
		}
		if err := ors.Err(); err != nil {
			ors.Close()
			return nil, fmt.Errorf("liquidity: чтение объектов: %w", err)
		}
		ors.Close()
	}

	// Запись: строка истории модели + все прогнозы — одна транзакция
	// (ТЗ §3.4).
	err = withTx(ctx, pool, func(tx pgx.Tx) error {
		if err := saveModelRow(ctx, tx, rep, params); err != nil {
			return err
		}
		n, err := upsertEstimates(ctx, tx, rep, at, ests)
		if err != nil {
			return err
		}
		rep.Wrote = n
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("liquidity: запись результатов: %w", err)
	}
	return rep, nil
}

// loadAttrSpecs — реестр атрибутов страны: ВСЕ зарегистрированные
// атрибуты (ТЗ §9.2 — «атрибуты из реестра»; фильтр used_in_pricing
// этапа 5 belonged к гедонической модели).
func loadAttrSpecs(ctx context.Context, pool *pgxpool.Pool, country string) ([]AttrSpec, error) {
	rows, err := pool.Query(ctx, `
		SELECT key, data_type, allowed_values
		FROM attribute_registry
		WHERE country = $1
		ORDER BY key`, country)
	if err != nil {
		return nil, fmt.Errorf("liquidity: реестр атрибутов: %w", err)
	}
	var out []AttrSpec
	for rows.Next() {
		var key, dataType string
		var allowed []byte
		if err := rows.Scan(&key, &dataType, &allowed); err != nil {
			rows.Close()
			return nil, fmt.Errorf("liquidity: чтение реестра атрибутов: %w", err)
		}
		switch dataType {
		case "bool":
			vals := []string{}
			if len(allowed) > 0 && string(allowed) != "null" && string(allowed) != "[]" {
				if err := json.Unmarshal(allowed, &vals); err != nil {
					rows.Close()
					return nil, fmt.Errorf("liquidity: allowed_values атрибута %s: %w", key, err)
				}
			}
			if len(vals) == 0 {
				vals = []string{"false", "true"}
			}
			out = append(out, AttrSpec{Key: key, Kind: "onehot", Values: vals})
		case "enum":
			vals := []string{}
			if len(allowed) > 0 && string(allowed) != "null" {
				if err := json.Unmarshal(allowed, &vals); err != nil {
					rows.Close()
					return nil, fmt.Errorf("liquidity: allowed_values атрибута %s: %w", key, err)
				}
			}
			if len(vals) == 0 {
				// ТЗ §0.2: enum без allowed_values one-hot не
				// построить, молча пропускать атрибут нельзя.
				rows.Close()
				return nil, fmt.Errorf("liquidity: атрибут %s (enum) без allowed_values — реестр неполон", key)
			}
			out = append(out, AttrSpec{Key: key, Kind: "onehot", Values: vals})
		case "int", "float":
			out = append(out, AttrSpec{Key: key, Kind: "numeric"})
		default:
			rows.Close()
			return nil, fmt.Errorf("liquidity: неизвестный data_type %q атрибута %s (реестр)", dataType, key)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("liquidity: чтение реестра атрибутов: %w", err)
	}
	rows.Close()
	return out, nil
}

// loadDataset — объекты всех статусов + история цен + valuations +
// старт экспозиции min(first_seen_at, min posted_at) (ТЗ §14.5.1).
func loadDataset(ctx context.Context, pool *pgxpool.Pool, country, dealType string, at time.Time) (*Dataset, error) {
	type objRow struct {
		id         int64
		status     string
		zoneID     *int64
		attrsRaw   []byte
		unreliable bool
		firstSeen  time.Time
		delistedAt *time.Time
		lastSeen   time.Time
	}
	rows, err := pool.Query(ctx, `
		SELECT o.id, o.status, o.zone_id, o.attributes, o.posted_date_unreliable,
		       o.first_seen_at, o.delisted_at, o.last_seen_at
		FROM objects o
		WHERE o.country = $1 AND o.deal_type = $2
		ORDER BY o.id`, country, dealType)
	if err != nil {
		return nil, fmt.Errorf("liquidity: загрузка объектов: %w", err)
	}
	var objs []objRow
	var ids []int64
	for rows.Next() {
		var o objRow
		if err := rows.Scan(&o.id, &o.status, &o.zoneID, &o.attrsRaw, &o.unreliable,
			&o.firstSeen, &o.delistedAt, &o.lastSeen); err != nil {
			rows.Close()
			return nil, fmt.Errorf("liquidity: чтение объектов: %w", err)
		}
		objs = append(objs, o)
		ids = append(ids, o.id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("liquidity: чтение объектов: %w", err)
	}
	rows.Close()
	if len(objs) == 0 {
		return &Dataset{}, nil
	}

	// Мин posted_at по ВСЕМ сырым наблюдениям (ТЗ §14.5.1):
	// object_listings — связка «объект ↔ (источник, external_id)»,
	// позже одного объекта в raw_listings несколько строк.
	minPosted := map[int64]time.Time{}
	prows, err := pool.Query(ctx, `
		SELECT ol.object_id, MIN(rl.posted_at)
		FROM object_listings ol
		JOIN raw_listings rl
		  ON rl.source_id = ol.source_id AND rl.external_id = ol.external_id
		WHERE ol.object_id = ANY($1) AND rl.posted_at IS NOT NULL
		GROUP BY ol.object_id`, ids)
	if err != nil {
		return nil, fmt.Errorf("liquidity: min posted_at: %w", err)
	}
	for prows.Next() {
		var id int64
		var mp time.Time
		if err := prows.Scan(&id, &mp); err != nil {
			prows.Close()
			return nil, fmt.Errorf("liquidity: чтение min posted_at: %w", err)
		}
		minPosted[id] = mp
	}
	if err := prows.Err(); err != nil {
		prows.Close()
		return nil, fmt.Errorf("liquidity: чтение min posted_at: %w", err)
	}
	prows.Close()

	// История цен (предикторы ТЗ §9.2 считаются на начале интервалов
	// только по строкам change_at <= t — внутри dataset.go).
	prices := map[int64][]PricePoint{}
	srows, err := pool.Query(ctx, `
		SELECT object_id, change_at, price_minor
		FROM price_history
		WHERE object_id = ANY($1)
		ORDER BY object_id, change_at`, ids)
	if err != nil {
		return nil, fmt.Errorf("liquidity: история цен: %w", err)
	}
	for srows.Next() {
		var id int64
		var pp PricePoint
		if err := srows.Scan(&id, &pp.At, &pp.Minor); err != nil {
			srows.Close()
			return nil, fmt.Errorf("liquidity: чтение истории цен: %w", err)
		}
		prices[id] = append(prices[id], pp)
	}
	if err := srows.Err(); err != nil {
		srows.Close()
		return nil, fmt.Errorf("liquidity: чтение истории цен: %w", err)
	}
	srows.Close()

	// valuations с price_deviation — предиктор price_deviation.
	valDevs := map[int64][]ValPoint{}
	vrows, err := pool.Query(ctx, `
		SELECT object_id, computed_at, price_deviation
		FROM valuations
		WHERE object_id = ANY($1) AND price_deviation IS NOT NULL
		ORDER BY object_id, computed_at`, ids)
	if err != nil {
		return nil, fmt.Errorf("liquidity: valuations: %w", err)
	}
	for vrows.Next() {
		var id int64
		var vp ValPoint
		if err := vrows.Scan(&id, &vp.At, &vp.Deviation); err != nil {
			vrows.Close()
			return nil, fmt.Errorf("liquidity: чтение valuations: %w", err)
		}
		valDevs[id] = append(valDevs[id], vp)
	}
	if err := vrows.Err(); err != nil {
		vrows.Close()
		return nil, fmt.Errorf("liquidity: чтение valuations: %w", err)
	}
	vrows.Close()

	out := &Dataset{Objects: make([]Obj, 0, len(objs))}
	for _, o := range objs {
		end := at
		if o.status == "delisted" {
			if o.delistedAt != nil {
				end = *o.delistedAt
			} else {
				end = o.lastSeen // защита от NULL delisted_at
			}
		}
		start := o.firstSeen
		if mp, ok := minPosted[o.id]; ok && mp.Before(start) {
			start = mp
		}
		attrsMap := map[string]string{}
		if len(o.attrsRaw) > 0 {
			var raw map[string]any
			if err := json.Unmarshal(o.attrsRaw, &raw); err != nil {
				return nil, fmt.Errorf("liquidity: attributes объекта %d: %w", o.id, err)
			}
			for k, val := range raw {
				attrsMap[k] = normAttrValue(val)
			}
		}
		out.Objects = append(out.Objects, Obj{
			ID: o.id, Status: o.status, Start: start, End: end,
			ZoneID: o.zoneID, Attrs: attrsMap, Unreliable: o.unreliable,
			Prices: prices[o.id], ValDevs: valDevs[o.id],
		})
	}
	return NewDataset(out.Objects), nil
}

// normAttrValue — значение атрибута из JSONB в строку (формат
// one-hot реестра: строковые enum, "true"/"false" для bool) —
// та же нормализация, что в этапе 5.
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

// periodFeatRow — строка признаков person-period интервала.
func periodFeatRow(ds *Dataset, p Period) FeatRow {
	o := ds.Objects[p.Obj]
	return FeatRow{
		Week: p.Week, Month: int(p.Start.UTC().Month()),
		Reductions: p.Reductions, DropPct: p.DropPct,
		DaysSince: p.DaysSince, Increased: p.Increased,
		ValDev: p.ValDev, ZoneID: o.ZoneID, Attrs: o.Attrs,
	}
}

// withTx — транзакция: begin → fn → commit; при ошибке fn — откат.
func withTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("liquidity: tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
