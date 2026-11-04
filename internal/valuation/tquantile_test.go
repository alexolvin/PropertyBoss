package valuation

import (
	"math"
	"testing"
)

// Табличные квантили t-распределения (0.975) — справочные значения.
func TestTQuantileKnownValues(t *testing.T) {
	cases := []struct {
		df   float64
		want float64
	}{
		{1, 12.706205},
		{2, 4.302653},
		{5, 2.570582},
		{10, 2.228139},
		{17, 2.109816},
		{20, 2.085963},
		{30, 2.042272},
		{60, 2.000298},
		{100, 1.983972},
		{1000, 1.962345},
	}
	for _, tc := range cases {
		got := tQuantile(0.975, tc.df)
		if math.Abs(got-tc.want) > 1e-4 {
			t.Errorf("tQuantile(0.975, %v) = %v, want %v (diff %.2e)", tc.df, got, tc.want, math.Abs(got-tc.want))
		}
	}
}

// tCDF: F(0) = 0.5; симметрия F(-t) = 1 - F(t); монотонность.
func TestTCDFProperties(t *testing.T) {
	const df = 17
	if got := tCDF(0, df); math.Abs(got-0.5) > 1e-12 {
		t.Errorf("tCDF(0) = %v, want 0.5", got)
	}
	for _, tv := range []float64{0.5, 1.0, 2.109816, 3.0} {
		sum := tCDF(tv, df) + tCDF(-tv, df)
		if math.Abs(sum-1) > 1e-10 {
			t.Errorf("tCDF(%v)+tCDF(-%v) = %v, want 1", tv, tv, sum)
		}
	}
	// t(0.975; 17) = 2.109816 — квантиль из тестов выше самодостаточен,
	// здесь проверим согласованность CDF и квантиля.
	tq := tQuantile(0.975, df)
	if got := tCDF(tq, df); math.Abs(got-0.975) > 1e-9 {
		t.Errorf("tCDF(tQuantile(0.975)) = %v, want 0.975", got)
	}
	// Монотонность.
	prev := tCDF(0, df)
	for i := 1; i <= 50; i++ {
		tv := float64(i) / 10
		cur := tCDF(tv, df)
		if cur <= prev {
			t.Errorf("tCDF не монотонна в %v: %v <= %v", tv, cur, prev)
		}
		prev = cur
	}
}
