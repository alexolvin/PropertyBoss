// model.go — пуллогированная логистическая регрессия на
// person-period данных: дискретная модель дожития (ТЗ §9.2).
package liquidity

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"
)

// maxWeekBin — длительность экспозиции кодируется индикаторами
// недель 0..11 + хвост «12+» (ТЗ §9.2: «сплайн или набор
// индикаторов интервалов»; с текущим объёмом событий сплайн
// переобучил бы, набор индикаторов — честнее).
const maxWeekBin = 12

// fitRidge — крошечный L2 на неконстантные столбцы: защита от
// полной сепарации и вырожденных (постоянно нулевых) столбцов.
// Величина пренебрежима для коэффициентов (сжатие ~1e-5) —
// числовой стандарт, а не модельное допущение.
const fitRidge = 1e-6

// AttrSpec — атрибут реестра в кодировании модели.
type AttrSpec struct {
	Key    string
	Kind   string   // "onehot" | "numeric"
	Values []string // onehot: значения из allowed_values реестра
}

// FeatRow — входная строка кодирования: person-period интервал
// (обучение/валидация) или текущее состояние объекта (прогноз).
type FeatRow struct {
	Week       int
	Month      int // 1..12, месяц начала интервала (UTC)
	Reductions int
	DropPct    float64
	DaysSince  int
	Increased  int
	ValDev     *float64
	ZoneID     *int64
	Attrs      map[string]string
}

// Model — обученная дискретная модель дожития.
type Model struct {
	Names []string // имена колонок (аудит → params JSONB)
	Beta  []float64
	Zones []int64 // zone-индикаторы в порядке колонок
	Attrs []AttrSpec
}

// colLayout — раскладка колонок матрицы признаков (порядок фиксирован
// для аудита):
// [intercept | week_0..11, week_12plus | month_2..12 (январь — база) |
//
//	price_reductions, price_drop_pct, days_since_price_change,
//	price_increased, price_deviation | zone_* | attr_*].
//
// price_deviation и атрибуты — реестр ТЗ §9.2.
func colLayout(zones []int64, attrs []AttrSpec) []string {
	names := []string{"intercept"}
	for k := 0; k < maxWeekBin; k++ {
		names = append(names, fmt.Sprintf("week_%d", k))
	}
	names = append(names, "week_12plus")
	for m := 2; m <= 12; m++ {
		names = append(names, fmt.Sprintf("month_%d", m))
	}
	names = append(names, "price_reductions", "price_drop_pct",
		"days_since_price_change", "price_increased", "price_deviation")
	for _, z := range zones {
		names = append(names, fmt.Sprintf("zone_%d", z))
	}
	for _, a := range attrs {
		if a.Kind == "numeric" {
			names = append(names, "attr_"+a.Key)
		} else {
			for _, v := range a.Values {
				names = append(names, fmt.Sprintf("attr_%s=%s", a.Key, v))
			}
		}
	}
	return names
}

// encode — вектор признаков строки (ТЗ §9.2: значения на начало
// интервала). Пропуски: числовой атрибут → 0, one-hot → все нули,
// price_deviation nil → 0, зона вне обучающего набора → все нули —
// та же конвенция, что у гедонической модели (этап 5).
func (m *Model) encode(r FeatRow) []float64 {
	K := len(m.Names)
	x := make([]float64, K)
	x[0] = 1
	w := r.Week
	if w < 0 {
		w = 0
	}
	if w >= maxWeekBin {
		x[1+maxWeekBin] = 1 // week_12plus
	} else {
		x[1+w] = 1
	}
	if r.Month >= 2 && r.Month <= 12 {
		x[1+maxWeekBin+1+(r.Month-2)] = 1
	}
	c := 1 + maxWeekBin + 1 + 11 // начало блока price-*
	x[c] = float64(r.Reductions)
	x[c+1] = r.DropPct
	x[c+2] = float64(r.DaysSince)
	x[c+3] = float64(r.Increased)
	if r.ValDev != nil {
		x[c+4] = *r.ValDev
	}
	zc := c + 5
	for _, z := range m.Zones {
		if r.ZoneID != nil && *r.ZoneID == z {
			x[zc] = 1
			break
		}
		zc++
	}
	ac := zc + len(m.Zones)
	for _, a := range m.Attrs {
		if a.Kind == "numeric" {
			if v, ok := parseFloatAttr(r.Attrs[a.Key]); ok {
				x[ac] = v
			}
			ac++
			continue
		}
		v := r.Attrs[a.Key]
		for i, val := range a.Values {
			if v == val {
				x[ac+i] = 1
				break
			}
		}
		ac += len(a.Values)
	}
	return x
}

// fitLogistic — логистическая регрессия методом IRLS (Ньютон).
// Матрица весов n×n НЕ материализуется (на реальных данных это
// сотни МБ, ТЗ §3.8): XᵀWX и XᵀW(y−p) накапливаются напрямую за
// O(n·K²). Возвращает коэффициенты и признак сходимости
// (несходимость → статус uncalibrated, ТЗ §9.4).
func fitLogistic(x [][]float64, y []int) (beta []float64, converged bool) {
	n, K := len(x), 0
	if n > 0 {
		K = len(x[0])
	}
	beta = make([]float64, K)
	const maxIter, tol = 200, 1e-9
	for it := 0; it < maxIter; it++ {
		xtwx := make([]float64, K*K)
		xtwres := make([]float64, K)
		for i := 0; i < n; i++ {
			eta := 0.0
			for j := 0; j < K; j++ {
				eta += x[i][j] * beta[j]
			}
			p := 1 / (1 + math.Exp(-eta))
			wgt := p * (1 - p)
			if wgt < 1e-12 {
				wgt = 1e-12
			}
			res := float64(y[i]) - p
			for a := 0; a < K; a++ {
				xa := x[i][a]
				if xa == 0 {
					continue
				}
				wxa := wgt * xa
				// Ньютон: (XᵀWX)β' = XᵀWX·β + Xᵀ(y−p) — второй
				// член без весов (взвешенный был бы неверной итерацией).
				xtwres[a] += wxa*eta + xa*res
				for b := a; b < K; b++ {
					xtwx[a*K+b] += wxa * x[i][b]
				}
			}
		}
		// Симметрия + L2 на неконстантных столбцах (fitRidge выше).
		for a := 0; a < K; a++ {
			for b := a + 1; b < K; b++ {
				xtwx[a*K+b] = (xtwx[a*K+b] + xtwx[b*K+a]) / 2
				xtwx[b*K+a] = xtwx[a*K+b]
			}
		}
		for a := 0; a < K; a++ {
			if a > 0 {
				xtwx[a*K+a] += fitRidge
			}
		}
		// Система K×K.
		var maxDelta float64
		ok := true
		for j := 0; j < K; j++ {
			// Гаусс с выбором ведущего элемента.
			piv := j
			for r := j + 1; r < K; r++ {
				if math.Abs(xtwx[r*K+j]) > math.Abs(xtwx[piv*K+j]) {
					piv = r
				}
			}
			if math.Abs(xtwx[piv*K+j]) < 1e-15 {
				ok = false
				break
			}
			if piv != j {
				for c := j; c < K; c++ {
					xtwx[j*K+c], xtwx[piv*K+c] = xtwx[piv*K+c], xtwx[j*K+c]
				}
				xtwres[j], xtwres[piv] = xtwres[piv], xtwres[j]
			}
			d := xtwx[j*K+j]
			xtwres[j] /= d
			for c := j + 1; c < K; c++ {
				xtwx[j*K+c] /= d
			}
			for r := j + 1; r < K; r++ {
				f := xtwx[r*K+j]
				if f == 0 {
					continue
				}
				xtwres[r] -= f * xtwres[j]
				for c := j + 1; c < K; c++ {
					xtwx[r*K+c] -= f * xtwx[j*K+c]
				}
				xtwx[r*K+j] = 0
			}
		}
		if !ok {
			return nil, false
		}
		for j := K - 1; j >= 0; j-- {
			s := xtwres[j]
			for c := j + 1; c < K; c++ {
				s -= xtwx[j*K+c] * beta[c]
			}
			betaNew := s
			if d := math.Abs(betaNew - beta[j]); d > maxDelta {
				maxDelta = d
			}
			// beta[c] справа — старые значения (обход снизу вверх),
			// обновляем в конце цикла.
			beta[j] = betaNew
		}
		if maxDelta < tol {
			return beta, true
		}
	}
	return nil, false
}

// predict — вероятность ухода с рынка в следующем недельном
// интервале при данном состоянии признаков.
func (m *Model) predict(r FeatRow) float64 {
	x := m.encode(r)
	eta := 0.0
	for j := range m.Beta {
		eta += x[j] * m.Beta[j]
	}
	return 1 / (1 + math.Exp(-eta))
}

// horizonProb — вероятность ухода с рынка в ближайшие T дней:
// 1 − ∏(1 − h_k) по первым ceil(T/7) неделям (ТЗ §9.2). Недельные
// предикторы (week/month) сдвигаются по шагам; предикторы цены
// заморожены на текущих значениях — будущее поведение цены
// неизвестно (допущение, см. отчёт).
func (m *Model) horizonProb(r FeatRow, start time.Time, horizonDays int) float64 {
	steps := (horizonDays + 6) / 7
	if steps < 1 {
		steps = 1
	}
	surv := 1.0
	for k := 0; k < steps; k++ {
		rk := r
		rk.Week = r.Week + k
		rk.Month = int(start.AddDate(0, 0, 7*k).UTC().Month())
		h := m.predict(rk)
		if h > 1 {
			h = 1
		}
		surv *= 1 - h
	}
	return 1 - surv
}

// parseFloatAttr — числовой атрибут из map[string]string.
func parseFloatAttr(v string) (float64, bool) {
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// uniqueZones — отсортированные уникальные zone_id из набора строк.
func uniqueZones(rs []FeatRow) []int64 {
	set := make(map[int64]bool)
	for _, r := range rs {
		if r.ZoneID != nil {
			set[*r.ZoneID] = true
		}
	}
	out := make([]int64, 0, len(set))
	for z := range set {
		out = append(out, z)
	}
	sort.Slice(out, func(a, b int) bool { return out[a] < out[b] })
	return out
}
