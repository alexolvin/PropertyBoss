package valuation

import "math"

// tQuantile — квантиль распределения Стьюдента: t такое, что
// P(T <= t) = p при T ~ t(df), df >= 1.
//
// Используется для 95% интервала предсказания: tQuantile(0.975, n-K).
// Пакет gonum/stat/dist отсутствует в локальном модульном кеше (см.
// отчёт этапа 5 — особенность окружения), поэтому CDF считается
// численно: F(t) = 0.5 + ∫_0^t f(u)du (адаптивный Симпсон по гладкой
// плотности t-распределения) + бисекция. Проверка: при df=1 (Коши)
// F(t) = 0.5 + arctan(t)/π, при большом df — нормальное распределение
// (TestTQuantileKnownValues против табличных квантилей).
func tQuantile(p, df float64) float64 {
	if df < 1 {
		panic("valuation: tQuantile: df >= 1")
	}
	if p <= 0 || p >= 1 {
		panic("valuation: tQuantile: p в (0,1)")
	}
	if p <= 0.5 {
		// Симметрия; в текущем использовании не применяется (p = 0.975).
		return -tQuantile(1-p, df)
	}
	lo := 0.0
	hi := 1.0
	for tCDF(hi, df) < p {
		hi *= 2
	}
	for i := 0; i < 100; i++ {
		mid := 0.5 * (lo + hi)
		if tCDF(mid, df) < p {
			lo = mid
		} else {
			hi = mid
		}
		if lo == hi {
			break
		}
	}
	return 0.5 * (lo + hi)
}

// tCDF — функция распределения Стьюдента с df степенями свободы.
func tCDF(t, df float64) float64 {
	if t < 0 {
		return 1 - tCDF(-t, df)
	}
	// f(u) = tNorm(df) * (1 + u²/df)^(-(df+1)/2); плотность чётная,
	// поэтому F(t) = 0.5 + ∫_0^t.
	return 0.5 + tNorm(df)*asimpson(func(u float64) float64 {
		return math.Pow(1+u*u/df, -(df+1)/2)
	}, 0, t, 1e-13)
}

// tNorm — нормирующий множитель плотности t:
// Γ((df+1)/2) / (√(df·π)·Γ(df/2)).
func tNorm(df float64) float64 {
	la, _ := math.Lgamma((df + 1) / 2)
	lb, _ := math.Lgamma(df / 2)
	return math.Exp(la - lb - 0.5*math.Log(df*math.Pi))
}

// asimpson — ∫_a^b f dx адаптивным правилом Симпсона: рекурсивное
// дробление, пока |S(2 полуинтервала) − S(целый)| ≤ 15·tol (оценка
// ошибки Симпсона 1/15), глубина не более 40. Для гладких f (плотность
// t) сходится за десятки разбиений; tol — абсолютная.
func asimpson(f func(float64) float64, a, b, tol float64) float64 {
	if a == b {
		return 0
	}
	fa, fb := f(a), f(b)
	m := 0.5 * (a + b)
	fm := f(m)
	s := (b - a) / 6 * (fa + 4*fm + fb)
	return asimpRec(f, a, m, b, fa, fm, fb, s, tol, 40)
}

func asimpRec(f func(float64) float64, a, m, b, fa, fm, fb, s, tol float64, depth int) float64 {
	ml := 0.5 * (a + m)
	mr := 0.5 * (m + b)
	fl := f(ml)
	fr := f(mr)
	sl := (m - a) / 6 * (fa + 4*fl + fm)
	sr := (b - m) / 6 * (fm + 4*fr + fb)
	if depth <= 0 || math.Abs(sl+sr-s) <= 15*tol {
		// Внесение поправки Richardson 1/15.
		return sl + sr + (sl+sr-s)/15
	}
	return asimpRec(f, a, ml, m, fa, fl, fm, sl, tol/2, depth-1) +
		asimpRec(f, m, mr, b, fm, fr, fb, sr, tol/2, depth-1)
}
