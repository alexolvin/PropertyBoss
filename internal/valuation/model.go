// Package valuation — гедоническая ценовая модель (ТЗ §7.2) с правилами
// отказа (ТЗ §7.3, изменено ТЗ v4-2 §4.10).
//
// ТЗ v4-2 §4.10 (переопределяет правило пропусков §7.3):
//   - порог покрытия (max_missing_rate) применяется ТОЛЬКО к обязательному
//     набору признаков: цена, площадь, зона, тип недвижимости. Отсутствие
//     любого из них делает наблюдение непригодным (NULL с причиной);
//     проверка покрытия по всем активным объектам — в Run, отказ модели —
//     required_coverage.
//   - необязательный признак с высокой долей пропусков модель НЕ отклоняет:
//     режим level (по умолчанию, рекомендован ТЗ — пропуск несёт сведения)
//     кодирует пропуск явным уровнем: onehot — дополнительная колонка
//     «не указано», числовой признак — колонка значения + индикатор пропуска
//     (значение не подставляется — импутация запрещена, §4.10.4);
//     режим exclude исключает признак из модели, если доля пропусков выше
//     optional_exclude_rate (выбор режима — в конфиге, фиксируется в
//     отчёте как решение документа моделей).
//   - тип недвижимости — onehot по наблюдаемым в выборке типам; в
//     однородной выборке (один тип) колонка постоянна = интерцепт,
//     и признак не добавляется (нулевая информация, не порог).
//   - доли пропусков по каждому признаку — в FitResult.MissingRates
//     (публикуются в отчёте; раздел «Модели» интерфейса — этап UI).
//
// Спецификация (ТЗ §7.2, оценивается отдельно для каждой страны и типа
// сделки; общая модель по трём странам запрещена):
//
//	log(price_per_sqm) = α + zone_effect(zone_id) + Σ βᵢ·attributeᵢ
//	                   + geo_level(geo_confidence)
//	                   + γ·f(area) + time_effect(month) + ε
//
// geo_level (C10/C16, ТЗ v4-2 §4.7): уровень геокода — хранимое поле
// objects.geo_confidence, которое модель ЧИТАЕТ как признак (onehot по
// словарю уровней + «не указано»), а не публикуемая метрика «ради
// метрики». Однородная выборка (все наблюдения одного уровня, включая
// все-неизвестный) — постоянная колонка = интерцепт, нулевая
// информация, признак не добавляется (то же правило, что тип
// недвижимости). Доля «не указано» — в MissingRates["geo_level"]
// (показывается всегда, даже когда признак не добавлен: это и есть
// замер «уровень геокода у 100%», ТЗ v4-2 §11.2).
//
// Допущения исполнителя (зафиксированы, см. отчёт этапа 5):
//   - f(area) = log(area): у спецификации один коэффициент γ, лог-форма
//     даёт постоянную эластичность цены за м² по площади — стандартная
//     гедоническая спецификация;
//   - time_effect(month) — 11 фиктивных переменных месяцев (февраль…
//     декабрь) по месяцу последнего наблюдения цены, январь — база;
//   - zone_effect — фиктивная переменная на эффективную зону; зона с
//     числом активных наблюдений меньше min_obs_per_zone берёт эффект
//     родительской зоны (zone_fallback=true, ТЗ §7.3). Штраф блока зон —
//     иерархическое сжатие К РОДИТЕЛЬСКОМУ УРОВНЮ, а не к нулю (ТЗ §7.2,
//     коррекция C1): колонки группируются по родителю — группа G =
//     {колонка-представитель уровня G (малые зоны) ∪ зоны с родителем G};
//     внутри группы — центрирование (I − 11ᵀ/k) (сжатие к среднему
//     уровня-родителя, среднее группы — свободный эффект уровня, как
//     фиктивные переменные ANOVA); одиночная группа — сжатие к
//     общенациональному уровню (интерцепт);
//   - категориальные атрибуты (bool/enum) — one-hot из attribute_registry
//     (used_in_pricing), значения вне allowed_values считаются пропуском;
//     числовые атрибуты (int/float) — линейная колонка без масштабирования
//     (гребневая регуляризация учитывает масштаб коэффициента);
//   - интервал предсказания — для нового объекта:
//     ŷ ± t(0.975, n−K)·σ·sqrt(1 + xᵀMx), M = (XᵀX + λP)⁻¹ (приблизительно,
//     с учётом гребневой регуляризации; P — матрица штрафа, см. ниже).
//
// Метод — гребневая регрессия на gonum/mat (ТЗ §7.2): регуляризация
// обязательна, λ подбирается k-fold кросс-валидацией по сетке из конфига,
// а не назначается (CV-MSE по каждому λ сетки — в FitResult.LambdaCVMSE,
// публикуется в отчёте: выбор λ прозрачен, коррекция C1). Матрица
// штрафа P: интерцепт не штрафуется; блок зон — иерархическое сжатие к
// родителю (zonePenalty); остальные колонки — диагональная единица
// (гребень). Деньги: модель работает в лог-пространстве float64 —
// это числовой расчёт, а не денежный путь: ТЗ §5 запрещает float для
// хранения/конвертации денег, а не для статистических вычислений.
// Результат возвращается в BIGINT-минорных единицах с округлением.
package valuation

import (
	"fmt"
	"math"
	"sort"

	"gonum.org/v1/gonum/mat"
)

// float64Epsilon — машинное ε для float64 (2⁻⁵²); rcond = K·ε —
// стандартный порог численного ранга (C21: ранг матрицы подгонки
// до её принятия).
const float64Epsilon = 2.220446049250313e-16

// Observation — один объект в выборке модели. Обязательный набор
// (цена, площадь, зона, тип — ТЗ v4-2 §4.10.1) у наблюдений выборки
// всегда заполнен: отсутствие любого из них — непригодное наблюдение
// (NULL с причиной, отсекает вызывающий).
type Observation struct {
	ObjectID     int64
	PriceMinor   int64   // актуальная цена, минорные единицы валюты рынка; > 0
	AreaSQM      float64 // м²; > 0
	ZoneID       *int64  // зона объекта; в выборке всегда != nil
	PropertyType string  // тип недвижимости; в выборке всегда != ""
	// AttrValues — значения атрибутов по ключам attribute_registry;
	// отсутствующий ключ или пустое значение — пропуск.
	AttrValues map[string]string
	// Month — месяц последнего наблюдения цены, 1..12 (UTC).
	Month int
	// GeoLevel — уровень геокода (objects.geo_confidence):
	// building | street | municipality | postcode | zone (словарь
	// geocode.Result.Confidence); "" — неизвестен. C10/C16: хранимое
	// поле, которое модель читает как признак (onehot + «не указано»).
	GeoLevel string
}

// AttrSpec — атрибут, участвующий в ценовой модели (used_in_pricing).
type AttrSpec struct {
	Key  string
	Kind string // onehot (bool/enum) | numeric (int/float)
	// Values — допустимые значения onehot (порядок one-hot-колонок).
	Values []string
}

// ModelInput — всё, что нужно модели для одной пары (страна, тип сделки).
type ModelInput struct {
	// Пороги ТЗ §7.3 (из конфига, не из кода).
	MinObsPerParam int
	MinObsPerZone  int
	// MaxMissingRate — порог покрытия ОБЯЗАТЕЛЬНОГО набора (ТЗ v4-2
	// §4.10.1); проверка — в Run, здесь не используется.
	MaxMissingRate float64
	// KFold и LambdaGrid — подбор λ (ТЗ §7.2).
	KFold      int
	LambdaGrid []float64 // по возрастанию, > 0
	// MaxMatCond (C21): максимум числа обусловленности матрицы
	// подгонки (XᵀX + λP); cond выше порога или ранг < K — подгонка
	// отвергается (degenerate_matrix). 0 — порог не задаётся (только
	// ранг). Гонум Solve решает вырожденную систему без ошибки,
	// поэтому проверка «нет ошибки» недостаточна (D16).
	MaxMatCond float64

	// Attrs — необязательные атрибуты модели, попорядку.
	Attrs []AttrSpec
	// OptionalMissing — режим пропусков необязательных признаков
	// (ТЗ v4-2 §4.10.2): "level" | "exclude".
	OptionalMissing string
	// OptionalExcludeRate — (только режим exclude): доля пропусков, выше
	// которой признак исключается из модели.
	OptionalExcludeRate float64

	// Observations — объекты с обязательным набором признаков.
	Observations []Observation
	// ZoneParent — зона → родитель (nil — родителя нет). Нужен для
	// zone_fallback (ТЗ §7.3).
	ZoneParent map[int64]*int64
}

// Prediction — результат модели по одному объекту.
type Prediction struct {
	// PriceDeviation — (цена объекта − предсказание)/предсказание;
	// nil — модель не выдала число, причина в NullReason (ТЗ §7.3).
	PriceDeviation *float64
	// NullReason — код причины NULL (машина-читаемый, префикс до «:»).
	NullReason string
	// Предсказанная цена и границы интервала, минорные единицы;
	// 0, когда PriceDeviation == nil.
	PredictedMinor    int64
	IntervalLowMinor  int64
	IntervalHighMinor int64
	// ZoneFallback — zone_effect взято с родительского уровня (ТЗ §7.3).
	ZoneFallback bool
}

// FitResult — итог прогона модели для одной пары (страна, тип сделки).
type FitResult struct {
	// Rejected — модель не построена (правила ТЗ §7.3); у всех объектов
	// PriceDeviation = NULL с причиной Reason.
	Rejected bool
	// Reason — причина отклонения модели (когда Rejected).
	Reason string

	// Выбранное λ и качество (уходит в строки valuations).
	Lambda     float64
	RSquared   float64
	SampleSize int // n — объектов в выборке
	Params     int // K — колонок матрицы проектирования
	// LambdaCVMSE — CV-MSE по каждому λ сетки (порядок LambdaGrid);
	// публикуется в отчёте: выбор λ виден, а не объявляется (ТЗ §7.2,
	// коррекция C1).
	LambdaCVMSE []float64

	// MatRank / MatCond (C21): ранг и число обусловленности матрицы
	// подгонки (XᵀX + λP) при выбранном λ; публикуются с показателями
	// качества (вместе с LambdaCVMSE).
	MatRank int
	MatCond float64

	// MissingRates — доля пропусков каждого необязательного признака в
	// выборке (ТЗ v4-2 §4.10.5: публикуется в отчёте/разделе «Модели»).
	MissingRates map[string]float64
	// ExcludedAttrs — признаки, исключённые из модели (режим exclude,
	// доля пропусков выше порога; ТЗ v4-2 §4.10.2).
	ExcludedAttrs []string

	// Predictions — по id объекта.
	Predictions map[int64]Prediction

	// ParamNames / Coeffs (C26): имена и коэффициенты колонок матрицы
	// проектирования (порядок совпадает с колонками X) — публикуются в
	// отчёте: коэффициент публикуемого признака виден вместе с долей
	// его уровня (seller_type, гео-уровень). Пусто, когда модель
	// отклонена до построения.
	ParamNames []string
	Coeffs     []float64
}

// Fit собирает матрицу проектирования, проверяет правила отказа
// (ТЗ §7.3 + v4-2 §4.10), подбирает λ кросс-валидацией и строит
// гребневую регрессию. Чисто: без БД, детерминированно (складки CV —
// round-robin по отсортированным id объектов).
func Fit(in *ModelInput) (*FitResult, error) {
	if len(in.LambdaGrid) == 0 {
		return nil, fmt.Errorf("valuation: lambda_grid не задан")
	}
	for _, l := range in.LambdaGrid {
		if l <= 0 {
			return nil, fmt.Errorf("valuation: lambda_grid содержит %v <= 0", l)
		}
	}
	if in.KFold < 2 {
		return nil, fmt.Errorf("valuation: kfold должен быть >= 2, задано %d", in.KFold)
	}
	switch in.OptionalMissing {
	case "level", "exclude":
	default:
		return nil, fmt.Errorf("valuation: optional_missing: level|exclude, задано %q (ТЗ v4-2 §4.10.2)", in.OptionalMissing)
	}
	if in.OptionalExcludeRate <= 0 || in.OptionalExcludeRate > 1 {
		return nil, fmt.Errorf("valuation: optional_exclude_rate должен быть в (0, 1], задано %v", in.OptionalExcludeRate)
	}

	n := len(in.Observations)
	res := &FitResult{SampleSize: n, Predictions: map[int64]Prediction{}, MissingRates: map[string]float64{}}
	if n == 0 {
		res.Rejected = true
		res.Reason = "no_observations"
		return res, nil
	}

	// Необязательные признаки (ТЗ v4-2 §4.10.2): пропуски модель НЕ
	// отклоняют. Режим exclude — признак с долей пропусков выше порога
	// исключается из модели (фиксируется в ExcludedAttrs); режим level —
	// все признаки остаются, пропуск кодируется явным уровнем
	// «не указано» (рекомендация ТЗ: пропуск несёт сведения).
	attrs := make([]AttrSpec, 0, len(in.Attrs))
	for _, a := range in.Attrs {
		missing := 0
		for _, o := range in.Observations {
			if attrMissing(a, o.AttrValues[a.Key]) {
				missing++
			}
		}
		rate := float64(missing) / float64(n)
		res.MissingRates[a.Key] = rate
		if in.OptionalMissing == "exclude" && rate > in.OptionalExcludeRate {
			res.ExcludedAttrs = append(res.ExcludedAttrs, a.Key)
			continue
		}
		attrs = append(attrs, a)
	}

	// Тип недвижимости — обязательный признак модели (ТЗ v4-2 §4.10.1),
	// в выборке у всех наблюдений заполнен. Onehot по наблюдаемым типам;
	// однородная выборка (один тип) — постоянная колонка = интерцепт,
	// признак не добавляется (нулевая информация, а не порог).
	typeSet := map[string]struct{}{}
	for _, o := range in.Observations {
		if o.PropertyType != "" {
			typeSet[o.PropertyType] = struct{}{}
		}
	}
	typeVals := sortedStrings(typeSet)
	if len(typeVals) == 1 {
		typeVals = nil
	}

	// Уровень геокода (C10/C16, ТЗ v4-2 §4.7): хранимое поле
	// objects.geo_confidence, которое модель читает как признак. Onehot
	// по словарю geoLevels + уровень «не указано» для значений вне
	// словаря (включая "" — неизвестен). Однородная выборка (все
	// наблюдения одного уровня, включая все-неизвестный) — постоянная
	// колонка = интерцепт, нулевая информация, признак не добавляется
	// (то же правило, что тип недвижимости).
	geoSet := map[string]struct{}{}
	for _, o := range in.Observations {
		geoSet[o.GeoLevel] = struct{}{}
	}
	geoCols := 0
	if len(geoSet) > 1 {
		geoCols = len(geoLevels) + 1 // словарь + «не указано»
	}
	// Доля «не указано» публикуется всегда (даже когда признак не
	// добавлен): это и есть замер «уровень геокода у 100%» (§11.2).
	geoUnknown := 0
	for _, o := range in.Observations {
		if geoIndexOf(o.GeoLevel) < 0 {
			geoUnknown++
		}
	}
	res.MissingRates["geo_level"] = float64(geoUnknown) / float64(n)

	// Эффективная зона каждого объекта (ТЗ §7.3): зона с числом активных
	// наблюдений меньше min_obs_per_zone → родительский уровень,
	// zone_fallback = true.
	zoneCount := map[int64]int{}
	for _, o := range in.Observations {
		if o.ZoneID != nil {
			zoneCount[*o.ZoneID]++
		}
	}
	effGroup := make([]*int64, n) // эффективная зона-группа фиктивной переменной
	fallback := make([]bool, n)
	for i, o := range in.Observations {
		if o.ZoneID == nil {
			continue
		}
		zone := *o.ZoneID
		if zoneCount[zone] >= in.MinObsPerZone {
			effGroup[i] = &zone
			continue
		}
		if p, ok := in.ZoneParent[zone]; ok && p != nil {
			effGroup[i] = p
		}
		// Родителя нет (топ-уровень иерархии) — без zone_effect.
		fallback[i] = true
	}

	// Раскладка колонок: [1 | зоны | атрибуты | тип | гео-уровень |
	// log(area) | месяцы]. Числовой признак — две колонки: значение +
	// индикатор пропуска (подстановка значения на место пропуска
	// запрещена, ТЗ v4-2 §4.10.4). Onehot в режиме level — дополнительная
	// колонка «не указано». Блок гео-уровня — 0 или len(geoLevels)+1
	// колонок (см. выше: однородная выборка — признак не добавляется).
	zoneGroups := sortedUniqueGroups(effGroup)
	attrCols := 0
	for _, a := range attrs {
		switch a.Kind {
		case "numeric":
			attrCols += 2
		default:
			attrCols += len(a.Values)
			if in.OptionalMissing == "level" {
				attrCols++ // «не указано»
			}
		}
	}
	const monthsCols = 11 // февраль…декабрь, январь — база
	K := 1 + len(zoneGroups) + attrCols + len(typeVals) + geoCols + 1 + monthsCols
	res.Params = K

	// Матрица штрафа P (K×K): интерцепт не штрафуется (P[0,0]=0);
	// блок зон (колонки 1..Z) — иерархическое сжатие к родителю
	// (zonePenalty, C1); остальные колонки (с Z+1) — диагональная
	// единица (гребень). P — симметричная неотрицательно
	// полуопределённая; при λ > 0 XᵀX + λP положительно определена:
	// единственная нештрафуемая колонка — интерцепт (колонка 0), и
	// её вклад в XᵀX положителен (якорь обратимости блока зон — в
	// zonePenalty). Важно: колонка с Z+1 штрафуется — без единицы на
	// диагонали у ВСЕХ нештрафуемых-кроме-интерцепта колонок
	// все-нулевая колонка (например, unused уровень onehot при Z=0)
	// делала XᵀX + λP вырожденной (D16, обнаружено C16-geo).
	Z := len(zoneGroups)
	pPen := mat.NewDense(K, K, nil)
	if Z > 0 {
		zp := zonePenalty(zoneGroups, in.ZoneParent)
		for i := 0; i < Z; i++ {
			for j := 0; j < Z; j++ {
				pPen.Set(1+i, 1+j, zp.At(i, j))
			}
		}
	}
	for i := 1 + Z; i < K; i++ {
		pPen.Set(i, i, 1)
	}

	// Правило 1 (ТЗ §7.3): наблюдений меньше, чем min_obs_per_param ×
	// число параметров — модель не выдаёт результат (критерий качества).
	if n < in.MinObsPerParam*K {
		res.Rejected = true
		res.Reason = fmt.Sprintf("insufficient_observations: n=%d, params=%d, min_obs_per_param=%d",
			n, K, in.MinObsPerParam)
		return res, nil
	}
	// (Правило пропусков §7.3 отменено ТЗ v4-2 §4.10: порог покрытия —
	// только для обязательного набора, он проверяется в Run по всем
	// активным объектам; необязательные признаки модель не отклоняют.)

	// Матрица проектирования X (n×K) и отклик y = log(price_per_sqm).
	x := make([]float64, 0, n*K)
	y := make([]float64, n)
	zoneCol := map[int64]int{}
	for i, g := range zoneGroups {
		zoneCol[*g] = i
	}
	attrCol := make([]int, len(attrs))
	c := 1 + len(zoneGroups)
	for i, a := range attrs {
		attrCol[i] = c
		switch a.Kind {
		case "numeric":
			c += 2
		default:
			c += len(a.Values)
			if in.OptionalMissing == "level" {
				c++
			}
		}
	}
	typeCol0 := c
	typeCol := map[string]int{}
	for i, tv := range typeVals {
		typeCol[tv] = typeCol0 + i
	}
	// Блок гео-уровня (существует при geoCols > 0): i-й уровень
	// geoLevels → geoCol0+i, «не указано» → geoCol0+len(geoLevels).
	geoCol0 := c + len(typeVals)
	areaCol := geoCol0 + geoCols
	monthCol0 := areaCol + 1 // месяц m → колонка monthCol0 + (m − 2), m = 2..12

	// Имена колонок матрицы проектирования (C26): тот же порядок, что
	// раскладка выше — публикация коэффициентов с долями уровней.
	colNames := make([]string, 0, K)
	colNames = append(colNames, "intercept")
	for _, g := range zoneGroups {
		colNames = append(colNames, fmt.Sprintf("zone:%d", *g))
	}
	for _, a := range attrs {
		switch a.Kind {
		case "numeric":
			colNames = append(colNames, "attr:"+a.Key, "attr:"+a.Key+":missing")
		default:
			for _, v := range a.Values {
				colNames = append(colNames, "attr:"+a.Key+"="+v)
			}
			if in.OptionalMissing == "level" {
				colNames = append(colNames, "attr:"+a.Key+"=not_specified")
			}
		}
	}
	for _, tv := range typeVals {
		colNames = append(colNames, "type:"+tv)
	}
	// Блок гео-уровня существует при geoCols > 0 (несколько уровней в
	// корпусе); однородный уровень — не признак, колонок нет.
	if geoCols > 0 {
		for _, gl := range geoLevels {
			colNames = append(colNames, "geo:"+gl)
		}
		colNames = append(colNames, "geo:not_specified")
	}
	colNames = append(colNames, "log_area")
	for m := 2; m <= 12; m++ {
		colNames = append(colNames, fmt.Sprintf("month:%d", m))
	}
	if len(colNames) != K {
		return nil, fmt.Errorf("valuation: раскладка имён колонок (%d) не совпадает с K=%d — внутренний сбой", len(colNames), K)
	}

	for i, o := range in.Observations {
		row := make([]float64, K)
		row[0] = 1
		if g := effGroup[i]; g != nil {
			row[1+zoneCol[*g]] = 1
		}
		for ai, a := range attrs {
			v := o.AttrValues[a.Key]
			if a.Kind == "numeric" {
				if f, ok := parseFloatAttr(v); ok {
					row[attrCol[ai]] = f
				} else {
					row[attrCol[ai]+1] = 1 // индикатор пропуска
				}
				continue
			}
			if idx := onehotIndex(a, v); idx >= 0 {
				row[attrCol[ai]+idx] = 1
			} else if in.OptionalMissing == "level" {
				row[attrCol[ai]+len(a.Values)] = 1 // «не указано»
			}
			// Режим exclude: пропуск = все нули (базовая категория).
		}
		if tc, ok := typeCol[o.PropertyType]; ok {
			row[tc] = 1
		}
		if geoCols > 0 {
			if gi := geoIndexOf(o.GeoLevel); gi >= 0 {
				row[geoCol0+gi] = 1
			} else {
				row[geoCol0+len(geoLevels)] = 1 // «не указано»
			}
		}
		row[areaCol] = math.Log(o.AreaSQM)
		if o.Month >= 2 {
			row[monthCol0+(o.Month-2)] = 1
		}
		x = append(x, row...)
		psm := float64(o.PriceMinor) / o.AreaSQM // минорные единицы / м²
		y[i] = math.Log(psm)
	}
	X := mat.NewDense(n, K, x)
	Y := mat.NewDense(n, 1, y)

	// Подбор λ: k-fold кросс-валидация, MSE на всех данных, при равенстве —
	// меньший λ (сетка по возрастанию, строгое «<» оставляет первый).
	k := in.KFold
	if k > n {
		k = n
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return in.Observations[order[a]].ObjectID < in.Observations[order[b]].ObjectID })
	var bestLambda, bestMSE float64
	for lambdaIdx, lambda := range in.LambdaGrid {
		sse := 0.0
		for f := 0; f < k; f++ {
			trX, trY, teX, teY := splitFold(order, f, k, X, Y)
			beta := ridgeFit(trX, trY, lambda, pPen)
			sse += sseOf(teX, teY, beta)
		}
		mse := sse / float64(n)
		res.LambdaCVMSE = append(res.LambdaCVMSE, mse)
		if lambdaIdx == 0 || mse < bestMSE {
			bestLambda, bestMSE = lambda, mse
		}
	}

	// Финальная модель на всех данных + дисперсия остатков.
	beta := ridgeFit(X, Y, bestLambda, pPen)
	yhat := new(mat.Dense)
	yhat.Mul(X, beta)

	sse := 0.0
	sumY, sumY2 := 0.0, 0.0
	for i := 0; i < n; i++ {
		e := y[i] - yhat.At(i, 0)
		sse += e * e
		sumY += y[i]
		sumY2 += y[i] * y[i]
	}
	meanY := sumY / float64(n)
	ssTot := sumY2 - float64(n)*meanY*meanY
	r2 := 0.0
	if ssTot > 0 {
		r2 = 1 - sse/ssTot
	}
	res.Lambda = bestLambda
	res.RSquared = r2

	// C26: коэффициенты с именами колонок — публикация с долями уровней.
	res.ParamNames = colNames
	res.Coeffs = make([]float64, K)
	for i := 0; i < K; i++ {
		res.Coeffs[i] = beta.At(i, 0)
	}

	df := n - K
	if df < 1 {
		df = 1
	}
	sigma := math.Sqrt(sse / float64(df))
	tCrit := tQuantile(0.975, float64(df))

	// M = (XᵀX + λP)⁻¹ для интервала предсказания нового объекта.
	// reg положительно определена при λ > 0 (P — симметричная
	// неотрицательно полуопределённая с якорем обратимости, см.
	// zonePenalty), поэтому Solve не ошибается — и при вырожденной
	// системе тоже (D16): решение возвращается без ошибки. Отсюда
	// C21: перед принятием подгонки считать ранг (rcond = K·ε —
	// численно устойчивый) и число обусловленности, отказ по порогу.
	reg := ridgeReg(X, bestLambda, pPen)
	svd := new(mat.SVD)
	if !svd.Factorize(reg, mat.SVDThin) { // сингулярные векторы не нужны — только ранг/cond
		res.Rejected = true
		res.Reason = "degenerate_matrix: svd_factorize_failed"
		return res, nil
	}
	rank := svd.Rank(float64(K) * float64Epsilon)
	cond := svd.Cond()
	res.MatRank, res.MatCond = rank, cond
	if rank < K || (in.MaxMatCond > 0 && cond > in.MaxMatCond) {
		res.Rejected = true
		res.Reason = fmt.Sprintf("degenerate_matrix: rank=%d/%d cond=%.3e (max_mat_cond=%.1e)",
			rank, K, cond, in.MaxMatCond)
		return res, nil
	}
	mInv := new(mat.Dense)
	mInv.Solve(reg, identityDense(K))

	// Предсказания.
	for i, o := range in.Observations {
		pi := Prediction{ZoneFallback: fallback[i]}
		yhatI := yhat.At(i, 0)
		xRow := X.RawRowView(i)
		mx := make([]float64, K)
		for r := 0; r < K; r++ {
			s := 0.0
			for j := 0; j < K; j++ {
				s += mInv.At(r, j) * xRow[j]
			}
			mx[r] = s
		}
		xMx := 0.0
		for j := 0; j < K; j++ {
			xMx += xRow[j] * mx[j]
		}
		seLog := sigma * math.Sqrt(1+xMx)
		predPSM := math.Exp(yhatI) // минорные единицы / м²
		lowPSM := math.Exp(yhatI - tCrit*seLog)
		highPSM := math.Exp(yhatI + tCrit*seLog)

		predicted := predPSM * o.AreaSQM // минорные единицы
		if predicted <= 0 || math.IsInf(predicted, 0) || math.IsNaN(predicted) {
			pi.NullReason = "degenerate_prediction"
			res.Predictions[o.ObjectID] = pi
			continue
		}
		dev := float64(o.PriceMinor)/predicted - 1
		pi.PriceDeviation = &dev
		pi.PredictedMinor = int64(math.Round(predicted))
		pi.IntervalLowMinor = int64(math.Round(lowPSM * o.AreaSQM))
		pi.IntervalHighMinor = int64(math.Round(highPSM * o.AreaSQM))
		res.Predictions[o.ObjectID] = pi
	}
	return res, nil
}

// attrMissing — пропуск атрибута: пустое значение; для onehot — ещё и
// значение вне allowed_values реестра (ТЗ §7.3 считает это пропуском).
func attrMissing(a AttrSpec, v string) bool {
	if v == "" {
		return true
	}
	if a.Kind == "numeric" {
		_, ok := parseFloatAttr(v)
		return !ok
	}
	return onehotIndex(a, v) < 0
}

// onehotIndex — позиция значения в one-hot-раскладке (−1 — не входит).
func onehotIndex(a AttrSpec, v string) int {
	for i, s := range a.Values {
		if s == v {
			return i
		}
	}
	return -1
}

// geoLevels — словарь уровней геокода (порядок one-hot-колонок блока
// geo_level, C10/C16). Совпадает с допустимыми значениями
// geocode.Result.Confidence: building | street | municipality |
// postcode | zone. Значение вне словаря (включая "" — неизвестен)
// кодируется отдельной колонкой «не указано».
var geoLevels = []string{"building", "street", "municipality", "postcode", "zone"}

// geoIndexOf — позиция уровня в geoLevels (−1 — не входит, в т.ч. "").
func geoIndexOf(v string) int {
	for i, s := range geoLevels {
		if s == v {
			return i
		}
	}
	return -1
}

// parseFloatAttr — числовое значение атрибута (string-представление из
// JSONB; допустимы "3", "3.5", "true"/"false" не ожидаются).
func parseFloatAttr(v string) (float64, bool) {
	if v == "" {
		return 0, false
	}
	var f float64
	if _, err := fmt.Sscanf(v, "%g", &f); err != nil {
		return 0, false
	}
	return f, true
}

// ridgeReg — XᵀX + λP (регрессор гребневой регрессии). P — матрица
// штрафа: интерцепт (первая колонка) не штрафуется, блок зон —
// иерархическое сжатие к родителю (ТЗ §7.2, C1), остальные колонки —
// единица. При λ > 0 регрессор положительно определён.
func ridgeReg(x *mat.Dense, lambda float64, p *mat.Dense) *mat.Dense {
	_, K := x.Dims()
	reg := new(mat.Dense)
	reg.Mul(x.T(), x)
	for i := 0; i < K; i++ {
		for j := 0; j < K; j++ {
			reg.Set(i, j, reg.At(i, j)+lambda*p.At(i, j))
		}
	}
	return reg
}

// ridgeFit — решение гребневой регрессии (XᵀX + λP)β = Xᵀy (см. ridgeReg).
func ridgeFit(x, y *mat.Dense, lambda float64, p *mat.Dense) *mat.Dense {
	reg := ridgeReg(x, lambda, p)
	xtb := new(mat.Dense)
	xtb.Mul(x.T(), y)
	beta := new(mat.Dense)
	beta.Solve(reg, xtb)
	return beta
}

// zonePenalty — матрица штрафа блока зон (Z×Z, Z — число эффективных
// зон-групп, порядок — zoneGroups). Иерархическое сжатие К РОДИТЕЛЬСКОМУ
// УРОВНЮ, а не к нулю (ТЗ §7.2, коррекция C1):
//   - колонки группируются по родителю: группа G = {колонка-представитель
//     уровня G (малые зоны, zone_fallback) ∪ колонки зон с родителем G}
//     (ключ группы = parent(zone) для зоны, сама зона для уровня без
//     родителя — то же правило, что эффективная группа в X);
//   - группа с k ≥ 2 колонками — центрирование (I − 11ᵀ/k): коэффициенты
//     сжимаются к среднему группы, а среднее группы — свободный эффект
//     уровня-родителя (фиктивные переменные ANOVA: внутриуровневые
//     отклонения штрафуются, уровень — нет);
//   - одиночная группа (k = 1) — диагональная единица: сжатие к
//     общенациональному уровню (интерцепт), как в классическом гребне;
//   - якорь обратимости: направление (интерцепт, −1 в каждой группе)
//     лежит в ядре XᵀX (каждая строка — ровно в одной группе) и в ядре
//     центрирующих блоков; если одиночной группы в корпусе нет, одна
//     колонка первой группы получает диагональную единицу — без неё
//     XᵀX + λP вырожден.
//
// Результат — симметричная неотрицательно полуопределённая матрица.
// Вызывать при Z > 0 (mat.NewDense не принимает нулевые размеры).
func zonePenalty(zoneGroups []*int64, parent map[int64]*int64) *mat.Dense {
	Z := len(zoneGroups)
	p := mat.NewDense(Z, Z, nil)
	byGroup := map[int64][]int{}
	for ci, g := range zoneGroups {
		key := *g
		if par, ok := parent[*g]; ok && par != nil {
			key = *par
		}
		byGroup[key] = append(byGroup[key], ci)
	}
	keys := make([]int64, 0, len(byGroup))
	for k := range byGroup {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool { return keys[a] < keys[b] })
	singletons := 0
	for _, key := range keys {
		cols := byGroup[key]
		if len(cols) == 1 {
			c := cols[0]
			p.Set(c, c, p.At(c, c)+1)
			singletons++
			continue
		}
		kf := float64(len(cols))
		for i := range cols {
			for j := range cols {
				v := -1.0 / kf
				if i == j {
					v += 1
				}
				p.Set(cols[i], cols[j], p.At(cols[i], cols[j])+v)
			}
		}
	}
	if singletons == 0 && Z > 0 {
		c := byGroup[keys[0]][0]
		p.Set(c, c, p.At(c, c)+1)
	}
	return p
}

// identityDense — единичная матрица n×n (в локальном кеше gonum
// пакетной функции NewIdentity нет).
func identityDense(n int) *mat.Dense {
	m := mat.NewDense(n, n, nil)
	for i := 0; i < n; i++ {
		m.Set(i, i, 1)
	}
	return m
}

// sseOf — сума квадратов ошибок предсказания X·beta против Y.
func sseOf(x, y, beta *mat.Dense) float64 {
	pred := new(mat.Dense)
	pred.Mul(x, beta)
	s := 0.0
	n := y.RawMatrix().Rows
	for i := 0; i < n; i++ {
		e := y.At(i, 0) - pred.At(i, 0)
		s += e * e
	}
	return s
}

// splitFold — тренировочная/тестовая части k-fold CV: складка f из k —
// тест, остальное — тренировка; складки назначаются round-robin по
// переданному (детерминированному) порядку строк.
func splitFold(order []int, fold, k int, x, y *mat.Dense) (trX, trY, teX, teY *mat.Dense) {
	trIdx, teIdx := []int{}, []int{}
	for pos, row := range order {
		if pos%k == fold {
			teIdx = append(teIdx, row)
		} else {
			trIdx = append(trIdx, row)
		}
	}
	return rowsSubset(x, trIdx), rowsSubset(y, trIdx), rowsSubset(x, teIdx), rowsSubset(y, teIdx)
}

// rowsSubset — выбор строк Dense по индексу (копии).
func rowsSubset(m *mat.Dense, idx []int) *mat.Dense {
	_, cols := m.Dims()
	data := make([]float64, 0, len(idx)*cols)
	for _, i := range idx {
		for c := 0; c < cols; c++ {
			data = append(data, m.At(i, c))
		}
	}
	return mat.NewDense(len(idx), cols, data)
}

// sortedStrings — отсортированные ключи множества (детерминированный
// порядок one-hot-колонок типа недвижимости).
func sortedStrings(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// sortedUniqueGroups — отсортированный список уникальных эффективных зон.
func sortedUniqueGroups(groups []*int64) []*int64 {
	set := map[int64]struct{}{}
	for _, g := range groups {
		if g != nil {
			set[*g] = struct{}{}
		}
	}
	out := make([]*int64, 0, len(set))
	for id := range set {
		v := id
		out = append(out, &v)
	}
	sort.Slice(out, func(a, b int) bool { return *out[a] < *out[b] })
	return out
}
