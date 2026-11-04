// metrics.go — обязательные метрики валидации (ТЗ §9.4):
// калибровочная кривая по децилям предсказанной вероятности,
// Brier score с разложением на калибровку/разрешение/неопределённость,
// C-index. Всё считается на временном хольдауте (интервалы после T).
package liquidity

import (
	"math"
	"sort"
)

// CalibDecile — точка калибровочной кривой: дециль по
// предсказанной вероятности (1 — самые низкие, 10 — самые высокие).
type CalibDecile struct {
	Decile    int     `json:"decile"`
	Predicted float64 `json:"predicted"` // среднее предсказанных
	Actual    float64 `json:"actual"`    // фактическая доля ушедших
	N         int     `json:"n"`
}

// BrierDecomp — компоненты разложения Brier (см. BrierDecompose:
// точное тождество включает также within и inbin_cov).
type BrierDecomp struct {
	Reliability float64 `json:"reliability"` // отклонение от калибровки
	Resolution  float64 `json:"resolution"`  // разделимость (полезная часть)
	Uncertainty float64 `json:"uncertainty"` // базовая неопределённость f(1−f)
}

// decileGroup — номер группы для строки i отсортированного по
// предсказанию набора: min(9, i·10/n). Сбалансированные децили для
// любого n (при n ≥ 10 — ровно по n/10 на группу; в проде
// n ≥ minTestIntervals, все 10 децилей непустые).
func decileGroup(i, n int) int {
	g := i * 10 / n
	if g > 9 {
		g = 9
	}
	return g
}

// sortedIndices — индексы строк по возрастанию предсказанной
// вероятности (общая подготовка для децилей и разложения Brier).
func sortedIndices(pred []float64) []int {
	rows := make([]int, len(pred))
	for i := range rows {
		rows[i] = i
	}
	sort.SliceStable(rows, func(a, b int) bool { return pred[rows[a]] < pred[rows[b]] })
	return rows
}

// CalibrationDeciles — разбиение по децилям предсказанной
// вероятности: в каждом дециле сравнивается среднее предсказанных
// вероятностей с фактической долей ушедших.
func CalibrationDeciles(pred []float64, y []int) []CalibDecile {
	n := len(pred)
	if n == 0 {
		return []CalibDecile{}
	}
	rows := sortedIndices(pred)
	type acc struct {
		sumP   float64
		events int
		cnt    int
	}
	accs := make([]acc, 10)
	for i := 0; i < n; i++ {
		g := decileGroup(i, n)
		idx := rows[i]
		accs[g].sumP += pred[idx]
		accs[g].events += y[idx]
		accs[g].cnt++
	}
	out := []CalibDecile{}
	for g := 0; g < 10; g++ {
		if accs[g].cnt == 0 {
			continue
		}
		c := accs[g].cnt
		out = append(out, CalibDecile{
			Decile:    g + 1,
			Predicted: accs[g].sumP / float64(c),
			Actual:    float64(accs[g].events) / float64(c),
			N:         c,
		})
	}
	return out
}

// MaxCalibDev — максимум |predicted − actual| по непустым децилям.
// second — был ли хотя бы один непустой дециль.
func MaxCalibDev(d []CalibDecile) (max float64, any bool) {
	for _, c := range d {
		if c.N == 0 {
			continue
		}
		any = true
		if dev := math.Abs(c.Predicted - c.Actual); dev > max {
			max = dev
		}
	}
	return max, any
}

// BrierScore — средняя квадратичная ошибка вероятностей.
func BrierScore(pred []float64, y []int) float64 {
	if len(pred) == 0 {
		return 0
	}
	s := 0.0
	for i := range pred {
		e := pred[i] - float64(y[i])
		s += e * e
	}
	return s / float64(len(pred))
}

// BrierDecompose — компоненты разложения Brier по децилям (те же
// квантили, что CalibrationDeciles) + сам Brier score. Точное
// тождество (выведено по группам):
//
//	Brier = reliability + within − resolution + uncertainty − 2·inbin_cov,
//
// где within — внутридецильная дисперсия предсказаний, inbin_cov —
// внутридецильная ковариация (p, y). Трёхчленная запись
// reliability+resolution−uncertainty тождеством НЕ является (на
// сырой выборке может быть отрицательной), поэтому brier_score в
// строке модели — истинный mean(p−y)², а компоненты — стандартные
// диагностические величины калибровки.
func BrierDecompose(pred []float64, y []int) (BrierDecomp, float64) {
	n := len(pred)
	if n == 0 {
		return BrierDecomp{}, 0
	}
	rows := sortedIndices(pred)
	fTotal := 0.0
	for i := range y {
		fTotal += float64(y[i])
	}
	fTotal /= float64(n)
	type acc struct {
		sumP   float64
		events int
		cnt    int
	}
	accs := make([]acc, 10)
	for i := 0; i < n; i++ {
		g := decileGroup(i, n)
		idx := rows[i]
		accs[g].sumP += pred[idx]
		accs[g].events += y[idx]
		accs[g].cnt++
	}
	var reliability, resolution float64
	for g := 0; g < 10; g++ {
		if accs[g].cnt == 0 {
			continue
		}
		cnt := float64(accs[g].cnt)
		pMean, fMean := accs[g].sumP/cnt, float64(accs[g].events)/cnt
		dpf := pMean - fMean
		reliability += cnt * dpf * dpf
		dfr := fMean - fTotal
		resolution += cnt * dfr * dfr
	}
	reliability /= float64(n)
	resolution /= float64(n)
	uncertainty := fTotal * (1 - fTotal)
	return BrierDecomp{
		Reliability: reliability,
		Resolution:  resolution,
		Uncertainty: uncertainty,
	}, BrierScore(pred, y)
}

// CIndex — доля concordant-пар на валидационной выборке: пара
// (ушедший, не ушедший) concordant, если у ушедшего предсказанная
// вероятность выше; равенство — 0.5. Пары «оба ушли» / «оба
// остались» не сравнимы в интервальной цензуре и не считаются.
// Алгоритм O(n log n) (сортировка по предсказанию + подсчёт
// накопленных нулей) — на больших хольдаутах O(n²) не уложилось бы.
// second — число сравнимых пар (0 → C-index не определён, NULL).
func CIndex(pred []float64, y []int) (float64, int) {
	rows := make([]int, len(pred))
	for i := range rows {
		rows[i] = i
	}
	sort.SliceStable(rows, func(a, b int) bool { return pred[rows[a]] < pred[rows[b]] })
	// Группы по равным предсказаниям.
	n := len(pred)
	var concordant float64
	comparable := 0
	zeroTotal := 0
	for i := range y {
		if y[i] == 0 {
			zeroTotal++
		}
	}
	zeroBefore := 0 // нули в уже обработанных (более низких) группах
	g := 0
	for g < n {
		h := g
		for h < n && pred[rows[h]] == pred[rows[g]] {
			h++
		}
		oneIn, zeroIn := 0, 0
		for i := g; i < h; i++ {
			if y[rows[i]] == 1 {
				oneIn++
			} else {
				zeroIn++
			}
		}
		// Нули после группы = все нули − до группы − в группе.
		zeroAfter := zeroTotal - zeroBefore - zeroIn
		// Ушедшие в группе concordant с нулями НИЖЕ по предсказанию
		// (до группы в восходящей сортировке); нули внутри группы —
		// 0.5 concordant (связка).
		concordant += float64(oneIn) * float64(zeroBefore)
		concordant += 0.5 * float64(oneIn) * float64(zeroIn)
		comparable += oneIn * (zeroBefore + zeroIn + zeroAfter)
		zeroBefore += zeroIn
		g = h
	}
	if comparable == 0 {
		return 0, 0
	}
	return concordant / float64(comparable), comparable
}
