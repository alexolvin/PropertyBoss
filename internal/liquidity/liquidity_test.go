package liquidity

// Этап 7 (ТЗ §9): unit-тесты чистых функций — построение
// person-period выборки, защита от утечки будущих цен, фит
// логистической регрессии и метрики валидации. Без DB.

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/config"
	"propertyboss/internal/db"
)

func TestObjIntervalsTargets(t *testing.T) {
	base := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	ds := NewDataset([]Obj{
		{ID: 1, Status: "active", Start: base, End: base.AddDate(0, 0, 21)},
		{ID: 2, Status: "delisted", Start: base, End: base.AddDate(0, 0, 10)},
		{ID: 3, Status: "delisted", Start: base, End: base.AddDate(0, 0, 7)},
		{ID: 4, Status: "delisted", Start: base, End: base.AddDate(0, 0, 2)},
	})
	got := map[int][]Period{}
	for _, p := range ds.Periods {
		got[p.Obj] = append(got[p.Obj], p)
	}
	targets := func(i int) (n, ones int) {
		for _, p := range got[i] {
			n++
			ones += p.Target
		}
		return
	}
	// Активен 21 день: 3 интервала, целей нет, последний — ровно до End.
	if n, ones := targets(0); n != 3 || ones != 0 {
		t.Fatalf("obj 0: интервалов=%d целей=%d, ждали 3/0", n, ones)
	}
	if !got[0][2].End.Equal(base.AddDate(0, 0, 21)) {
		t.Fatalf("obj 0: последний интервал должен заканчиваться в End, %v", got[0][2].End)
	}
	// Ушёл на 10-й день: единичная цель в интервале 1 (7,14].
	if n, ones := targets(1); n != 2 || ones != 1 {
		t.Fatalf("obj 1: интервалов=%d целей=%d, ждали 2/1", n, ones)
	}
	if got[1][1].Target != 1 || got[1][0].Target != 0 {
		t.Fatalf("obj 1: цель должна быть в интервале 1, получили %v", got[1])
	}
	// Ушёл ровно на 7-й день: цель в интервале 0 (граница включена).
	if n, ones := targets(2); n != 1 || ones != 1 {
		t.Fatalf("obj 2: интервалов=%d целей=%d, ждали 1/1", n, ones)
	}
	// Ушёл на 2-й день: один интервал, цель в нём.
	if n, ones := targets(3); n != 1 || ones != 1 {
		t.Fatalf("obj 3: интервалов=%d целей=%d, ждали 1/1", n, ones)
	}
}

// ТЗ §9.2: предикторы цены — только по строкам change_at <= начала
// интервала; подстановка будущей цены запрещена.
func TestPriceFeaturesAtNoLeakage(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	o := Obj{Prices: []PricePoint{
		{At: base, Minor: 100},
		{At: base.AddDate(0, 0, 10), Minor: 80},
		{At: base.AddDate(0, 0, 20), Minor: 85},
	}}
	cases := []struct {
		at        time.Time
		r, s, inc int
		d         float64
	}{
		{base.Add(-1 * time.Hour), 0, 0, 0, 0}, // цены ещё не было
		{base, 0, 0, 0, 0},                     // стартовая цена известна
		{base.AddDate(0, 0, 5), 0, 5, 0, 0},    // 5 дней без изменения
		{base.AddDate(0, 0, 12), 1, 2, 0, 20},  // одно снижение на 20%
		{base.AddDate(0, 0, 25), 1, 5, 1, 15},  // снижение + повышение
	}
	for i, c := range cases {
		r, d, s, inc := priceFeaturesAt(o, c.at)
		if r != c.r || s != c.s || inc != c.inc || math.Abs(d-c.d) > 1e-9 {
			t.Errorf("case %d: получено r=%d d=%.4f s=%d inc=%d, ждали r=%d d=%.4f s=%d inc=%d",
				i, r, d, s, inc, c.r, c.d, c.s, c.inc)
		}
	}
}

func TestValDevAt(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	o := Obj{ValDevs: []ValPoint{
		{At: base, Deviation: -0.1},
		{At: base.AddDate(0, 0, 30), Deviation: 0.2},
	}}
	if v := valDevAt(o, base.Add(-1*time.Hour)); v != nil {
		t.Fatalf("до первой оценки — nil, получено %v", *v)
	}
	if v := valDevAt(o, base.AddDate(0, 0, 15)); v == nil || *v != -0.1 {
		t.Fatalf("на 15-й день — первая оценка, получено %v", v)
	}
	if v := valDevAt(o, base.AddDate(0, 0, 45)); v == nil || *v != 0.2 {
		t.Fatalf("на 45-й день — вторая оценка, получено %v", v)
	}
}

// IRLS восстанавливает известные коэффициенты на синтетических данных.
func TestFitLogisticRecoversSignal(t *testing.T) {
	rnd := rand.New(rand.NewSource(42))
	const n = 4000
	want := []float64{0, 1.0, -0.8, 0.5}
	x := make([][]float64, n)
	y := make([]int, n)
	for i := 0; i < n; i++ {
		x1, x2, x3 := rnd.NormFloat64(), rnd.NormFloat64(), rnd.NormFloat64()
		x[i] = []float64{1, x1, x2, x3}
		eta := want[1]*x1 + want[2]*x2 + want[3]*x3
		p := 1 / (1 + math.Exp(-eta))
		if rnd.Float64() < p {
			y[i] = 1
		}
	}
	beta, ok := fitLogistic(x, y)
	if !ok {
		t.Fatal("фит не сошёлся на сходимых данных")
	}
	for j := range want {
		if math.Abs(beta[j]-want[j]) > 0.25 {
			t.Errorf("beta[%d] = %.4f, ждали ≈ %.2f", j, beta[j], want[j])
		}
	}
}

// Проверенные вручную значения (вывод см. комментарий BrierDecompose).
func TestBrierDecompose(t *testing.T) {
	// A: n=2, две группы. Brier = (0.04+0.04)/2 = 0.04;
	// reliability = 0.04, resolution = 0.25, uncertainty = 0.25.
	dec, brier := BrierDecompose([]float64{0.2, 0.8}, []int{0, 1})
	if math.Abs(brier-0.04) > 1e-9 || math.Abs(dec.Reliability-0.04) > 1e-9 ||
		math.Abs(dec.Resolution-0.25) > 1e-9 || math.Abs(dec.Uncertainty-0.25) > 1e-9 {
		t.Errorf("case A: brier=%.4f dec=%+v, ждали 0.04 / {0.04 0.25 0.25}", brier, dec)
	}
	// B: все предсказания 0.5, половина событий. Brier = Rel = Res =
	// Uncertainty = 0.25 (каждая строка — свой дециль).
	dec, brier = BrierDecompose([]float64{0.5, 0.5, 0.5, 0.5}, []int{1, 1, 0, 0})
	if math.Abs(brier-0.25) > 1e-9 || math.Abs(dec.Reliability-0.25) > 1e-9 ||
		math.Abs(dec.Resolution-0.25) > 1e-9 || math.Abs(dec.Uncertainty-0.25) > 1e-9 {
		t.Errorf("case B: brier=%.4f dec=%+v, ждали 0.25 / {0.25 0.25 0.25}", brier, dec)
	}
	// C: идеально откалиброванные 2 блока по 50 (p=0.2/0.8, доли 0.2/0.8):
	// reliability = 0, Brier = uncertainty − resolution = 0.25 − 0.09 = 0.16.
	pred := make([]float64, 0, 100)
	ty := make([]int, 0, 100)
	for i := 0; i < 50; i++ {
		pred = append(pred, 0.2)
		if i%10 < 2 {
			ty = append(ty, 1) // 2 из 10 в каждом дециле
		} else {
			ty = append(ty, 0)
		}
	}
	for i := 0; i < 50; i++ {
		pred = append(pred, 0.8)
		if i%10 < 8 {
			ty = append(ty, 1) // 8 из 10 в каждом дециле
		} else {
			ty = append(ty, 0)
		}
	}
	dec, brier = BrierDecompose(pred, ty)
	if math.Abs(brier-0.16) > 1e-9 || dec.Reliability > 1e-9 ||
		math.Abs(dec.Resolution-0.09) > 1e-9 || math.Abs(dec.Uncertainty-0.25) > 1e-9 {
		t.Errorf("case C: brier=%.4f dec=%+v, ждали 0.16 / {0 0.09 0.25}", brier, dec)
	}
}

func TestCIndex(t *testing.T) {
	cases := []struct {
		name      string
		y         []int
		p         []float64
		wantC     float64
		wantPairs int
	}{
		{"perfect", []int{1, 1, 0, 0}, []float64{0.9, 0.8, 0.2, 0.1}, 1.0, 4},
		{"reversed", []int{1, 1, 0, 0}, []float64{0.1, 0.2, 0.8, 0.9}, 0.0, 4},
		{"all ties", []int{1, 0, 1, 0}, []float64{0.5, 0.5, 0.5, 0.5}, 0.5, 4},
		{"mixed", []int{1, 0, 0, 1}, []float64{0.9, 0.4, 0.6, 0.1}, 0.5, 4},
		{"no events", []int{0, 0, 0}, []float64{0.1, 0.2, 0.3}, 0, 0},
	}
	for _, c := range cases {
		ci, comparable := CIndex(c.p, c.y)
		if c.wantPairs == 0 {
			if comparable != 0 {
				t.Errorf("%s: comparable=%d, ждали 0", c.name, comparable)
			}
			continue
		}
		if comparable != c.wantPairs || math.Abs(ci-c.wantC) > 1e-9 {
			t.Errorf("%s: C=%.4f пар=%d, ждали %.4f / %d", c.name, ci, comparable, c.wantC, c.wantPairs)
		}
	}
}

func TestCalibrationDeciles(t *testing.T) {
	// n=20: ровно 10 децилей по 2 строки, предсказание неубывает.
	pred := make([]float64, 20)
	y := make([]int, 20)
	for i := 0; i < 20; i++ {
		pred[i] = float64(i) / 20
		if i%2 == 0 {
			y[i] = 1
		}
	}
	d := CalibrationDeciles(pred, y)
	if len(d) != 10 {
		t.Fatalf("децилей=%d, ждали 10", len(d))
	}
	total := 0
	for i, c := range d {
		total += c.N
		if c.N != 2 {
			t.Errorf("дециль %d: n=%d, ждали 2", i+1, c.N)
		}
		if i > 0 && c.Predicted < d[i-1].Predicted {
			t.Errorf("децили не отсортированы по предсказанию: %v < %v", c, d[i-1])
		}
	}
	if total != 20 {
		t.Errorf("сумма n = %d, ждали 20", total)
	}
}

// horizonProb — 1 − ∏(1−h_k): week/month сдвигаются по шагам.
func TestHorizonProb(t *testing.T) {
	names := colLayout(nil, nil)
	m := &Model{Names: names}
	m.Beta = make([]float64, len(names))
	// week_12plus (индекс 1+maxWeekBin) → logit 3: h = 0.75,
	// остальные недели: h = 0.5.
	m.Beta[1+maxWeekBin] = math.Log(3)
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	// С 0-й недели: 4 шага по 0.5 → 1 − 0.5^4 = 0.9375.
	got := m.horizonProb(FeatRow{Week: 0, Month: 3}, start, 28)
	if math.Abs(got-(1-0.5*0.5*0.5*0.5)) > 1e-9 {
		t.Errorf("с 0-й недели: P(28д) = %.6f, ждали 0.9375", got)
	}
	// С 12-й: 2 шага по h=0.75 → 1 − (1−0.75)² = 1 − 0.25² = 0.9375.
	got = m.horizonProb(FeatRow{Week: 12, Month: 3}, start, 14)
	if math.Abs(got-(1-0.25*0.25)) > 1e-9 {
		t.Errorf("с 12-й недели: P(14д) = %.6f, ждали 0.9375", got)
	}
	// Месяцы не влияют (их коэффициенты нулевые).
	got = m.horizonProb(FeatRow{Week: 0, Month: 12}, start, 7)
	if math.Abs(got-0.5) > 1e-9 {
		t.Errorf("P(7д) = %.6f, ждали 0.5", got)
	}
}

// --- Live-тесты (PB_TEST_DSN) ---
//
// Песочница — страна 'LL' (отдельно от 'TT' у этапов 4–6: пакеты
// гонятся параллельно и не должны видеть объекты друг друга).

const liqCountry = "LL"

func liqPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PB_TEST_DSN")
	if dsn == "" {
		t.Skip("PB_TEST_DSN не задан — live-тест пропускается")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	unlock, err := db.LiveTestLock(context.Background(), pool)
	if err != nil {
		t.Fatalf("live lock: %v", err)
	}
	// t.Cleanup идёт LIFO: тестовые сетыпы регистрируют cleanup ПОСЛЕ
	// unlock — чистка работает под локом, лок отпущен последним.
	t.Cleanup(pool.Close)
	t.Cleanup(unlock)
	return pool
}

func liqCfg(minEvents int, holdoutRatio float64) *config.Config {
	// Пороги намеренно ниже реальных: тестовая выборка мала.
	// MaxCalibDev = 1.0 — гейт калибровки по определению не
	// срабатывает (отклонение ≤ 1): тест проверяет механику прогона.
	cfg := &config.Config{}
	cfg.Dashboard.Countries = []string{liqCountry}
	cfg.Dashboard.DealTypes = []string{"sale"}
	cfg.Dashboard.MarketCurrencies = map[string]string{liqCountry: "EUR"}
	cfg.Liquidity.MinEvents = minEvents
	cfg.Liquidity.MaxCalibDev = 1.0
	cfg.Liquidity.HorizonDays = 30
	cfg.Liquidity.HoldoutRatio = holdoutRatio
	return cfg
}

// liqNow — «сейчас» тестовых данных (опора liqDaysAgo). По умолчанию —
// реальное время; обучающий тест фиксирует его (месяц признаков —
// календарный, при относительных датах выборка зависела бы от даты
// запуска — см. TestRunLiveTrainPredict).
var liqNow = time.Now().UTC()

func liqDaysAgo(n int) time.Time {
	return liqNow.AddDate(0, 0, -n)
}

// liqTestNow — фиксированная дата обучающего сценария. Признак month —
// календарный месяц начала интервала (ТЗ §9.2), поэтому при относительных
// датах (liqDaysAgo) состав design-матрицы зависит от дня запуска, а
// сходимость IRLS на недоопределённом синтетическом сценарии (8 событий на
// ~30 параметров) — от состава (flake: на 6 из 8 проверенных дат 2026
// фит сходится лишь на части дат). Фиксация даты делает тест
// детерминированным: он проверяет механику разбиения/обучения/публикации,
// а не капризы численного решателя. «Сейчас» продакшен-кода — реальное
// время (nowUTC не трогается).
var liqTestNow = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// pinLiqNow — фиксирует liqNow и nowUTC на liqTestNow на время теста
// (восстановление в Cleanup).
func pinLiqNow(t *testing.T) {
	t.Helper()
	oldNowUTC, oldLiqNow := nowUTC, liqNow
	t.Cleanup(func() { nowUTC, liqNow = oldNowUTC, oldLiqNow })
	nowUTC = func() time.Time { return liqTestNow }
	liqNow = liqTestNow
}

// liqCleanup — регистрация LIFO-очистки ДО вставок: после всех
// вставок удаляет строки моделей, прогнозы и объекты страны.
// Сразу же (под live-локом) чистит и остатки предыдущего
// упавшего прогона — тесты идемпотентны.
func liqCleanup(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `DELETE FROM notifications
		WHERE kind = 'liquidity_model' AND payload->>'country' = $1`, liqCountry)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM notifications
			WHERE kind = 'liquidity_model' AND payload->>'country' = $1`, liqCountry)
		_, _ = pool.Exec(ctx, `DELETE FROM liquidity_models WHERE country = $1`, liqCountry)
		_, _ = pool.Exec(ctx, `DELETE FROM liquidity_estimates
			WHERE object_id IN (SELECT id FROM objects WHERE country = $1)`, liqCountry)
		_, _ = pool.Exec(ctx, `DELETE FROM objects WHERE country = $1`, liqCountry)
	})
}

// liqObj — объект с историей цен: базовая цена на старте, снижение
// на 20% на 3-й день (типичный паттерн: цена «живёт» до ухода).
// delistAfter > 0 — объект снят с рынка через N дней.
func liqObj(t *testing.T, pool *pgxpool.Pool, i, startDaysAgo, delistAfter int) int64 {
	t.Helper()
	ctx := context.Background()
	status, delistedAt := "active", any(nil)
	endDays := startDaysAgo
	if delistAfter > 0 {
		status = "delisted"
		endDays = startDaysAgo - delistAfter
		delistedAt = liqDaysAgo(endDays)
	}
	var id int64
	err := pool.QueryRow(ctx, `
		INSERT INTO objects (country, deal_type, address, area_sqm,
		                      current_price_minor, currency, status,
		                      first_seen_at, last_seen_at, delisted_at)
		VALUES ($1, 'sale', $2, 60, 100000000, 'EUR', $3, $4, $5, $6)
		RETURNING id`,
		liqCountry, fmt.Sprintf("LiQ addr %d", i), status,
		liqDaysAgo(startDaysAgo), liqDaysAgo(endDays), delistedAt).Scan(&id)
	if err != nil {
		t.Fatalf("объект %d: %v", i, err)
	}
	for j, pp := range []struct {
		daysAgo int
		minor   int64
	}{
		{startDaysAgo, 100000000},
		{startDaysAgo - 3, 80000000},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO price_history (object_id, source_id, price_minor, currency, change_at)
			VALUES ($1, 'test', $2, 'EUR', $3)`, id, pp.minor, liqDaysAgo(pp.daysAgo)); err != nil {
			t.Fatalf("объект %d, цена %d: %v", i, j, err)
		}
	}
	return id
}

// ТЗ §9.3, §14.5: завершённых наблюдений меньше min_events — честный
// холодный старт: прогнозы NULL с причиной insufficient_history (для
// ушедших — delisted), строка модели записана, метрики NULL.
func TestRunLiveInsufficientHistory(t *testing.T) {
	pool := liqPool(t)
	liqCleanup(t, pool)
	ctx := context.Background()
	// 3 завершённых (< min_events=100) + 2 активных.
	liqObj(t, pool, 1, 40, 14)
	liqObj(t, pool, 2, 50, 21)
	liqObj(t, pool, 3, 60, 28)
	liqObj(t, pool, 4, 20, 0)
	liqObj(t, pool, 5, 30, 0)

	rep, err := Run(ctx, pool, liqCfg(100, 0.5), liqCountry, "sale")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Status != "insufficient_history" {
		t.Fatalf("status = %q (reject %q), ждали insufficient_history", rep.Status, rep.RejectReason)
	}
	if rep.CompletedEvents != 3 {
		t.Fatalf("завершённых событий = %d, ждали 3", rep.CompletedEvents)
	}
	if rep.Estimated != 0 || rep.Nulls != 5 {
		t.Fatalf("estimated=%d nulls=%d, ждали 0/5", rep.Estimated, rep.Nulls)
	}

	var status string
	var nEvents, minEvents int
	var calJSON []byte
	var maxDev *float64
	err = pool.QueryRow(ctx, `
		SELECT status, n_completed_events, min_events, calibration, max_calib_dev
		FROM liquidity_models
		WHERE country = $1 AND deal_type = 'sale'
		ORDER BY computed_at DESC, id DESC
		LIMIT 1`, liqCountry).Scan(&status, &nEvents, &minEvents, &calJSON, &maxDev)
	if err != nil {
		t.Fatalf("строка модели: %v", err)
	}
	if status != "insufficient_history" || nEvents != 3 || minEvents != 100 {
		t.Fatalf("строка модели: status=%q events=%d min=%d", status, nEvents, minEvents)
	}
	if calJSON != nil {
		t.Fatalf("calibration = %s, ждали NULL", calJSON)
	}
	if maxDev != nil {
		t.Fatalf("max_calib_dev = %v, ждали NULL", *maxDev)
	}

	var total, withHazard, delReason, insReason int
	err = pool.QueryRow(ctx, `
		SELECT count(*),
		       count(hazard_probability),
		       count(*) FILTER (WHERE null_reason = 'delisted'),
		       count(*) FILTER (WHERE null_reason = 'insufficient_history')
		FROM liquidity_estimates
		WHERE object_id IN (SELECT id FROM objects WHERE country = $1)`, liqCountry).
		Scan(&total, &withHazard, &delReason, &insReason)
	if err != nil {
		t.Fatalf("сводка прогнозов: %v", err)
	}
	if total != 5 || withHazard != 0 || delReason != 3 || insReason != 2 {
		t.Fatalf("прогнозы: total=%d with_h=%d del=%d ins=%d (ждали 5/0/3/2)",
			total, withHazard, delReason, insReason)
	}

	// ТЗ §9.4: «оповещения по ликвидности не отправляются», пока модель
	// не откалибрована — строки в очереди нет вовсе.
	var nNotes int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications
		 WHERE kind = 'liquidity_model' AND payload->>'country' = $1`,
		liqCountry).Scan(&nNotes); err != nil {
		t.Fatalf("чтение очереди: %v", err)
	}
	if nNotes != 0 {
		t.Errorf("insufficient_history: уведомлений %d, ждали 0", nNotes)
	}
}

// ТЗ §9.2–9.4 end-to-end: временное разбиение (обучение до T),
// обучение на завершённых событиях, публикация, метрики в строке
// модели; активным — вероятности в [0,1], ушедшим — NULL 'delisted'.
func TestRunLiveTrainPredict(t *testing.T) {
	pool := liqPool(t)
	liqCleanup(t, pool)
	pinLiqNow(t) // детерминированная дата сценария (см. liqTestNow)
	ctx := context.Background()

	// 10 завершённых: старт 70..88 дней назад, уход 14..42 дня
	// после старта ((i*7)%35 зацикливается: 0,7,14,21,28,0,…).
	// Окно 88 дней, T = середина (holdout 0.5) — 44 дня назад:
	// уходы 56,51,46,66,61,56,51,46 дней назад — в обучающем окне
	// (8), 41 и 36 — в проверочном (2).
	for i := 0; i < 10; i++ {
		liqObj(t, pool, 100+i, 70+2*i, 14+(i*7)%35)
	}
	// 30 активных: старт 8..66 дней назад — правый цензурированный
	// ноль, заполняют проверочное окно (≥20 интервалов).
	for i := 0; i < 30; i++ {
		liqObj(t, pool, 200+i, 8+2*i, 0)
	}

	rep, err := Run(ctx, pool, liqCfg(2, 0.5), liqCountry, "sale")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Status != "published" {
		t.Fatalf("status = %q, reject = %q — ждали published", rep.Status, rep.RejectReason)
	}
	if rep.CompletedEvents != 10 {
		t.Fatalf("завершённых событий = %d, ждали 10", rep.CompletedEvents)
	}
	if rep.TrainEvents != 8 {
		t.Fatalf("событий в обучающем окне = %d, ждали 8", rep.TrainEvents)
	}
	if rep.NTrain == 0 || rep.NTest < 20 {
		t.Fatalf("train/test = %d/%d — временное разбиение не работает", rep.NTrain, rep.NTest)
	}
	if rep.NTrain+rep.NTest != rep.NPeriods {
		t.Fatalf("train+test = %d != n_person_periods = %d (все объекты надёжны)",
			rep.NTrain+rep.NTest, rep.NPeriods)
	}
	if rep.MaxCalibDev == nil || rep.Brier == nil {
		t.Fatalf("метрики NULL у опубликованной модели")
	}
	if rep.Estimated != 30 || rep.Nulls != 10 {
		t.Fatalf("estimated=%d nulls=%d, ждали 30/10", rep.Estimated, rep.Nulls)
	}

	// ТЗ §9.4: метрики и параметры сохраняются вместе с версией.
	var status string
	var nParams int
	var calJSON []byte
	var maxDev, brier *float64
	err = pool.QueryRow(ctx, `
		SELECT status, n_params, calibration, max_calib_dev, brier_score
		FROM liquidity_models
		WHERE country = $1 AND deal_type = 'sale' AND model_version = $2`,
		liqCountry, rep.ModelVersion).Scan(&status, &nParams, &calJSON, &maxDev, &brier)
	if err != nil {
		t.Fatalf("строка модели: %v", err)
	}
	if status != "published" || nParams <= 0 || len(calJSON) == 0 || maxDev == nil || brier == nil {
		t.Fatalf("строка модели: status=%q n_params=%d calib_len=%d maxDev=%v brier=%v",
			status, nParams, len(calJSON), maxDev, brier)
	}

	// ТЗ §9.3: ушедшим — NULL 'delisted'; активным — вероятность
	// в [0,1] с версией модели и числом событий в обучении.
	var total, withH, nullH, delOK, activeBad int
	err = pool.QueryRow(ctx, `
		SELECT count(*),
		       count(hazard_probability),
		       count(*) FILTER (WHERE hazard_probability IS NULL),
		       count(*) FILTER (WHERE hazard_probability IS NULL AND null_reason = 'delisted'),
		       count(*) FILTER (WHERE hazard_probability IS NOT NULL
		                           AND (hazard_probability < 0 OR hazard_probability > 1
		                                OR model_version <> $2
		                                OR events_in_training <> $3))
		FROM liquidity_estimates
		WHERE object_id IN (SELECT id FROM objects WHERE country = $1)`,
		liqCountry, rep.ModelVersion, rep.TrainEvents).
		Scan(&total, &withH, &nullH, &delOK, &activeBad)
	if err != nil {
		t.Fatalf("сводка прогнозов: %v", err)
	}
	if total != 40 || withH != 30 || nullH != 10 || delOK != 10 || activeBad != 0 {
		t.Fatalf("прогнозы: total=%d with_h=%d null=%d del=%d bad_active=%d (ждали 40/30/10/10/0)",
			total, withH, nullH, delOK, activeBad)
	}

	// ТЗ §9.4: публикация — ровно одно уведомление в очереди.
	// (Дальше — только проверки БЕЗ model_version в WHERE: второй
	// Run в ту же минуту даст строку с той же версией.)
	var nNotes int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications
		 WHERE kind = 'liquidity_model' AND payload->>'country' = $1`,
		liqCountry).Scan(&nNotes); err != nil {
		t.Fatalf("чтение очереди: %v", err)
	}
	if nNotes != 1 {
		t.Fatalf("публикация: уведомлений %d, ждали 1", nNotes)
	}
	// Повторный прогон уже опубликованной модели — без дубля.
	if _, err := Run(ctx, pool, liqCfg(2, 0.5), liqCountry, "sale"); err != nil {
		t.Fatalf("второй Run: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications
		 WHERE kind = 'liquidity_model' AND payload->>'country' = $1`,
		liqCountry).Scan(&nNotes); err != nil {
		t.Fatalf("чтение очереди (2-й прогон): %v", err)
	}
	if nNotes != 1 {
		t.Errorf("после 2-го прогона: уведомлений %d, ждали 1 (без дубля)", nNotes)
	}
}
