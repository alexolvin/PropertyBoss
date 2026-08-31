package schedule

import (
	"math"
	"testing"
	"time"
)

// Unit-тесты ЧИСТОЙ логики расписания (без БД): слоты, веса,
// экспоненциальный откат, потолок, парсер спецификаций.
// DB-часть покрыта live-тестами (PB_TEST_DSN, schedule_live_test.go).

func TestSlotKey(t *testing.T) {
	if got := SlotKey(1, 9); got != "1-9" {
		t.Errorf("SlotKey(1,9) = %q, ждали 1-9", got)
	}
	if got := SlotKey(0, 0); got != "0-0" {
		t.Errorf("SlotKey(0,0) = %q, ждали 0-0", got)
	}
}

func TestWindowHours(t *testing.T) {
	if hs := WindowHours(8, 20); len(hs) != 12 || hs[0] != 8 || hs[11] != 19 {
		t.Errorf("WindowHours(8,20) = %v, ждали 8..19", hs)
	}
	if hs := WindowHours(0, 24); len(hs) != 24 || hs[0] != 0 || hs[23] != 23 {
		t.Errorf("WindowHours(0,24) = %v, ждали 0..23 (24 — до полуночи)", hs)
	}
	if hs := WindowHours(23, 24); len(hs) != 1 || hs[0] != 23 {
		t.Errorf("WindowHours(23,24) = %v, ждали [23]", hs)
	}
}

func TestBackoffDuration(t *testing.T) {
	base, mult, max := 60*time.Minute, 2.0, 72*time.Hour
	cases := []struct {
		strikes int
		want    time.Duration
	}{
		{1, 60 * time.Minute},
		{2, 2 * time.Hour},
		{3, 4 * time.Hour},
		{4, 8 * time.Hour},
		{5, 16 * time.Hour},
		{6, 32 * time.Hour},
		{7, 64 * time.Hour},  // 64ч — ещё до потолка
		{8, 72 * time.Hour},  // 128ч -> потолок 72ч
		{50, 72 * time.Hour}, // не переполняется, потолок
	}
	for _, c := range cases {
		if got := BackoffDuration(base, mult, max, c.strikes); got != c.want {
			t.Errorf("BackoffDuration(strikes=%d) = %v, ждали %v", c.strikes, got, c.want)
		}
	}
	// strikes < 1 — не меньше base.
	if got := BackoffDuration(base, mult, max, 0); got != base {
		t.Errorf("BackoffDuration(strikes=0) = %v, ждали %v", got, base)
	}
	// Множитель 1 — без экспоненты.
	if got := BackoffDuration(base, 1.0, max, 5); got != base {
		t.Errorf("BackoffDuration(mult=1, strikes=5) = %v, ждали %v", got, base)
	}
}

func TestEffectiveCap(t *testing.T) {
	cases := []struct {
		max  int
		rate float64
		want int
	}{
		{4, 1.0, 4},
		{4, 0.5, 2},
		{4, 0.25, 1},
		{4, 0.1, 0},  // floor: потолок может стать 0 — источник ждёт
		{3, 0.5, 1},  // floor(1.5) = 1
		{3, 0.33, 0}, // floor(0.99) = 0
	}
	for _, c := range cases {
		if got := EffectiveCap(c.max, c.rate); got != c.want {
			t.Errorf("EffectiveCap(%d, %v) = %d, ждали %d", c.max, c.rate, got, c.want)
		}
	}
}

func TestWindowPoolAvg(t *testing.T) {
	w := &Window{DOW: 1, HourStart: 9, HourEnd: 11} // слоты 1-9, 1-10
	yields := map[string]SlotAgg{
		"1-9":  {Scans: 10, NewObjects: 30}, // avg 3
		"1-10": {Scans: 10, NewObjects: 10}, // avg 1
	}
	avg, n := w.poolAvg(yields)
	if n != 20 {
		t.Errorf("scans = %d, ждали 20", n)
	}
	if math.Abs(avg-2.0) > 1e-12 {
		t.Errorf("avg = %v, ждали 2.0 (пул 40/20, а не среднее средних)", avg)
	}
	// Пустое окно данных — (0, 0), не ошибка.
	avg, n = w.poolAvg(map[string]SlotAgg{"2-9": {Scans: 5, NewObjects: 5}})
	if avg != 0 || n != 0 {
		t.Errorf("пустые данные: avg=%v n=%d, ждали 0/0", avg, n)
	}
}

func TestWindowWeightsWarmingUp(t *testing.T) {
	windows := []Window{
		{ID: 1, DOW: 1, HourStart: 0, HourEnd: 24},
		{ID: 2, DOW: 2, HourStart: 0, HourEnd: 24},
	}
	// warming_up: равные веса 1.0, данные выхода не используются
	// (ТЗ §10.5 — консервативно, «не настроено по статистике»).
	weights := windowWeights(windows, map[string]SlotAgg{"1-0": {Scans: 100, NewObjects: 1000}}, true, 0.1)
	for i, w := range weights {
		if w != 1.0 {
			t.Errorf("warming_up: weight[%d] = %v, ждали 1.0", i, w)
		}
	}
}

func TestWindowWeightsProportional(t *testing.T) {
	const eps = 0.1
	// A (дow 1, 0-24): 10 сканов, 30 новых → avg 3.
	// B (дow 2, 0-24): 10 сканов, 10 новых → avg 1.
	// C (дow 3, 0-24): данных нет → avg 0, вес — ε-пол (ТЗ §10.3:
	// слот не отключается насовсем).
	windows := []Window{
		{ID: 1, DOW: 1, HourStart: 0, HourEnd: 24},
		{ID: 2, DOW: 2, HourStart: 0, HourEnd: 24},
		{ID: 3, DOW: 3, HourStart: 0, HourEnd: 24},
	}
	// По одному занесённому слоту в окне A и B (пул по всем слотам
	// окна: окна 0-24, данные в слоте 0).
	yields := map[string]SlotAgg{
		SlotKey(1, 0): {Scans: 10, NewObjects: 30}, // окно A: avg 3
		SlotKey(2, 0): {Scans: 10, NewObjects: 10}, // окно B: avg 1
	}
	weights := windowWeights(windows, yields, false, eps)
	wantA := eps + (1-eps)*1.0       // avg 3 = maxAvg
	wantB := eps + (1-eps)*(1.0/3.0) // avg 1 / 3
	if math.Abs(weights[0]-wantA) > 1e-12 {
		t.Errorf("weight A = %v, ждали %v", weights[0], wantA)
	}
	if math.Abs(weights[1]-wantB) > 1e-12 {
		t.Errorf("weight B = %v, ждали %v", weights[1], wantB)
	}
	if weights[2] != eps {
		t.Errorf("weight C = %v, ждали ε-пол %v (слот не отключается)", weights[2], eps)
	}
	if !(weights[0] > weights[1] && weights[1] > weights[2]) {
		t.Errorf("порядок весов нарушен: %v", weights)
	}
}

func TestWindowWeightsAllZero(t *testing.T) {
	const eps = 0.1
	windows := []Window{
		{ID: 1, DOW: 1, HourStart: 0, HourEnd: 24},
		{ID: 2, DOW: 2, HourStart: 0, HourEnd: 24},
	}
	// Нигде данных нет: все веса ε — равно и консервативно.
	weights := windowWeights(windows, map[string]SlotAgg{}, false, eps)
	for i, w := range weights {
		if w != eps {
			t.Errorf("weight[%d] = %v, ждали ε=%v", i, w, eps)
		}
	}
}

func TestParseSpec(t *testing.T) {
	cases := []struct {
		spec string
		min  int
		max  int
		want []int
		err  bool
	}{
		{"0-6", 0, 6, []int{0, 1, 2, 3, 4, 5, 6}, false},
		{"1-5,0", 0, 6, []int{0, 1, 2, 3, 4, 5}, false},
		{"3", 0, 23, []int{3}, false},
		{"8-10,8", 0, 23, []int{8, 9, 10}, false}, // дубликаты схлопываются
		{" 2 - 4 ", 0, 6, []int{2, 3, 4}, false},  // пробелы допустимы
		{"6-2", 0, 6, nil, true},                  // начало > конца
		{"0-7", 0, 6, nil, true},                  // вне диапазона
		{"", 0, 6, nil, true},                     // пусто
		{"x", 0, 6, nil, true},                    // не число
	}
	for _, c := range cases {
		got, err := ParseSpec(c.spec, c.min, c.max)
		if c.err {
			if err == nil {
				t.Errorf("ParseSpec(%q): ожидалась ошибка, получили %v", c.spec, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSpec(%q): %v", c.spec, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("ParseSpec(%q) = %v, ждали %v", c.spec, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("ParseSpec(%q) = %v, ждали %v", c.spec, got, c.want)
				break
			}
		}
	}
}
