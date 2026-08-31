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

	"propertyboss/internal/config"
	"propertyboss/internal/db"
)

// gauss — нормальный шум (Box-Muller) для синтетических выборок.
func gauss(rng *rand.Rand) float64 {
	u1 := 1 - rng.Float64()
	u2 := 1 - rng.Float64()
	return math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*u2)
}

// baseInput — пороги ТЗ §7.3, как в конфиге по умолчанию.
func baseInput() *ModelInput {
	return &ModelInput{
		MinObsPerParam: 10,
		MinObsPerZone:  30,
		MaxMissingRate: 0.5,
		KFold:          5,
		LambdaGrid:     []float64{0.01, 0.1, 1, 10, 100},
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

// ТЗ §7.3: n < min_obs_per_param × params — отказ.
// K = 1 + 0 зон + 4 (onehot) + 1 (area) + 11 (месяцы) = 17 → нужно ≥ 170.
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

// ТЗ §7.3: доля пропусков по ключевому атрибуту выше порога — отказ.
// K = 1 + 0 + 3 (onehot) + 1 + 11 = 16 → нужно ≥ 160; n=200 проходит,
// пропусков 120/200 = 0.6 > 0.5.
func TestFitAttributeMissingRate(t *testing.T) {
	in := baseInput()
	in.Attrs = []AttrSpec{{Key: "state", Kind: "onehot", Values: []string{"a", "b", "c"}}}
	for i := 0; i < 200; i++ {
		attrs := map[string]string{}
		if i >= 120 { // 120 из 200 без значения
			attrs["state"] = []string{"a", "b", "c"}[i%3]
		}
		in.Observations = append(in.Observations, Observation{
			ObjectID:   int64(i + 1),
			PriceMinor: int64(100000000 * (1 + 0.01*float64(i%10))),
			AreaSQM:    50 + 5*float64(i%20),
			AttrValues: attrs,
			Month:      1 + i%12,
		})
	}
	fit, err := Fit(in)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if !fit.Rejected {
		t.Fatalf("ожидался отказ, модель построилась")
	}
	if !strings.HasPrefix(fit.Reason, "attribute_missing_rate") {
		t.Fatalf("ожидалась причина attribute_missing_rate, получено %q", fit.Reason)
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
	return cfg
}

// vvSetup — зоны (регион + 2 муниципалитета), атрибут, объекты-подопытные.
// 40 объектов в M2 (большая зона, ≥ min_obs_per_zone), 2 в M1 (малая →
// zone_fallback на регион), 1 без цены, 1 в не рыночной валюте (CZK),
// 1 с NULL валютой (objects.currency — nullable; регрессия: скан в
// string падал, честная причина — no_currency).
// Возвращает id зон M1/M2 и id особых объектов; cleanup — в t.Cleanup.
func vvSetup(t *testing.T, pool *pgxpool.Pool, sampleCount int) (m1, m2, noPrice, czkObj, noCur int64) {
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

	// currency — *string: nil → NULL в БД (проверка ветки no_currency).
	insertObj := func(zone int64, daysAgo int, priceMinor *int64, currency *string) (int64, error) {
		var id int64
		area := 50 + 2*float64(daysAgo)
		err := pool.QueryRow(ctx, `
			INSERT INTO objects (country, deal_type, zone_id, address, area_sqm, current_price_minor, currency, attributes, first_seen_at, last_seen_at)
			VALUES ('VV', 'sale', $1, $2, $3, $4, $5, $6::jsonb,
			        now() - ($7 || ' days')::interval, now() - ($7 || ' days')::interval)
			RETURNING id`,
			zone, fmt.Sprintf("Test addr %d", daysAgo), area, priceMinor, currency,
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
		if _, err := insertObj(zone, i*2, &price, strPtr("EUR")); err != nil {
			t.Fatalf("объект %d: %v", i, err)
		}
	}
	id, err := insertObj(m2, 90, nil, strPtr("EUR"))
	if err != nil {
		t.Fatalf("объект без цены: %v", err)
	}
	noPrice = id
	id, err = insertObj(m2, 91, intPtr(int64(math.Round(1500.0*68*100))), strPtr("CZK"))
	if err != nil {
		t.Fatalf("объект CZK: %v", err)
	}
	czkObj = id
	id, err = insertObj(m2, 92, intPtr(int64(math.Round(1500.0*69*100))), nil)
	if err != nil {
		t.Fatalf("объект с NULL валютой: %v", err)
	}
	noCur = id
	return m1, m2, noPrice, czkObj, noCur
}

func intPtr(v int64) *int64 { return &v }

func strPtr(s string) *string { return &s }

// Позиция end-to-end: строки valuations по всем активным объектам;
// у выборочных — отклонение с интервалом; у остальных — NULL с причиной.
func TestRunLiveFull(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, _, noPrice, czkObj, noCur := vvSetup(t, pool, 40)

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
	if rep.Wrote != 43 {
		t.Fatalf("записано %d строк, ожидал 43 (40 в выборке + no_price + CZK + NULL валюта)", rep.Wrote)
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
	if total != 43 || withDev != 40 || withInterval != 40 {
		t.Fatalf("сводка: total=%d with_dev=%d with_interval=%d (ожидал 43/40/40)",
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
}

// Отказ end-to-end: мало данных — строки пишутся, все с NULL и причиной
// (критерий этапа 5: «NULL с причиной, а не число»).
func TestRunLiveInsufficient(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, _, _, _, _ = vvSetup(t, pool, 5) // только 5 объектов в выборке

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
	if rep.Wrote != 8 { // 5 в выборке + no_price + CZK + NULL валюта
		t.Fatalf("записано %d строк, ожидал 8", rep.Wrote)
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
	if total != 8 || withDev != 0 {
		t.Fatalf("сводка: total=%d with_dev=%d (ожидал 8/0)", total, withDev)
	}
}
