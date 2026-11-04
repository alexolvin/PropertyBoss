package valuation

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"gonum.org/v1/gonum/mat"

	"propertyboss/internal/config"
	"propertyboss/internal/db"
)

// gauss — нормальный шум (Box-Muller) для синтетических выборок.
func gauss(rng *rand.Rand) float64 {
	u1 := 1 - rng.Float64()
	u2 := 1 - rng.Float64()
	return math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*u2)
}

// baseInput — пороги ТЗ §7.3 / v4-2 §4.10, как в конфиге по умолчанию.
func baseInput() *ModelInput {
	return &ModelInput{
		MinObsPerParam:      10,
		MinObsPerZone:       30,
		MaxMissingRate:      0.5,
		KFold:               5,
		LambdaGrid:          []float64{0.01, 0.1, 1, 10, 100},
		OptionalMissing:     "level",
		OptionalExcludeRate: 0.5,
	}
}

// ТЗ §7.3: нет наблюдений — модель не строится, причина фиксируется.
func TestFitNoObservations(t *testing.T) {
	fit, err := Fit(baseInput())
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if !fit.Rejected || fit.Reason != "no_observations" {
		t.Fatalf("ожидался отказ no_observations, получено rejected=%v reason=%q", fit.Rejected, fit.Reason)
	}
}

// ТЗ §7.3: n < min_obs_per_param × params — отказ (критерий качества,
// сохранён в v4-2 §4.10).
// K = 1 + 0 зон + 5 (onehot 4 значения + «не указано», режим level) +
// 0 (один тип — нет типа-колонки) + 1 (area) + 11 (месяцы) = 18.
func TestFitInsufficientObservations(t *testing.T) {
	in := baseInput()
	in.Attrs = []AttrSpec{{Key: "state", Kind: "onehot", Values: []string{"a", "b", "c", "d"}}}
	for i := 0; i < 50; i++ {
		in.Observations = append(in.Observations, Observation{
			ObjectID:   int64(i + 1),
			PriceMinor: 100000000,
			AreaSQM:    80,
			Month:      1,
		})
	}
	fit, err := Fit(in)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if !fit.Rejected {
		t.Fatalf("ожидался отказ, модель построилась")
	}
	if !strings.HasPrefix(fit.Reason, "insufficient_observations") {
		t.Fatalf("ожидалась причина insufficient_observations, получено %q", fit.Reason)
	}
}

// missingSample — 200 наблюдений, у 120 атрибут «state» пропущен
// (доля 0.6). Общий базис для тестов режима пропусков (ТЗ v4-2 §4.10.2).
func missingSample() []Observation {
	var obs []Observation
	for i := 0; i < 200; i++ {
		attrs := map[string]string{}
		if i >= 120 { // 120 из 200 без значения
			attrs["state"] = []string{"a", "b", "c"}[i%3]
		}
		obs = append(obs, Observation{
			ObjectID:   int64(i + 1),
			PriceMinor: int64(100000000 * (1 + 0.01*float64(i%10))),
			AreaSQM:    50 + 5*float64(i%20),
			AttrValues: attrs,
			Month:      1 + i%12,
		})
	}
	return obs
}

// ТЗ v4-2 §4.10.2: высокая доля пропусков опционального признака модель
// НЕ отклоняет (прежний отказ attribute_missing_rate отменён). Режим
// level — признак остаётся, пропуск кодируется уровнем «не указано»;
// доля пропусков фиксируется в MissingRates (§4.10.5).
func TestFitLevelModeHighMissingNoReject(t *testing.T) {
	in := baseInput() // OptionalMissing = "level"
	in.Attrs = []AttrSpec{{Key: "state", Kind: "onehot", Values: []string{"a", "b", "c"}}}
	in.Observations = missingSample()
	fit, err := Fit(in)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if fit.Rejected {
		t.Fatalf("режим level: модель отклонена из-за пропусков — запрещено (ТЗ v4-2 §4.10.2): %s", fit.Reason)
	}
	if got := fit.MissingRates["state"]; got < 0.59 || got > 0.61 {
		t.Errorf("MissingRates[state] = %v, ожидал ≈0.60", got)
	}
	// K = 1 + 0 зон + 4 (onehot 3 значения + «не указано») + 0 (тип) +
	// 1 (area) + 11 (месяцы) = 17.
	if fit.Params != 17 {
		t.Errorf("Params = %d, ожидал 17 (onehot 3 + «не указано»)", fit.Params)
	}
}

// ТЗ v4-2 §4.10.2, режим exclude: признак с долей пропусков выше порога
// исключается из модели (фиксируется в ExcludedAttrs), модель строится.
func TestFitExcludeModeDropsAttr(t *testing.T) {
	in := baseInput()
	in.OptionalMissing = "exclude"
	in.OptionalExcludeRate = 0.5
	in.Attrs = []AttrSpec{
		{Key: "state", Kind: "onehot", Values: []string{"a", "b", "c"}}, // пропусков 0.6 > 0.5 → исключить
		{Key: "rare_miss", Kind: "onehot", Values: []string{"x", "y"}},  // пропусков 0 → остаться
	}
	in.Observations = missingSample()
	for i := range in.Observations {
		if in.Observations[i].AttrValues == nil {
			in.Observations[i].AttrValues = map[string]string{}
		}
		// Всегда заполнен (0 пропусков) — признак остаётся в модели.
		in.Observations[i].AttrValues["rare_miss"] = []string{"x", "y"}[i%2]
	}
	fit, err := Fit(in)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if fit.Rejected {
		t.Fatalf("модель отклонена: %s — пропуски опциональных признаков не отклоняют", fit.Reason)
	}
	if len(fit.ExcludedAttrs) != 1 || fit.ExcludedAttrs[0] != "state" {
		t.Fatalf("ExcludedAttrs = %v, ожидал [state]", fit.ExcludedAttrs)
	}
	if _, ok := fit.MissingRates["state"]; !ok {
		t.Error("MissingRates не зафиксировал исключённый признак")
	}
	// K без «state»: 1 + 2 (rare_miss) + 1 (area) + 11 (месяцы) = 15.
	if fit.Params != 15 {
		t.Errorf("Params = %d, ожидал 15 (state исключён)", fit.Params)
	}
}

// ТЗ v4-2 §4.10.4: импутация запрещена — численный признак всегда
// занимает две колонки (значение + индикатор пропуска); onehot в режиме
// level — значения + «не указано». Проверка по числу параметров K
// (заполняется до проверки минимальной выборки).
func TestFitMissingColumnLayout(t *testing.T) {
	mk := func(mode string) *ModelInput {
		in := baseInput()
		in.OptionalMissing = mode
		in.Attrs = []AttrSpec{
			{Key: "cat", Kind: "onehot", Values: []string{"a", "b"}},
			{Key: "num", Kind: "numeric"},
		}
		// Оба признака заполнены (пропусков нет — признаки не
		// исключаются ни в одном режиме): n=1 упрёт в правило 1,
		// но Params заполняется до него.
		in.Observations = []Observation{{
			ObjectID: 1, PriceMinor: 100, AreaSQM: 50, Month: 3,
			AttrValues: map[string]string{"cat": "a", "num": "10"},
		}}
		return in
	}
	// level: 1 + (2 значения + «не указано») + 2 (число + индикатор) +
	// 0 (тип) + 1 + 11 = 18.
	f1, err := Fit(mk("level"))
	if err != nil {
		t.Fatalf("Fit level: %v", err)
	}
	if f1.Params != 18 {
		t.Errorf("level: Params = %d, ожидал 18", f1.Params)
	}
	// exclude: 1 + 2 (без «не указано») + 2 + 0 + 1 + 11 = 17.
	f2, err := Fit(mk("exclude"))
	if err != nil {
		t.Fatalf("Fit exclude: %v", err)
	}
	if f2.Params != 17 {
		t.Errorf("exclude: Params = %d, ожидал 17", f2.Params)
	}
}

// ТЗ v4-2 §4.10.1: тип недвижимости — признак модели; в выборке с двумя
// типами появляется onehot-колонка, в однородной — нет (постоянная
// колонка = интерцепт, нулевая информация).
func TestFitTypeOneHot(t *testing.T) {
	mk := func(types []string) *ModelInput {
		in := baseInput()
		var obs []Observation
		for i, ty := range types {
			obs = append(obs, Observation{
				ObjectID:     int64(i + 1),
				PriceMinor:   100,
				AreaSQM:      50,
				PropertyType: ty,
				Month:        3,
			})
		}
		in.Observations = obs
		return in
	}
	// один тип: K = 1 + 1 + 11 = 13 (без типа-колонки).
	f1, err := Fit(mk([]string{"flat", "flat", "flat"}))
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if f1.Params != 13 {
		t.Errorf("один тип: Params = %d, ожидал 13", f1.Params)
	}
	// два типа: K = 1 + 2 (onehot по типам) + 1 + 11 = 15.
	f2, err := Fit(mk([]string{"flat", "house", "flat"}))
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if f2.Params != 15 {
		t.Errorf("два типа: Params = %d, ожидал 15", f2.Params)
	}
}

// C10/C16 (ТЗ v4-2 §4.7): уровень геокода — хранимое поле, которое
// модель читает как признак. Негомогенная выборка — onehot по словарю
// geoLevels (5 уровней) + «не указано» (+6 колонок); однородная
// (один уровень, включая все-неизвестный "") — признак не добавляется
// (постоянная колонка = интерцепт, нулевая информация, то же правило,
// что тип недвижимости). MissingRates["geo_level"] публикуется всегда.
func TestFitGeoLevelOneHot(t *testing.T) {
	mk := func(levels []string) *ModelInput {
		in := baseInput()
		var obs []Observation
		for i, lv := range levels {
			obs = append(obs, Observation{
				ObjectID:   int64(i + 1),
				PriceMinor: 100,
				AreaSQM:    50,
				GeoLevel:   lv,
				Month:      3,
			})
		}
		in.Observations = obs
		return in
	}
	// Два уровня + неизвестный: K = 1 + 6 (5 уровней + «не указано») +
	// 1 (area) + 11 (месяцы) = 19.
	f1, err := Fit(mk([]string{"postcode", "street", ""}))
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if f1.Params != 19 {
		t.Errorf("смешанные уровни: Params = %d, ожидал 19 (onehot 5 + «не указано»)", f1.Params)
	}
	if got := f1.MissingRates["geo_level"]; got < 1/3.0-1e-9 || got > 1/3.0+1e-9 {
		t.Errorf("MissingRates[geo_level] = %v, ожидал 1/3", got)
	}
	// Однородная (один известный уровень): K = 1 + 1 + 11 = 13 —
	// признак не добавляется.
	f2, err := Fit(mk([]string{"postcode", "postcode", "postcode"}))
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if f2.Params != 13 {
		t.Errorf("однородная: Params = %d, ожидал 13 (признак не добавляется)", f2.Params)
	}
	if got := f2.MissingRates["geo_level"]; got != 0 {
		t.Errorf("MissingRates[geo_level] = %v, ожидал 0", got)
	}
	// Однородная все-неизвестный (""): K = 13 (не один уровень ≠
	// гетерогенности), MissingRates = 1.
	f3, err := Fit(mk([]string{"", "", ""}))
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if f3.Params != 13 {
		t.Errorf("все-неизвестный: Params = %d, ожидал 13 (признак не добавляется)", f3.Params)
	}
	if got := f3.MissingRates["geo_level"]; got != 1 {
		t.Errorf("MissingRates[geo_level] = %v, ожидал 1", got)
	}
}

// C10/C16: модель восстанавливает эффект уровня геокода — наблюдение
// с «street» истинно дешевле «postcode», без geo-блока это было бы
// систематическим отклонением. Проверка по среднему |отклонению|
// (аналог TestFitRecoversSignal для нового признака).
func TestFitGeoLevelRecoversSignal(t *testing.T) {
	const n = 700
	in := baseInput()
	rng := rand.New(rand.NewSource(11))
	for i := 0; i < n; i++ {
		area := 40 + 100*rng.Float64() // 40..140 м²
		// Уровень — независимое случайное извлечение (НЕ i%2: месяц
		// 1+i%12 сохраняет i%2, и month-фиктивные поглотили бы эффект
		// geo-уровня — тест тогда проходил бы и без geo-блока).
		level := "postcode"
		geoMult := 1.0
		if rng.Float64() < 0.5 {
			level = "street"
			geoMult = 0.85 // street истинно на 15% дешевле
		}
		y := math.Log(100000.0) + 0.10*math.Log(area) + math.Log(geoMult)
		y += 0.01 * gauss(rng)
		price := math.Exp(y) * area // базовые единицы валюты
		in.Observations = append(in.Observations, Observation{
			ObjectID:   int64(2000 + i),
			PriceMinor: int64(math.Round(price * 100)),
			AreaSQM:    area,
			GeoLevel:   level,
			Month:      1 + i%12,
		})
	}

	fit, err := Fit(in)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if fit.Rejected {
		t.Fatalf("модель отклонена: %s", fit.Reason)
	}
	if fit.Params != 19 {
		t.Fatalf("Params = %d, ожидал 19 (geo-блок добавлен)", fit.Params)
	}
	var sumAbs, maxAbs float64
	for i := 0; i < n; i++ {
		id := int64(2000 + i)
		p, ok := fit.Predictions[id]
		if !ok {
			t.Fatalf("нет предсказания для объекта %d", id)
		}
		if p.PriceDeviation == nil {
			t.Fatalf("объект %d: deviation=NULL (%s)", id, p.NullReason)
		}
		d := math.Abs(*p.PriceDeviation)
		sumAbs += d
		if d > maxAbs {
			maxAbs = d
		}
	}
	if sumAbs/float64(n) > 0.012 {
		t.Errorf("среднее |отклонение| = %v — эффект geo-уровня не восстановлен", sumAbs/float64(n))
	}
	if maxAbs > 0.06 {
		t.Errorf("макс |отклонение| = %v — слишком велик", maxAbs)
	}
}

// Позиция: модель восстанавливает известный сигнал (зона, атрибут, площадь),
// отклонения малы, интервал предсказания содержит предсказание.
func TestFitRecoversSignal(t *testing.T) {
	const n = 700
	zoneA, zoneB := int64(1), int64(2)
	in := baseInput()
	in.Attrs = []AttrSpec{{Key: "has_garage", Kind: "onehot", Values: []string{"false", "true"}}}
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < n; i++ {
		area := 40 + 100*rng.Float64() // 40..140 м²
		zone := zoneA
		if i%2 == 1 {
			zone = zoneB
		}
		garage := i%3 == 0
		zoneMult, attrMult := 1.0, 1.0
		if zone == zoneB {
			zoneMult = 1.25
		}
		if garage {
			attrMult = 1.2
		}
		// Истинная модель: log(PSM) = c + 0.1·log(area) + зона + атрибут + шум 1%.
		y := math.Log(100000.0) + 0.10*math.Log(area) + math.Log(zoneMult*attrMult)
		y += 0.01 * gauss(rng)
		price := math.Exp(y) * area // базовые единицы валюты
		in.Observations = append(in.Observations, Observation{
			ObjectID:   int64(1000 + i),
			PriceMinor: int64(math.Round(price * 100)),
			AreaSQM:    area,
			ZoneID:     &zone,
			AttrValues: map[string]string{"has_garage": fmt.Sprintf("%t", garage)},
			Month:      1 + i%12,
		})
	}

	fit, err := Fit(in)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if fit.Rejected {
		t.Fatalf("модель отклонена: %s", fit.Reason)
	}
	if fit.RSquared < 0.9 {
		t.Errorf("R² = %v — сигнал не восстановлен", fit.RSquared)
	}
	var sumAbs, maxAbs float64
	for i := 0; i < n; i++ {
		id := int64(1000 + i)
		p, ok := fit.Predictions[id]
		if !ok {
			t.Fatalf("нет предсказания для объекта %d", id)
		}
		if p.PriceDeviation == nil {
			t.Fatalf("объект %d: deviation=NULL (%s)", id, p.NullReason)
		}
		if p.ZoneFallback {
			t.Fatalf("объект %d: zone_fallback при ≥30 наблюдениях в зоне", id)
		}
		if !(p.IntervalLowMinor < p.PredictedMinor && p.PredictedMinor < p.IntervalHighMinor) {
			t.Fatalf("объект %d: интервал [%d, %d] не содержит %d",
				id, p.IntervalLowMinor, p.IntervalHighMinor, p.PredictedMinor)
		}
		d := math.Abs(*p.PriceDeviation)
		sumAbs += d
		if d > maxAbs {
			maxAbs = d
		}
	}
	if sumAbs/float64(n) > 0.012 {
		t.Errorf("среднее |отклонение| = %v — сигнал не восстановлен", sumAbs/float64(n))
	}
	if maxAbs > 0.06 {
		t.Errorf("макс |отклонение| = %v — слишком велик", maxAbs)
	}
}

// ТЗ §7.3: зона с < min_obs_per_zone наблюдений берёт эффект родителя,
// результат помечается zone_fallback=true.
func TestFitZoneFallback(t *testing.T) {
	zoneA := int64(1) // 2 наблюдения (< min_obs_per_zone=30)
	zoneB := int64(2) // 160 наблюдений
	root := int64(9)  // родитель zoneA
	in := baseInput()
	in.ZoneParent = map[int64]*int64{zoneA: &root}
	rng := rand.New(rand.NewSource(7))
	add := func(id int, zone int64) {
		area := 60 + 20*rng.Float64()
		y := math.Log(100000.0) + 0.001*gauss(rng)
		in.Observations = append(in.Observations, Observation{
			ObjectID:   int64(id),
			PriceMinor: int64(math.Round(math.Exp(y) * area * 100)),
			AreaSQM:    area,
			ZoneID:     &zone,
			Month:      1 + id%12,
		})
	}
	for i := 0; i < 2; i++ {
		add(100+i, zoneA)
	}
	for i := 0; i < 160; i++ {
		add(200+i, zoneB)
	}

	fit, err := Fit(in)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if fit.Rejected {
		t.Fatalf("модель отклонена: %s", fit.Reason)
	}
	for id := 100; id < 102; id++ {
		p, ok := fit.Predictions[int64(id)]
		if !ok || p.PriceDeviation == nil {
			t.Fatalf("объект %d: нет предсказания (%s)", id, p.NullReason)
		}
		if !p.ZoneFallback {
			t.Errorf("объект %d (малая зона): zone_fallback=false — должен быть true", id)
		}
	}
	for id := 200; id < 360; id++ {
		p, ok := fit.Predictions[int64(id)]
		if !ok || p.PriceDeviation == nil {
			t.Fatalf("объект %d: нет предсказания (%s)", id, p.NullReason)
		}
		if p.ZoneFallback {
			t.Errorf("объект %d (большая зона): zone_fallback=true — должен быть false", id)
		}
	}
}

// Детерминированность: два прогона по одним данным — те же λ, R² и числа.
func TestFitDeterministic(t *testing.T) {
	zone := int64(5)
	in := baseInput()
	rng := rand.New(rand.NewSource(99))
	for i := 0; i < 300; i++ {
		area := 40 + 100*rng.Float64()
		y := math.Log(120000.0) + 0.05*math.Log(area) + 0.02*gauss(rng)
		in.Observations = append(in.Observations, Observation{
			ObjectID:   int64(i + 1),
			PriceMinor: int64(math.Round(math.Exp(y) * area * 100)),
			AreaSQM:    area,
			ZoneID:     &zone,
			Month:      1 + i%12,
		})
	}
	f1, err := Fit(in)
	if err != nil {
		t.Fatalf("Fit 1: %v", err)
	}
	f2, err := Fit(in)
	if err != nil {
		t.Fatalf("Fit 2: %v", err)
	}
	if f1.Lambda != f2.Lambda || f1.RSquared != f2.RSquared {
		t.Fatalf("недетерминированно: λ %v/%v R² %v/%v", f1.Lambda, f2.Lambda, f1.RSquared, f2.RSquared)
	}
	p1, p2 := f1.Predictions[1], f2.Predictions[1]
	if p1.PriceDeviation == nil || p2.PriceDeviation == nil {
		t.Fatalf("нет отклонений для сравнения")
	}
	if *p1.PriceDeviation != *p2.PriceDeviation || p1.PredictedMinor != p2.PredictedMinor {
		t.Fatalf("предсказания различаются между прогонами")
	}
}

// C21/D16: gonum Solve решает вырожденную систему БЕЗ ошибки — проверка
// «нет ошибки» недостаточна. Перед принятием подгонки считаются ранг и
// число обусловленности матрицы подгонки (XᵀX + λP): точная
// коллинеарность (атрибут = 100000·log(area) — та же колонка в
// лог-пространстве, бит-в-бит воспроизводимо) даёт cond >> порога →
// отказ degenerate_matrix до использования коэффициентов. Контроль —
// та же выборка без дубля: принята, ранг полный, cond под порогом.
func TestFitDegenerateMatrix(t *testing.T) {
	newSample := func(withDup bool) *ModelInput {
		in := baseInput()
		in.LambdaGrid = []float64{0.01} // λ закреплён: отказ идёт по порогу обусловленности, не по выбору CV
		in.MaxMatCond = 1e12             // дефолт конфига (C21)
		if withDup {
			in.Attrs = []AttrSpec{{Key: "duplog", Kind: "numeric"}}
		}
		for i := 0; i < 200; i++ {
			area := 50 + 5*float64(i%20)
			psm := 15000000.0 * (1 + 0.01*float64(i%10))
			attrs := map[string]string{}
			if withDup {
				// %v — кратчайшая round-trip запись: ParseFloat в модели
				// восстановит тот же float64 → строгая коллинеарность
				// с колонкой log(area).
				attrs["duplog"] = fmt.Sprintf("%v", 100000*math.Log(area))
			}
			in.Observations = append(in.Observations, Observation{
				ObjectID:   int64(i + 1),
				PriceMinor: int64(psm * area),
				AreaSQM:    area,
				AttrValues: attrs,
				Month:      1,
			})
		}
		return in
	}

	fit, err := Fit(newSample(true))
	if err != nil {
		t.Fatalf("Fit (коллинеарно): %v", err)
	}
	if !fit.Rejected {
		t.Fatalf("коллинеарная колонка: подгонка принята (rank=%d/%d cond=%.3e)", fit.MatRank, fit.SampleSize, fit.MatCond)
	}
	if !strings.HasPrefix(fit.Reason, "degenerate_matrix") {
		t.Fatalf("причина: %q, ожидал префикс degenerate_matrix", fit.Reason)
	}

	// Контроль: та же выборка без дубля — принята, ранг полный,
	// обусловленность под порогом (число публикуется — FitResult).
	ok, err := Fit(newSample(false))
	if err != nil {
		t.Fatalf("Fit (контроль): %v", err)
	}
	if ok.Rejected {
		t.Fatalf("контроль: подгонка отклонена: %s", ok.Reason)
	}
	if ok.MatRank < 1 || ok.MatCond <= 1 || ok.MatCond >= 1e12 {
		t.Errorf("контроль: MatRank=%d MatCond=%.3e — ожидал полный ранг и cond < 1e12", ok.MatRank, ok.MatCond)
	}
}

// --- Live-тесты (PB_TEST_DSN) ---

func testPool(t *testing.T) *pgxpool.Pool {
	dsn := os.Getenv("PB_TEST_DSN")
	if dsn == "" {
		t.Skip("PB_TEST_DSN не задан — live-тест пропускается")
	}
	t.Helper()
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	unlock, err := db.LiveTestLock(context.Background(), pool)
	if err != nil {
		t.Fatalf("live lock: %v", err)
	}
	// t.Cleanup идёт LIFO: тестовые сетыпы регистрируют sweep ПОСЛЕ
	// unlock — чистка работает под локом, лок отпущен последним.
	t.Cleanup(pool.Close)
	t.Cleanup(unlock)
	return pool
}

func vvCfg() *config.Config {
	// Минимальный конфиг прогона: страна-заглушка 'VV' с валютой рынка EUR;
	// пороги намеренно ниже реальных — тестовая выборка мала.
	// Отдельная песочная страна (не 'TT' пакета zones): go test гоняет
	// бинари пакетов параллельно, и общий country позволял cleanup
	// одного бинаря стирать фикстуры другого (FK-срывы в параллельном
	// свипе, поймано на этапе 11).
	cfg := &config.Config{}
	cfg.Dashboard.Countries = []string{"VV"}
	cfg.Dashboard.DealTypes = []string{"sale"}
	cfg.Dashboard.MarketCurrencies = map[string]string{"VV": "EUR"}
	cfg.Valuation.MinObsPerParam = 2
	cfg.Valuation.MinObsPerZone = 3
	cfg.Valuation.MaxMissingRate = 0.5
	cfg.Valuation.KFold = 3
	cfg.Valuation.LambdaGrid = []float64{0.1, 1, 10}
	// обязательные с валидацией Fit (da4af07): без них live-тест падает
	// на «optional_missing: level|exclude, задано ""»
	cfg.Valuation.OptionalMissing = "level"
	cfg.Valuation.OptionalExcludeRate = 0.5
	return cfg
}

// vvSetup — зоны (регион + 2 муниципалитета), атрибут, объекты-подопытные.
// sampleCount объектов в M2 (большая зона, ≥ min_obs_per_zone), 2 в M1
// (малая → zone_fallback на регион), 1 без цены, 1 в не рыночной валюте
// (CZK), 1 с NULL валютой (objects.currency — nullable; регрессия: скан в
// string падал, честная причина — no_currency), 1 без зоны (zone_id NULL →
// no_zone) и 1 без типа (property_type NULL → no_type) — регрессия
// обязательного набора ТЗ v4-2 §4.10.1.
// Возвращает id зон M1/M2 и id особых объектов; cleanup — в t.Cleanup.
func vvSetup(t *testing.T, pool *pgxpool.Pool, sampleCount int) (m1, m2, noPrice, czkObj, noCur, noZone, noType int64) {
	t.Helper()
	ctx := context.Background()
	geom := "MULTIPOLYGON(((0 0,1 0,1 1,0 1,0 0)))"
	var region int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO zones (country, level, name, geom, source)
		 VALUES ('VV', 'region', 'VV-Region', ST_GeogFromText($1), 'test') RETURNING id`, geom).
		Scan(&region); err != nil {
		t.Fatalf("zone region: %v", err)
	}
	mv := []int64{0, 0}
	for i, name := range []string{"VV-M1", "VV-M2"} {
		if err := pool.QueryRow(ctx,
			`INSERT INTO zones (country, level, parent_id, name, geom, source)
			 VALUES ('VV', 'municipality', $1, $2, ST_GeogFromText($3), 'test') RETURNING id`,
			region, name, geom).Scan(&mv[i]); err != nil {
			t.Fatalf("zone municipality: %v", err)
		}
	}
	m1, m2 = mv[0], mv[1]
	if _, err := pool.Exec(ctx, `
		INSERT INTO attribute_registry
			(country, key, data_type, allowed_values, used_in_pricing, label_ru, label_en, source_evidence)
		VALUES ('VV', 'has_garage', 'bool', '["false","true"]', TRUE, 'Гараж', 'Garage', 'test')`); err != nil {
		t.Fatalf("attribute_registry: %v", err)
	}

	t.Cleanup(func() {
		// valuations удалятся каскадом за objects (FK ON DELETE CASCADE).
		_, _ = pool.Exec(context.Background(), `DELETE FROM objects WHERE country = 'VV'`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM zones WHERE country = 'VV' AND parent_id IS NOT NULL`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM zones WHERE country = 'VV' AND parent_id IS NULL`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM attribute_registry WHERE country = 'VV'`)
	})

	// zone, currency, propertyType — *T: nil → NULL в БД (проверка
	// веток no_zone / no_currency / no_type, ТЗ v4-2 §4.10.1).
	insertObj := func(zone *int64, daysAgo int, priceMinor *int64, currency *string, propertyType *string) (int64, error) {
		var id int64
		area := 50 + 2*float64(daysAgo)
		err := pool.QueryRow(ctx, `
			INSERT INTO objects (country, property_country, deal_type, zone_id, address, area_sqm, current_price_minor, currency, property_type, attributes, first_seen_at, last_seen_at)
			-- property_country: объект рынка VV (C20: корпус модели —
			-- объекты страны, по умолчанию колонки 'CZ' здесь не годится).
			VALUES ('VV', 'VV', 'sale', $1, $2, $3, $4, $5, $6, $7::jsonb,
			        now() - ($8 || ' days')::interval, now() - ($8 || ' days')::interval)
			RETURNING id`,
			zone, fmt.Sprintf("Test addr %d", daysAgo), area, priceMinor, currency, propertyType,
			`{"has_garage":"true"}`, fmt.Sprintf("%d", daysAgo)).Scan(&id)
		return id, err
	}

	// PSM = 1500 EUR/м², детерминированный шум ±5% (от i), месяц варьируется
	// через сдвиг дат (i*2 дней назад).
	for i := 0; i < sampleCount; i++ {
		zone := m2
		if i < 2 {
			zone = m1 // первые 2 — в малой зоне (fallback)
		}
		area := 50 + 2*float64(i*2)
		psm := 1500.0 * (1 + 0.01*float64((i*7)%11-5))
		price := int64(math.Round(psm * area * 100))
		if _, err := insertObj(intPtr(zone), i*2, &price, strPtr("EUR"), strPtr("flat")); err != nil {
			t.Fatalf("объект %d: %v", i, err)
		}
	}
	id, err := insertObj(intPtr(m2), 90, nil, strPtr("EUR"), strPtr("flat"))
	if err != nil {
		t.Fatalf("объект без цены: %v", err)
	}
	noPrice = id
	id, err = insertObj(intPtr(m2), 91, intPtr(int64(math.Round(1500.0*68*100))), strPtr("CZK"), strPtr("flat"))
	if err != nil {
		t.Fatalf("объект CZK: %v", err)
	}
	czkObj = id
	id, err = insertObj(intPtr(m2), 92, intPtr(int64(math.Round(1500.0*69*100))), nil, strPtr("flat"))
	if err != nil {
		t.Fatalf("объект с NULL валютой: %v", err)
	}
	noCur = id
	// Обязательный набор (ТЗ v4-2 §4.10.1): без зоны → no_zone,
	// без типа → no_type (остальные признаки на месте).
	id, err = insertObj(nil, 93, intPtr(int64(math.Round(1500.0*70*100))), strPtr("EUR"), strPtr("flat"))
	if err != nil {
		t.Fatalf("объект без зоны: %v", err)
	}
	noZone = id
	id, err = insertObj(intPtr(m2), 94, intPtr(int64(math.Round(1500.0*71*100))), strPtr("EUR"), nil)
	if err != nil {
		t.Fatalf("объект без типа: %v", err)
	}
	noType = id
	return m1, m2, noPrice, czkObj, noCur, noZone, noType
}

func intPtr(v int64) *int64 { return &v }

func strPtr(s string) *string { return &s }

// Позиция end-to-end: строки valuations по всем активным объектам;
// у выборочных — отклонение с интервалом; у остальных — NULL с причиной.
func TestRunLiveFull(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, _, noPrice, czkObj, noCur, noZone, noType := vvSetup(t, pool, 40)

	rep, err := Run(ctx, pool, vvCfg(), "VV", "sale")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Rejected {
		t.Fatalf("модель отклонена: %s", rep.Reason)
	}
	if rep.InSample != 40 || rep.ExcludedCurrency != 2 {
		t.Fatalf("выборка: in_sample=%d (ожидал 40), excluded_currency=%d (ожидал 2: CZK + NULL валюта)",
			rep.InSample, rep.ExcludedCurrency)
	}
	if rep.Wrote != 45 {
		t.Fatalf("записано %d строк, ожидал 45 (40 в выборке + no_price + CZK + NULL валюта + no_zone + no_type)", rep.Wrote)
	}
	// Покрытие обязательного набора (ТЗ v4-2 §4.10.1): зона и тип
	// отсутствуют по одному объекту из 45 — 44/45.
	if got := rep.RequiredCoverage["zone"]; math.Abs(got-44.0/45) > 0.001 {
		t.Errorf("RequiredCoverage[zone] = %v, ожидал ≈%v", got, 44.0/45)
	}
	// Цена отсутствует только у объекта no_price: объекты CZK и с NULL
	// валютой цену имеют (валюта исключает их из выборки, а покрытие
	// обязательного набора считается по всем 45 активным) — 44/45.
	if got := rep.RequiredCoverage["price"]; math.Abs(got-44.0/45) > 0.001 {
		t.Errorf("RequiredCoverage[price] = %v, ожидал ≈%v", got, 44.0/45)
	}

	var total, withDev, withInterval, fb int
	err = pool.QueryRow(ctx, `
		SELECT count(*),
		       count(price_deviation),
		       count(*) FILTER (WHERE price_deviation IS NOT NULL
		                         AND interval_low_minor < predicted_price_minor
		                         AND predicted_price_minor < interval_high_minor),
		       count(*) FILTER (WHERE zone_fallback)
		FROM valuations v
		WHERE v.object_id IN (SELECT id FROM objects WHERE country = 'VV')`).
		Scan(&total, &withDev, &withInterval, &fb)
	if err != nil {
		t.Fatalf("сводка valuations: %v", err)
	}
	if total != 45 || withDev != 40 || withInterval != 40 {
		t.Fatalf("сводка: total=%d with_dev=%d with_interval=%d (ожидал 45/40/40)",
			total, withDev, withInterval)
	}
	if fb != 2 {
		t.Fatalf("zone_fallback: %d, ожидал 2 (малая зона M1)", fb)
	}

	var reason string
	if err := pool.QueryRow(ctx, `SELECT deviation_null_reason FROM valuations WHERE object_id = $1`, noPrice).
		Scan(&reason); err != nil {
		t.Fatalf("строка no_price: %v", err)
	}
	if reason != "no_price" {
		t.Fatalf("причина для объекта без цены: %q", reason)
	}
	if err := pool.QueryRow(ctx, `SELECT deviation_null_reason FROM valuations WHERE object_id = $1`, czkObj).
		Scan(&reason); err != nil {
		t.Fatalf("строка CZK: %v", err)
	}
	if reason != "currency_mismatch" {
		t.Fatalf("причина для объекта в CZK: %q", reason)
	}
	if err := pool.QueryRow(ctx, `SELECT deviation_null_reason FROM valuations WHERE object_id = $1`, noCur).
		Scan(&reason); err != nil {
		t.Fatalf("строка NULL-валюты: %v", err)
	}
	if reason != "no_currency" {
		t.Fatalf("причина для объекта с NULL валютой: %q", reason)
	}
	if err := pool.QueryRow(ctx, `SELECT deviation_null_reason FROM valuations WHERE object_id = $1`, noZone).
		Scan(&reason); err != nil {
		t.Fatalf("строка no_zone: %v", err)
	}
	if reason != "no_zone" {
		t.Fatalf("причина для объекта без зоны: %q", reason)
	}
	if err := pool.QueryRow(ctx, `SELECT deviation_null_reason FROM valuations WHERE object_id = $1`, noType).
		Scan(&reason); err != nil {
		t.Fatalf("строка no_type: %v", err)
	}
	if reason != "no_type" {
		t.Fatalf("причина для объекта без типа: %q", reason)
	}
}

// Отказ end-to-end: мало данных — строки пишутся, все с NULL и причиной
// (критерий этапа 5: «NULL с причиной, а не число»).
func TestRunLiveInsufficient(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, _, _, _, _, _, _ = vvSetup(t, pool, 5) // только 5 объектов в выборке

	rep, err := Run(ctx, pool, vvCfg(), "VV", "sale")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.Rejected {
		t.Fatalf("модель не отклонена при n=5")
	}
	if !strings.HasPrefix(rep.Reason, "insufficient_observations") {
		t.Fatalf("причина: %q", rep.Reason)
	}
	if rep.Wrote != 10 { // 5 в выборке + no_price + CZK + NULL валюта + no_zone + no_type
		t.Fatalf("записано %d строк, ожидал 10", rep.Wrote)
	}
	var total, withDev int
	err = pool.QueryRow(ctx, `
		SELECT count(*), count(price_deviation)
		FROM valuations v
		WHERE v.object_id IN (SELECT id FROM objects WHERE country = 'VV')`).
		Scan(&total, &withDev)
	if err != nil {
		t.Fatalf("сводка valuations: %v", err)
	}
	if total != 10 || withDev != 0 {
		t.Fatalf("сводка: total=%d with_dev=%d (ожидал 10/0)", total, withDev)
	}
}

// ТЗ v4-2 §4.10.1: покрытие обязательного набора ниже порога — честный
// отказ required_coverage ДО построения модели; строки пишутся по всем
// объектам (выборочные — с причиной отказа, остальные — со своими).
func TestRunLiveRequiredCoverage(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	geom := "MULTIPOLYGON(((0 0,1 0,1 1,0 1,0 0)))"
	var region, mun int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO zones (country, level, name, geom, source)
		 VALUES ('VV', 'region', 'VV-RQ-Region', ST_GeogFromText($1), 'test') RETURNING id`, geom).
		Scan(&region); err != nil {
		t.Fatalf("zone region: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO zones (country, level, parent_id, name, geom, source)
		 VALUES ('VV', 'municipality', $1, 'VV-RQ-M', ST_GeogFromText($2), 'test') RETURNING id`,
		region, geom).Scan(&mun); err != nil {
		t.Fatalf("zone municipality: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM objects WHERE country = 'VV'`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM zones WHERE country = 'VV' AND parent_id IS NOT NULL`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM zones WHERE country = 'VV' AND parent_id IS NULL`)
	})

	// 4 объекта: 1 полный (с зоной) + 3 без зоны → покрытие зоны
	// 1/4 = 0.25 < 0.5 (остальные признаки — 100% по 4 объектам).
	insert := func(zone *int64, daysAgo int, price int64) (int64, error) {
		var id int64
		err := pool.QueryRow(ctx, `
			INSERT INTO objects (country, property_country, deal_type, zone_id, address, area_sqm, current_price_minor, currency, property_type, attributes, first_seen_at, last_seen_at)
			-- property_country: объект рынка VV (C20), см. vvSetup.
			VALUES ('VV', 'VV', 'sale', $1, $2, 60, $3, 'EUR', 'flat', '{}'::jsonb,
			        now() - ($4 || ' days')::interval, now() - ($4 || ' days')::interval)
			RETURNING id`,
			zone, fmt.Sprintf("RQ addr %d", daysAgo), price, fmt.Sprintf("%d", daysAgo)).Scan(&id)
		return id, err
	}
	complete, err := insert(intPtr(mun), 1, 1500*60*100)
	if err != nil {
		t.Fatalf("полный объект: %v", err)
	}
	var noZones []int64
	for i := 0; i < 3; i++ {
		id, err := insert(nil, 2+i, 1500*60*100+int64(i))
		if err != nil {
			t.Fatalf("объект без зоны %d: %v", i, err)
		}
		noZones = append(noZones, id)
	}

	rep, err := Run(ctx, pool, vvCfg(), "VV", "sale")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.Rejected {
		t.Fatalf("модель не отклонена при покрытии зоны 0.25")
	}
	if !strings.HasPrefix(rep.Reason, "required_coverage") {
		t.Fatalf("причина: %q", rep.Reason)
	}
	if got := rep.RequiredCoverage["zone"]; math.Abs(got-0.25) > 0.001 {
		t.Errorf("RequiredCoverage[zone] = %v, ожидал 0.25", got)
	}
	if rep.InSample != 1 || rep.Wrote != 4 {
		t.Fatalf("in_sample=%d wrote=%d (ожидал 1/4)", rep.InSample, rep.Wrote)
	}
	var reason string
	if err := pool.QueryRow(ctx, `SELECT deviation_null_reason FROM valuations WHERE object_id = $1`, complete).
		Scan(&reason); err != nil {
		t.Fatalf("строка полного объекта: %v", err)
	}
	if !strings.HasPrefix(reason, "required_coverage") {
		t.Fatalf("причина полного объекта: %q", reason)
	}
	var noZoneRows int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM valuations WHERE object_id = ANY($1) AND deviation_null_reason = 'no_zone'`, noZones).
		Scan(&noZoneRows); err != nil {
		t.Fatalf("сводка no_zone: %v", err)
	}
	if noZoneRows != 3 {
		t.Errorf("строк no_zone: %d, ожидал 3", noZoneRows)
	}
}

// C1: zonePenalty — структура штрафа блока зон:
//   - группа (зоны с родителем G + колонка-представитель G) с k ≥ 2 —
//     центрирование (I − 11ᵀ/k): отклонения от среднего группы
//     штрафуются, среднее группы (эффект уровня-родителя) — в ядре;
//   - одиночная группа — диагональная единица (сжатие к интерцепту);
//   - без одиночной группы — якорь обратимости на первой колонке.
func TestZonePenaltyStructure(t *testing.T) {
	approx := func(got, want float64) bool { return math.Abs(got-want) < 1e-12 }

	// Случай 1: зоны 1, 2 с родителем 9 + представитель 9 (группа, k=3)
	// и одиночная зона 7 (родителя нет).
	z1, z2, r, s := int64(1), int64(2), int64(9), int64(7)
	groups := []*int64{&z1, &z2, &s, &r} // порядок как sortedUniqueGroups: 1,2,7,9
	parent := map[int64]*int64{z1: &r, z2: &r}
	p := zonePenalty(groups, parent)
	// Отсортированные зоны: [1,2,7,9] → кол0=z1, кол1=z2, кол2=7 (одиночная),
	// кол3=9 (представитель). Группа 9 = {кол0, кол1, кол3}, k=3.
	// (I − 11ᵀ/3): diag 2/3, offdiag −1/3 внутри группы.
	for _, c := range []int{0, 1, 3} {
		if !approx(p.At(c, c), 2.0/3) {
			t.Errorf("диагональ группы, кол%d: %v (ожидал 2/3)", c, p.At(c, c))
		}
	}
	for _, ij := range [][2]int{{0, 1}, {0, 3}, {1, 3}} {
		if !approx(p.At(ij[0], ij[1]), -1.0/3) {
			t.Errorf("внедиагональ группы (%d,%d): %v (ожидал −1/3)", ij[0], ij[1], p.At(ij[0], ij[1]))
		}
	}
	// Одиночная зона 7 — колонка 2: единица, связь с группой отсутствует.
	if !approx(p.At(2, 2), 1) || !approx(p.At(0, 2), 0) || !approx(p.At(1, 2), 0) || !approx(p.At(2, 3), 0) {
		t.Errorf("одиночная группа: diag=%v, off=%v %v %v (ожидал 1, 0, 0, 0)",
			p.At(2, 2), p.At(0, 2), p.At(1, 2), p.At(2, 3))
	}
	// Среднее группы — в ядре: P·(1,1,0,1)ᵀ = 0.
	v := [4]float64{1, 1, 0, 1}
	for i := 0; i < 4; i++ {
		sv := 0.0
		for j := 0; j < 4; j++ {
			sv += p.At(i, j) * v[j]
		}
		if !approx(sv, 0) {
			t.Errorf("среднее группы не в ядре: (Pv)[%d] = %v", i, sv)
		}
	}

	// Случай 2: одиночной группы нет (одна группа, k=2) — якорь
	// обратимости: (I − 11ᵀ/2) + e0·e0ᵀ: diag(3/2, 1/2), offdiag −1/2.
	z3, z4 := int64(3), int64(4)
	parent2 := map[int64]*int64{z3: &r, z4: &r}
	groups2 := []*int64{&z3, &z4}
	p2 := zonePenalty(groups2, parent2)
	if !approx(p2.At(0, 0), 1.5) || !approx(p2.At(1, 1), 0.5) || !approx(p2.At(0, 1), -0.5) {
		t.Errorf("якорь: diag=%v %v, off=%v (ожидал 3/2, 1/2, −1/2)", p2.At(0, 0), p2.At(1, 1), p2.At(0, 1))
	}

	checkPenaltyShape(t, p)
	checkPenaltyShape(t, p2)
}

// checkPenaltyShape — симметрия и неотрицательная полуопределённость
// (32 фиксированных случайных вектора: qᵀPq ≥ 0).
func checkPenaltyShape(t *testing.T, p *mat.Dense) {
	t.Helper()
	n, _ := p.Dims()
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			if math.Abs(p.At(i, j)-p.At(j, i)) > 1e-12 {
				t.Fatalf("симметрия: P[%d][%d]=%v, P[%d][%d]=%v", i, j, p.At(i, j), j, i, p.At(j, i))
			}
		}
	}
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 32; trial++ {
		q := make([]float64, n)
		for i := range q {
			q[i] = rng.Float64() - 0.5
		}
		sv := 0.0
		for i := 0; i < n; i++ {
			pi := 0.0
			for j := 0; j < n; j++ {
				pi += p.At(i, j) * q[j]
			}
			sv += q[i] * pi
		}
		if sv < -1e-9 {
			t.Fatalf("PSD нарушен: qᵀPq = %v", sv)
		}
	}
}
