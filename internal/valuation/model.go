// Package valuation — гедоническая ценовая модель (ТЗ §7.2) с правилами
// отказа (ТЗ §7.3).
//
// Спецификация (ТЗ §7.2, оценивается отдельно для каждой страны и типа
// сделки; общая модель по трём странам запрещена):
//
//	log(price_per_sqm) = α + zone_effect(zone_id) + Σ βᵢ·attributeᵢ
//	                   + γ·f(area) + time_effect(month) + ε
//
// Допущения исполнителя (зафиксированы, см. отчёт этапа 5):
//   - f(area) = log(area): у спецификации один коэффициент γ, лог-форма
//     даёт постоянную эластичность цены за м² по площади — стандартная
//     гедоническая спецификация;
//   - time_effect(month) — 11 фиктивных переменных месяцев (февраль…
//     декабрь) по месяцу последнего наблюдения цены, январь — база;
//   - zone_effect — фиктивная переменная на эффективную зону; зона с
//     числом активных наблюдений меньше min_obs_per_zone берёт эффект
//     родительской зоны (zone_fallback=true, ТЗ §7.3);
//   - категориальные атрибуты (bool/enum) — one-hot из attribute_registry
//     (used_in_pricing), значения вне allowed_values считаются пропуском;
//     числовые атрибуты (int/float) — линейная колонка без масштабирования
//     (гребневая регуляризация учитывает масштаб коэффициента);
//   - интервал предсказания — для нового объекта:
//     ŷ ± t(0.975, n−K)·σ·sqrt(1 + xᵀMx), M = (XᵀX + λI)⁻¹ (приблизительно,
//     с учётом гребневой регуляризации).
//
// Метод — гребневая регрессия на gonum/mat (ТЗ §7.2): регуляризация
// обязательна, λ подбирается k-fold кросс-валидацией по сетке из конфига,
// а не назначается. Деньги: модель работает в лог-пространстве float64 —
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

// Observation — один объект в выборке модели (цена и площадь есть).
type Observation struct {
	ObjectID   int64
	PriceMinor int64   // актуальная цена, минорные единицы валюты рынка; > 0
	AreaSQM    float64 // м²; > 0
	ZoneID     *int64  // зона объекта; nil — зоны нет (нет координат)
	// AttrValues — значения атрибутов по ключам attribute_registry;
	// отсутствующий ключ или пустое значение — пропуск.
	AttrValues map[string]string
	// Month — месяц последнего наблюдения цены, 1..12 (UTC).
	Month int
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
	MaxMissingRate float64
	// KFold и LambdaGrid — подбор λ (ТЗ §7.2).
	KFold      int
	LambdaGrid []float64 // по возрастанию, > 0

	// Attrs — атрибуты модели, попорядку.
	Attrs []AttrSpec

	// Observations — объекты с ценой и площадью.
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

	// Predictions — по id объекта.
	Predictions map[int64]Prediction
}

// Fit собирает матрицу проектирования, проверяет правила отказа (ТЗ §7.3),
// подбирает λ кросс-валидацией и строит гребневую регрессию. Чисто:
// без БД, детерминированно (складки CV — round-robin по отсортированным
// id объектов).
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

	n := len(in.Observations)
	res := &FitResult{SampleSize: n, Predictions: map[int64]Prediction{}}
	if n == 0 {
		res.Rejected = true
		res.Reason = "no_observations"
		return res, nil
	}

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

	// Раскладка колонок: [1 | зоны | атрибуты | log(area) | месяцы 2..12].
	zoneGroups := sortedUniqueGroups(effGroup)
	attrCols := 0
	for _, a := range in.Attrs {
		if a.Kind == "numeric" {
			attrCols++
		} else {
			attrCols += len(a.Values)
		}
	}
	const monthsCols = 11 // февраль…декабрь, январь — база
	K := 1 + len(zoneGroups) + attrCols + 1 + monthsCols
	res.Params = K

	// Правило 1 (ТЗ §7.3): наблюдений меньше, чем min_obs_per_param ×
	// число параметров — модель не выдаёт результат.
	if n < in.MinObsPerParam*K {
		res.Rejected = true
		res.Reason = fmt.Sprintf("insufficient_observations: n=%d, params=%d, min_obs_per_param=%d",
			n, K, in.MinObsPerParam)
		return res, nil
	}

	// Правило 3 (ТЗ §7.3): доля пропусков по ключевому атрибуту выше
	// порога — модель не выдаёт результат.
	for _, a := range in.Attrs {
		missing := 0
		for _, o := range in.Observations {
			if attrMissing(a, o.AttrValues[a.Key]) {
				missing++
			}
		}
		rate := float64(missing) / float64(n)
		if rate > in.MaxMissingRate {
			res.Rejected = true
			res.Reason = fmt.Sprintf("attribute_missing_rate: key=%s, rate=%.2f, max=%.2f", a.Key, rate, in.MaxMissingRate)
			return res, nil
		}
	}

	// Матрица проектирования X (n×K) и отклик y = log(price_per_sqm).
	x := make([]float64, 0, n*K)
	y := make([]float64, n)
	zoneCol := map[int64]int{}
	for i, g := range zoneGroups {
		zoneCol[*g] = i
	}
	attrCol := make([]int, len(in.Attrs))
	c := 1 + len(zoneGroups)
	for i, a := range in.Attrs {
		attrCol[i] = c
		if a.Kind == "numeric" {
			c++
		} else {
			c += len(a.Values)
		}
	}
	areaCol := c
	monthCol0 := c + 1 // месяц m → колонка monthCol0 + (m − 2), m = 2..12
	for i, o := range in.Observations {
		row := make([]float64, K)
		row[0] = 1
		if g := effGroup[i]; g != nil {
			row[1+zoneCol[*g]] = 1
		}
		for ai, a := range in.Attrs {
			v := o.AttrValues[a.Key]
			if a.Kind == "numeric" {
				if f, ok := parseFloatAttr(v); ok {
					row[attrCol[ai]] = f
				}
				continue
			}
			if idx := onehotIndex(a, v); idx >= 0 {
				row[attrCol[ai]+idx] = 1
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
			beta := ridgeFit(trX, trY, lambda)
			sse += sseOf(teX, teY, beta)
		}
		mse := sse / float64(n)
		if lambdaIdx == 0 || mse < bestMSE {
			bestLambda, bestMSE = lambda, mse
		}
	}

	// Финальная модель на всех данных + дисперсия остатков.
	beta := ridgeFit(X, Y, bestLambda)
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

	df := n - K
	if df < 1 {
		df = 1
	}
	sigma := math.Sqrt(sse / float64(df))
	tCrit := tQuantile(0.975, float64(df))

	// M = (XᵀX + λI)⁻¹ для интервала предсказания нового объекта.
	xtx := new(mat.Dense)
	xtx.Mul(X.T(), X)
	// DenseCopyOf (а не new+Copy): в локальном кеше gonum метод Copy
	// копирует только пересечение размерностей получателя и источника
	// и НЕ realloc-ит получателя — new(mat.Dense).Copy(...) остаётся 0×0.
	reg := mat.DenseCopyOf(xtx)
	for i := 1; i < K; i++ {
		reg.Set(i, i, reg.At(i, i)+bestLambda)
	}
	// Ошибка Solve невозможна: reg = XᵀX + λP (P = diag(0,1,…,1))
	// положительно определена при λ > 0 (интерцепт не штрафуется).
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

// ridgeFit — решение гребневой регрессии (XᵀX + λI)β = Xᵀy. Интерцепт
// (первая колонка) не штрафуют: λ ставится на диагональ, кроме (0,0).
func ridgeFit(x, y *mat.Dense, lambda float64) *mat.Dense {
	_, K := x.Dims()
	xtx := new(mat.Dense)
	xtx.Mul(x.T(), x)
	xtb := new(mat.Dense)
	xtb.Mul(x.T(), y)
	for i := 1; i < K; i++ {
		xtx.Set(i, i, xtx.At(i, i)+lambda)
	}
	beta := new(mat.Dense)
	beta.Solve(xtx, xtb)
	return beta
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
