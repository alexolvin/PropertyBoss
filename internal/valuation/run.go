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

// ModelVersion — версия модели (valuations.model_version; параметры
// прогона видны в deviation_null_reason и в отчёте).
// v2 — правила ТЗ v4-2 §4.10: обязательный набор (цена/площадь/зона/тип)
// как условие наблюдения, пропуски опциональных признаков — уровень
// «не указано» / индикатор, а не импутация и не причина отказа.
// v3 (коррекция C1) — штраф блока зон: иерархическое сжатие к
// родительскому уровню (ANOVA-центрирование внутри группы родителя),
// а не к нулю; λ — k-fold CV по сетке, CV-MSE по каждому λ публикуется.
// v4 (C10/C16, ТЗ v4-2 §4.7) — уровень геокода (objects.geo_confidence)
// читается моделью как признак: onehot по словарю уровней + «не
// указано»; однородная выборка — признак не добавляется (нулевая
// информация, то же правило, что тип). Доля «не указано» —
// MissingRates["geo_level"] (публикуется всегда).
// v5 (C20/C21) — корпус модели: property_country (страна объекта),
// а не объявления площадки (иностранные заглушки исключены); проверка
// матрицы подгонки — ранг и число обусловленности (XᵀX + λP) до
// принятия подгонки, отказ по порогу max_mat_cond (degenerate_matrix):
// gonum Solve не ошибается на вырожденной системе (D16).
const ModelVersion = "hedonic-ridge-v5"

// RunReport — результат прогона оценки для (страна, тип сделки).
type RunReport struct {
	Country          string
	DealType         string
	TotalActive      int // активных объектов с ценой в валюте рынка
	ExcludedCurrency int // объектов без валюты или с валютой, отличной от рыночной
	InSample         int // с полным обязательным набором признаков
	Rejected         bool
	Reason           string
	ModelVersion     string
	SampleSize       int
	Params           int
	Lambda           float64
	RSquared         float64
	// LambdaGrid / LambdaCVMSE — сетка λ и CV-MSE по каждому её значению
	// (порядок совпадает); публикуется в отчёте: выбор λ прозрачен
	// (ТЗ §7.2, C1).
	LambdaGrid  []float64
	LambdaCVMSE []float64
	// MatRank / MatCond (C21): ранг и число обусловленности матрицы
	// подгонки (XᵀX + λP) при выбранном λ; публикуется с показателями
	// качества (0/0 — модель отклонена до построения).
	MatRank int
	MatCond float64
	Valued  int // строк с отклонением, не NULL
	Nulls   int // строк с NULL (с причиной)
	Wrote   int // строк, записано в valuations
	// RequiredCoverage — покрытие обязательного набора (price/area/zone/
	// type) по всем активным объектам (ТЗ v4-2 §4.10.1, §11.1: знаменатель
	// — применимые объекты; в текущем корпусе все объекты flat, все четыре
	// признака применимы ко всем).
	RequiredCoverage map[string]float64
	// MissingRates — доли пропусков опциональных признаков в выборке
	// (ТЗ v4-2 §4.10.5: публикуются с показателями качества).
	MissingRates map[string]float64
	// ExcludedAttrs — опциональные признаки, исключённые из модели
	// (режим exclude, доля пропусков выше порога).
	ExcludedAttrs []string
	// Corpus — активных объектов рынка (знаменатель долей покрытия;
	// публикуется: сравнение долей между прогонами без знаменателя
	// некорректно, C24).
	Corpus int
	// NotModeled (C25): объектов без уровня геокода (geo_confidence
	// IS NULL) — структурно не моделируемы (нет зоны и гео-признака):
	// несут флаг not_modeled и исключены из публикуемых оценок.
	// Доля публикуется всегда; превышение порога — дефект.
	NotModeled int
	// NotModeledShare — NotModeled / Corpus.
	NotModeledShare float64
	// NotModeledExceeds — доля выше valuation.max_not_modeled_share
	// (C25): дефект, требует разбора.
	NotModeledExceeds bool
	// SellerLevels (C26): распределение уровня продавца в корпусе
	// (agency/private/developer/unknown) — публикуется вместе с
	// коэффициентами seller_type: «unknown» — полноценный уровень
	// модели, а не пропуск.
	SellerLevels map[string]int
	// SellerCoeffs (C26): коэффициенты onehot-уровней seller_type
	// (log цены/м²) по именам уровней; пусто, когда модель отклонена
	// или признак не в модели.
	SellerCoeffs map[string]float64
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
		id           int64
		currency     *string
		priceMinor   *int64
		areaSQM      *float64
		zoneID       *int64
		month        int
		propertyType string
		geoConf      *string        // objects.geo_confidence: уровень геокода (C16)
		sellerType   *string        // objects.seller_type: уровень продавца (C26)
		rawAttrs     map[string]any // objects.attributes, распарсенный (может быть nil)
	}
	rows, err := pool.Query(ctx, `
		SELECT o.id, o.currency, o.current_price_minor, o.area_sqm, o.zone_id,
		       to_char(o.last_seen_at AT TIME ZONE 'UTC', 'MM')::int,
		       COALESCE(o.property_type, ''), o.geo_confidence, o.seller_type, o.attributes
		FROM objects o
		-- C20: корпус модели — объекты рынка, а не объявления рынка:
		-- иностранные объявления-заглушки на чешской площадке
		-- («Zahraničí»/«Slovensko», 480 объектов на двух страновых
		-- точках-заглушках) имеют property_country XX/SK и искажают
		-- уровни цен и эффекты зон — исключены.
		WHERE o.country = $1 AND o.property_country = $1
		  AND o.deal_type = $2 AND o.status = 'active'
		ORDER BY o.id`, country, dealType)
	if err != nil {
		return nil, fmt.Errorf("valuation: загрузка объектов: %w", err)
	}
	var objs []objRow
	zoneIDs := []int64{}
	for rows.Next() {
		var (
			o   objRow
			raw []byte
		)
		if err := rows.Scan(&o.id, &o.currency, &o.priceMinor, &o.areaSQM, &o.zoneID, &o.month, &o.propertyType, &o.geoConf, &o.sellerType, &raw); err != nil {
			rows.Close()
			return nil, fmt.Errorf("valuation: чтение объектов: %w", err)
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &o.rawAttrs); err != nil {
				rows.Close()
				return nil, fmt.Errorf("valuation: attributes объекта %d: %w", o.id, err)
			}
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

	// C25: not_modeled — объекты без уровня геокода: структурно не
	// моделируемы (нет зоны и гео-признака). Флаг выводимый из
	// geo_confidence (единый источник — без зеркальной колонки, урок
	// C14); доля публикуется всегда, превышение порога — дефект.
	rep.Corpus = len(objs)
	notModeled := 0
	sellerLevels := map[string]int{}
	for i := range objs {
		if objs[i].geoConf == nil {
			notModeled++
		}
		// C26: уровень продавца; «unknown» — полноценный уровень
		// модели, а не пропуск.
		lev := "unknown"
		if objs[i].sellerType != nil && *objs[i].sellerType != "" {
			lev = *objs[i].sellerType
		}
		sellerLevels[lev]++
	}
	rep.NotModeled = notModeled
	if len(objs) > 0 {
		rep.NotModeledShare = float64(notModeled) / float64(len(objs))
	}
	if rep.NotModeledShare > v.MaxNotModeledShare {
		rep.NotModeledExceeds = true
	}
	rep.SellerLevels = sellerLevels

	// Порог уверенности (ТЗ v4 §4.3, правило раздела 6 основного ТЗ):
	// значение с уверенностью ниже valuation.min_confidence не участвует
	// в оценке коэффициентов. Уверенность — из attribute_values; атрибут
	// без строки provenance считается пригодным (источник —
	// структурированное поле площадки/правило, уверенность ≥ порога).
	lowConf := map[int64]map[string]bool{}
	if v.MinConfidence > 0 && len(objs) > 0 {
		ids := make([]int64, len(objs))
		for i := range objs {
			ids[i] = objs[i].id
		}
		crows, err := pool.Query(ctx, `
			SELECT object_id, attr_key FROM attribute_values
			WHERE object_id = ANY($1) AND confidence < $2`, ids, v.MinConfidence)
		if err != nil {
			return nil, fmt.Errorf("valuation: уверенность атрибутов: %w", err)
		}
		for crows.Next() {
			var oid int64
			var key string
			if err := crows.Scan(&oid, &key); err != nil {
				crows.Close()
				return nil, fmt.Errorf("valuation: чтение уверенности: %w", err)
			}
			if lowConf[oid] == nil {
				lowConf[oid] = map[string]bool{}
			}
			lowConf[oid][key] = true
		}
		if err := crows.Err(); err != nil {
			crows.Close()
			return nil, fmt.Errorf("valuation: уверенность атрибутов: %w", err)
		}
		crows.Close()
	}

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

	// Выборка и причины NULL. Обязательный набор модели (ТЗ v4-2
	// §4.10.1): цена, площадь, зона, тип недвижимости — отсутствие
	// любого из них делает наблюдение непригодным. Порядок причин:
	// цена → валюта → площадь → зона → тип. Цена — зависимая переменная
	// модели: если её нет, no_price — главная причина, даже если валюта
	// тоже отсутствует.
	//
	// Покрытие обязательного набора считается по ВСЕМ активным объектам
	// (знаменатель — применимые объекты, ТЗ v4-2 §11.1; в текущем корпусе
	// все объекты flat и все четыре признака применимы ко всем).
	var sample []Observation
	nullReason := map[int64]string{}
	cov := map[string]int{"price": 0, "area": 0, "zone": 0, "type": 0}
	for i := range objs {
		o := &objs[i]
		if o.priceMinor != nil && *o.priceMinor > 0 {
			cov["price"]++
		}
		if _, ok := knownArea(o.areaSQM, o.rawAttrs); ok {
			cov["area"]++
		}
		if o.zoneID != nil {
			cov["zone"]++
		}
		if o.propertyType != "" {
			cov["type"]++
		}

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
		area, areaOK := knownArea(o.areaSQM, o.rawAttrs)
		if !areaOK {
			nullReason[o.id] = "no_area"
			continue
		}
		if o.zoneID == nil {
			nullReason[o.id] = "no_zone"
			continue
		}
		if o.propertyType == "" {
			nullReason[o.id] = "no_type"
			continue
		}
		attrsMap := make(map[string]string, len(o.rawAttrs))
		for k, val := range o.rawAttrs {
			if lowConf[o.id][k] {
				// Уверенность ниже порога — не в модель (ТЗ v4 §4.3);
				// в модели это пропуск, кодируемый уровнем «не указано».
				continue
			}
			attrsMap[k] = normAttrValue(val)
		}
		geoLevel := ""
		if o.geoConf != nil {
			geoLevel = *o.geoConf
		}
		sample = append(sample, Observation{
			ObjectID:     o.id,
			PriceMinor:   *o.priceMinor,
			AreaSQM:      area,
			ZoneID:       o.zoneID,
			PropertyType: o.propertyType,
			AttrValues:   attrsMap,
			Month:        o.month,
			GeoLevel:     geoLevel, // C16: хранимое поле, модель читает как признак
		})
	}
	rep.InSample = len(sample)

	// ТЗ v4-2 §4.10.1: порог покрытия сохраняется для обязательного
	// набора — ниже порога модель не публикуется (качество, а не
	// пропуски опциональных признаков).
	reqCov := map[string]float64{}
	if len(objs) > 0 {
		for f, n := range cov {
			reqCov[f] = float64(n) / float64(len(objs))
		}
	}
	rep.RequiredCoverage = reqCov
	var reqFail []string
	for _, f := range []string{"price", "area", "zone", "type"} {
		if reqCov[f] < v.MaxMissingRate {
			reqFail = append(reqFail, fmt.Sprintf("%s=%.2f", f, reqCov[f]))
		}
	}

	// Модель (ТЗ §7.2–7.3, v4-2 §4.10): либо строится, либо честно
	// отклоняется. Покрытие обязательного набора ниже порога — отказ
	// до построения (наблюдений с полным набором недостаточно).
	var fit *FitResult
	if len(reqFail) > 0 {
		fit = &FitResult{
			Rejected:   true,
			Reason:     fmt.Sprintf("required_coverage: %s ниже порога %.2f (ТЗ v4-2 §4.10.1)", strings.Join(reqFail, ", "), v.MaxMissingRate),
			SampleSize: len(sample),
		}
	} else {
		f, err := Fit(&ModelInput{
			MinObsPerParam:      v.MinObsPerParam,
			MinObsPerZone:       v.MinObsPerZone,
			MaxMissingRate:      v.MaxMissingRate,
			KFold:               v.KFold,
			LambdaGrid:          v.LambdaGrid,
			MaxMatCond:          v.MaxMatCond, // C21: порог обусловленности
			Attrs:               attrs,
			OptionalMissing:     v.OptionalMissing,
			OptionalExcludeRate: v.OptionalExcludeRate,
			Observations:        sample,
			ZoneParent:          zoneParent,
		})
		if err != nil {
			return nil, fmt.Errorf("valuation: модель: %w", err)
		}
		fit = f
	}
	rep.Rejected = fit.Rejected
	rep.Reason = fit.Reason
	rep.SampleSize = fit.SampleSize
	rep.Params = fit.Params
	rep.Lambda = fit.Lambda
	rep.RSquared = fit.RSquared
	rep.LambdaGrid = v.LambdaGrid
	rep.LambdaCVMSE = fit.LambdaCVMSE
	rep.MatRank, rep.MatCond = fit.MatRank, fit.MatCond
	rep.MissingRates = fit.MissingRates
	rep.ExcludedAttrs = fit.ExcludedAttrs

	// C26: коэффициенты seller_type по уровням — публикуются вместе
	// с долями уровней (SellerLevels выше). Пусто, когда модель
	// отклонена до построения или признак не вошёл в модель.
	if !fit.Rejected && len(fit.ParamNames) == len(fit.Coeffs) {
		for i, name := range fit.ParamNames {
			if lvl, ok := strings.CutPrefix(name, "attr:seller_type="); ok {
				if rep.SellerCoeffs == nil {
					rep.SellerCoeffs = map[string]float64{}
				}
				rep.SellerCoeffs[lvl] = fit.Coeffs[i]
			}
		}
	}

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

// knownArea — площадь объекта: зеркальная колонка (источник —
// структурированное поле площадки, шаг 1 цепочки ТЗ v4 §4.2) либо
// значение attributes->'area_sqm' (шаги 2–4: правила, шаблоны, LLM).
// Первое найденное — приоритет шагов цепочки. > 0 — известна.
func knownArea(areaSQM *float64, rawAttrs map[string]any) (float64, bool) {
	if areaSQM != nil && *areaSQM > 0 {
		return *areaSQM, true
	}
	if val := rawAttrs["area_sqm"]; val != nil { // чтение из nil-карты безопасно
		if f, ok := parseFloatAttr(normAttrValue(val)); ok && f > 0 {
			return f, true
		}
	}
	return 0, false
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
