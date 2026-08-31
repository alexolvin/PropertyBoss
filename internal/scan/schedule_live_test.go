package scan

// Живые тесты адаптивного расписания (этап 11, ТЗ §10) против живой БД
// (тот же паттерн, что scan_test.go: PB_TEST_DSN не задан — тест
// пропускается). Тесты прогоняют Runner.Run с включённым расписанием
// (запись выхода, бэкофф по капче) и функции пакета schedule напрямую
// (ComputeWeights, Plan, RecoverCooldowns). Каждый сценарий — на своём
// песочном источнике: промышленные данные (bazos-reality) не трогает.
// Файл в пакете scan (не schedule): scan уже импортирует schedule,
// обратное направление было бы циклом.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/config"
	"propertyboss/internal/db"
	"propertyboss/internal/schedule"
)

// Песочные источники расписания (отдельные от pb-scan-test и от
// промышленных).
const (
	schedSrcYield  = "pb-sched-yield"
	schedSrcWarm   = "pb-sched-warm"
	schedSrcBack   = "pb-sched-back"
	schedSrcBudget = "pb-sched-budget"
)

// seedSchedSource — источник (CZ, active) + одна активная конфигурация
// поиска + окна на все дни недели, сутки (0–24) в Europe/Prague.
// Возвращает id конфигурации.
func seedSchedSource(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string, maxPerHour int) int64 {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO sources (id, name, domain, country, deal_types, kind, access_policy, state)
		VALUES ($1, 'sched test source', 'sched.invalid', 'CZ', ARRAY['sale'], 'simple',
		        '{"note":"песочница теста расписания"}', 'active')`, id); err != nil {
		t.Fatalf("seed source %s: %v", id, err)
	}
	var cfgID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO search_configs (source_id, country, deal_type, currency)
		VALUES ($1, 'CZ', 'sale', 'CZK') RETURNING id`, id).Scan(&cfgID); err != nil {
		t.Fatalf("seed search_configs %s: %v", id, err)
	}
	for dow := 0; dow <= 6; dow++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO scan_windows (source_id, country, day_of_week, hour_start, hour_end, timezone, max_requests_per_hour)
			VALUES ($1, 'CZ', $2, 0, 24, 'Europe/Prague', $3)`, id, dow, maxPerHour); err != nil {
			t.Fatalf("seed scan_windows %s dow=%d: %v", id, dow, err)
		}
	}
	return cfgID
}

// sweepSchedSources — уборка всех песочных источников (порядок по ВК).
func sweepSchedSources(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	args := []any{schedSrcYield, schedSrcWarm, schedSrcBack, schedSrcBudget}
	queries := []string{
		`DELETE FROM scan_yield WHERE source_id IN ($1, $2, $3, $4)`,
		`DELETE FROM scan_windows WHERE source_id IN ($1, $2, $3, $4)`,
		`DELETE FROM price_history WHERE object_id IN
		 (SELECT object_id FROM object_listings WHERE source_id IN ($1, $2, $3, $4))`,
		`DELETE FROM objects WHERE id IN
		 (SELECT object_id FROM object_listings WHERE source_id IN ($1, $2, $3, $4))`,
		`DELETE FROM object_listings WHERE source_id IN ($1, $2, $3, $4)`,
		`DELETE FROM raw_listings WHERE source_id IN ($1, $2, $3, $4)`,
		`DELETE FROM scan_runs WHERE source_id IN ($1, $2, $3, $4)`,
		`DELETE FROM search_configs WHERE source_id IN ($1, $2, $3, $4)`,
		`DELETE FROM sources WHERE id IN ($1, $2, $3, $4)`,
	}
	for i, q := range queries {
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Logf("sweepSched[%d]: %v", i, err)
		}
	}
}

// schedSettings — параметры для живых тестов: бэкофф с базой 1 минута
// (дольные часы не ждать), остальные значения — как в продакшен-конфиге.
func schedSettings(minObsForTuning int) *schedule.Settings {
	return &schedule.Settings{
		ExplorationFraction: 0.1,
		MAWindowDays:        14,
		MinObsForTuning:     minObsForTuning,
		BackoffBase:         time.Minute,
		BackoffMultiplier:   2,
		BackoffMax:          72 * time.Hour,
		RecoveryStep:        1.5,
		MinRateFactor:       0.25,
		Timezones:           map[string]string{"CZ": "Europe/Prague"},
	}
}

var schedDedupe = map[string]config.DedupeParams{
	"CZ": {RadiusM: 50, AreaTolerancePct: 10, AddressSimilarity: 0.9},
}

// schedListing — минимальная запись (без атрибутов и координат).
func schedListing(extID string, priceMinor int64, posted time.Time) Listing {
	return Listing{
		ExternalID:   extID,
		URL:          "https://sched.invalid/" + extID,
		PriceMinor:   &priceMinor,
		Currency:     ptr("CZK"),
		Rooms:        ptr(2),
		PropertyType: ptr("byt"),
		Address:      ptr("Testova ul. " + extID + ", Praha"),
		PostedAt:     &posted,
	}
}

// openTestPool — общий каркас: PB_TEST_DSN, пул, Close в Cleanup
// (зарегистрирован ДО sweep'а вызывающего — LIFO: чистка, потом Close).
func openTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PB_TEST_DSN")
	if dsn == "" {
		t.Skip("PB_TEST_DSN не задан — тест требует живого Postgres")
	}
	pool, err := db.Open(t.Context(), dsn)
	if err != nil {
		t.Fatalf("открытие БД: %v", err)
	}
	unlock, err := db.LiveTestLock(t.Context(), pool)
	if err != nil {
		t.Fatalf("live lock: %v", err)
	}
	// t.Cleanup идёт LIFO: вызывающий регистрирует sweep ПОСЛЕ
	// unlock — чистка работает под локом, лок отпущен последним.
	t.Cleanup(func() { pool.Close() })
	t.Cleanup(unlock)
	return pool
}

// pragueNow — текущее время в поясе страны песочницы (ТЗ §10.2: слоты —
// в поясе страны объявления).
func pragueNow(t *testing.T) (time.Time, *time.Location) {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		t.Fatalf("Europe/Prague: %v", err)
	}
	return time.Now().In(loc), loc
}

// TestLiveScheduleYieldAndWeights — ТЗ §10.3: выход ПОЛНЫХ сканов
// попадает в scan_yield, после порога min_obs_for_tuning веса окон
// вычисляются пропорционально скользящему среднему: окно с данными —
// 1.0, окна без данных — ε-пол (не отключаются насовсем).
func TestLiveScheduleYieldAndWeights(t *testing.T) {
	pool := openTestPool(t)
	ctx := t.Context()
	t.Cleanup(func() { sweepSchedSources(t, context.Background(), pool) })
	sweepSchedSources(t, ctx, pool)
	cfgID := seedSchedSource(t, ctx, pool, schedSrcYield, 10)

	s := schedSettings(2) // порог 2 полных скана — быстро выйти из warming
	runner := NewRunner(pool, schedDedupe, s)
	cfg, err := LoadSearchConfig(ctx, pool, cfgID)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	posted := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	l1, l2 := schedListing("sy-1", 111000000, posted), schedListing("sy-2", 222000000, posted)

	rep1 := runner.Run(ctx, schedSrcYield, &fakeConn{id: schedSrcYield, listings: []Listing{l1, l2}}, cfg)
	if rep1.Completeness != "complete" || rep1.Err != nil || rep1.NewObjects != 2 {
		t.Fatalf("run1: completeness=%s new=%d err=%v (ожидали complete/2)", rep1.Completeness, rep1.NewObjects, rep1.Err)
	}
	rep2 := runner.Run(ctx, schedSrcYield, &fakeConn{id: schedSrcYield, listings: []Listing{l1, l2}}, cfg)
	if rep2.Completeness != "complete" || rep2.NewObjects != 0 {
		t.Fatalf("run2: completeness=%s new=%d (ожидали complete/0 — всё уже есть)", rep2.Completeness, rep2.NewObjects)
	}

	// Выход: 2 полных скана, 2 новых. Если тест пересёк часовую границу,
	// это 2 строки (2 слота) — инвариант в суммах.
	var (
		rows     int
		sumScans int
		sumNew   int
	)
	if err := pool.QueryRow(ctx, `
		SELECT count(*), coalesce(sum(scans),0), coalesce(sum(new_objects),0)
		FROM scan_yield WHERE source_id = $1`, schedSrcYield).
		Scan(&rows, &sumScans, &sumNew); err != nil {
		t.Fatalf("scan_yield: %v", err)
	}
	if sumScans != 2 || sumNew != 2 {
		t.Fatalf("scan_yield: scans=%d new=%d (ожидали 2/2)", sumScans, sumNew)
	}

	nowPrague, _ := pragueNow(t)
	windows, warming, err := schedule.ComputeWeights(ctx, pool, s, schedSrcYield)
	if err != nil {
		t.Fatalf("ComputeWeights: %v", err)
	}
	if warming {
		t.Fatal("warming_up=true при 2 полных сканах и пороге 2")
	}
	if len(windows) != 7 {
		t.Fatalf("окон = %d (ожидали 7)", len(windows))
	}
	todayDow := int(nowPrague.Weekday())
	var today, other *schedule.Window
	for i := range windows {
		switch {
		case windows[i].DOW == todayDow:
			today = &windows[i]
		case windows[i].DOW == (todayDow+1)%7:
			other = &windows[i]
		}
	}
	if today == nil || other == nil {
		t.Fatal("не найдены окно сегодняшнего дня или соседнего")
	}
	if today.Weight < 0.999 || today.Weight > 1.001 {
		t.Fatalf("вес окна дня = %.3f (ожидали ~1.0: единственный с данными)", today.Weight)
	}
	if today.SlotAvg == nil || *today.SlotAvg < 0.999 || *today.SlotAvg > 1.001 {
		t.Fatalf("slotAvg окна дня = %v (ожидали ~1.0: 2 новых / 2 скана)", today.SlotAvg)
	}
	if other.Weight < 0.099 || other.Weight > 0.101 {
		t.Fatalf("вес соседнего окна = %.3f (ожидали ε=0.1: данных нет)", other.Weight)
	}
	// Аудит: вес записан в scan_windows (ТЗ §10.2 «weight вычисляемый»).
	var dbWeight float64
	if err := pool.QueryRow(ctx,
		`SELECT weight FROM scan_windows WHERE id = $1`, today.ID).Scan(&dbWeight); err != nil {
		t.Fatalf("scan_windows.weight: %v", err)
	}
	if dbWeight < 0.999 || dbWeight > 1.001 {
		t.Fatalf("scan_windows.weight = %.3f (ожидали ~1.0)", dbWeight)
	}
}

// TestLiveScheduleWarmingUp — ТЗ §10.5: полных сканов меньше порога —
// веса равные (1.0), флаг warming_up: ранняя «адаптация» не выдаётся за
// настроенную по статистике.
func TestLiveScheduleWarmingUp(t *testing.T) {
	pool := openTestPool(t)
	ctx := t.Context()
	t.Cleanup(func() { sweepSchedSources(t, context.Background(), pool) })
	sweepSchedSources(t, ctx, pool)
	cfgID := seedSchedSource(t, ctx, pool, schedSrcWarm, 10)

	s := schedSettings(10) // 1 скан < 10 — warming
	runner := NewRunner(pool, schedDedupe, s)
	cfg, err := LoadSearchConfig(ctx, pool, cfgID)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	posted := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rep := runner.Run(ctx, schedSrcWarm, &fakeConn{
		id:       schedSrcWarm,
		listings: []Listing{schedListing("sw-1", 100000000, posted)},
	}, cfg)
	if rep.Completeness != "complete" {
		t.Fatalf("run: completeness=%s err=%v", rep.Completeness, rep.Err)
	}

	windows, warming, err := schedule.ComputeWeights(ctx, pool, s, schedSrcWarm)
	if err != nil {
		t.Fatalf("ComputeWeights: %v", err)
	}
	if !warming {
		t.Fatal("warming_up=false при 1 полном скане и пороге 10")
	}
	for _, w := range windows {
		if w.Weight < 0.999 || w.Weight > 1.001 {
			t.Fatalf("окно dow=%d weight=%.3f (ожидали 1.0 — warming_up)", w.DOW, w.Weight)
		}
	}
}

// TestLiveScheduleBackoff — ТЗ §10.4: капча → немедленно cooldown с
// экспоненциальным откатом (1 мин, затем 2 мин), rate_factor падает
// вдвое с поломом; полный скан → постепенное восстановление (×1.5 за
// скан, к 1.0). Кулдаун блокирует запуск нового скана.
func TestLiveScheduleBackoff(t *testing.T) {
	pool := openTestPool(t)
	ctx := t.Context()
	t.Cleanup(func() { sweepSchedSources(t, context.Background(), pool) })
	sweepSchedSources(t, ctx, pool)
	cfgID := seedSchedSource(t, ctx, pool, schedSrcBack, 10)

	s := schedSettings(10)
	runner := NewRunner(pool, schedDedupe, s)
	cfg, err := LoadSearchConfig(ctx, pool, cfgID)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	posted := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	captcha := &fakeConn{id: schedSrcBack, err: NewFail(FailCaptcha, errors.New("песочная капча"))}

	stateOf := func() (string, int, float64, *time.Time) {
		t.Helper()
		var ss schedule.SourceState
		err := pool.QueryRow(ctx, `
			SELECT id, country, state, cooldown_strikes, rate_factor, cooldown_until
			FROM sources WHERE id = $1`, schedSrcBack,
		).Scan(&ss.ID, &ss.Country, &ss.State, &ss.Strikes, &ss.RateFactor, &ss.CooldownUntil)
		if err != nil {
			t.Fatalf("чтение источника: %v", err)
		}
		return ss.State, ss.Strikes, ss.RateFactor, ss.CooldownUntil
	}
	expire := func() {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`UPDATE sources SET cooldown_until = now() - interval '1 minute' WHERE id = $1`,
			schedSrcBack); err != nil {
			t.Fatalf("ускорить истечение кулдауна: %v", err)
		}
	}
	// checkUntil — граница кулдауна в ожидаемой близости (±10 с: время
	// бэкоффа снимается в Run до запросов, часы могут слегка разойтись).
	checkUntil := func(until *time.Time, expected time.Duration) {
		t.Helper()
		if until == nil {
			t.Fatalf("cooldown_until = nil (ожидали +%.0f мин)", expected.Minutes())
		}
		delta := until.Sub(time.Now().Add(expected))
		if delta > 10*time.Second || delta < -10*time.Second {
			t.Fatalf("cooldown_until = %s, ожидали ≈ now+%.0f мин (расхождение %v)",
				until.Format("2006-01-02 15:04:05"), expected.Minutes(), delta)
		}
	}
	checkRate := func(rate, want float64) {
		t.Helper()
		if rate < want-0.001 || rate > want+0.001 {
			t.Fatalf("rate_factor = %.4f (ожидали %.4f)", rate, want)
		}
	}

	// 1. Капча → cooldown, strikes=1, кулдаун = base (1 мин), rate 1.0→0.5.
	rep1 := runner.Run(ctx, schedSrcBack, captcha, cfg)
	if rep1.Completeness != "failed" || rep1.FailureKind != FailCaptcha {
		t.Fatalf("run1: completeness=%s kind=%s (ожидали failed/captcha)", rep1.Completeness, rep1.FailureKind)
	}
	st, strikes, rate, until := stateOf()
	if st != "cooldown" || strikes != 1 {
		t.Fatalf("после капчи: state=%s strikes=%d (ожидали cooldown/1)", st, strikes)
	}
	checkRate(rate, 0.5)
	checkUntil(until, time.Minute)

	// 2. Запуск в кулдауне отклонён (и не создаёт scan_run, не трогает
	// strikes). Отказ — либо про состояние cooldown, либо про кулдаун.
	rep2 := runner.Run(ctx, schedSrcBack, captcha, cfg)
	msg := ""
	if rep2.Err != nil {
		msg = rep2.Err.Error()
	}
	if rep2.Err == nil || (!strings.Contains(msg, "cooldown") && !strings.Contains(msg, "кулдаун")) {
		t.Fatalf("run2: err=%v (ожидали отказ про кулдаун)", rep2.Err)
	}
	_, strikes2, rate2, _ := stateOf()
	var runs2 int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM scan_runs WHERE source_id = $1`, schedSrcBack).Scan(&runs2); err != nil {
		t.Fatalf("scan_runs count: %v", err)
	}
	if strikes2 != 1 || rate2 < 0.499 || rate2 > 0.501 || runs2 != 1 {
		t.Fatalf("отклонённый запуск изменил состояние: strikes=%d rate=%.3f runs=%d (ожидали 1/0.5/1)",
			strikes2, rate2, runs2)
	}

	// 3. Истечение → RecoverCooldowns возвращает в active (rate НЕ
	// восстанавливается скачком).
	expire()
	recovered, err := schedule.RecoverCooldowns(ctx, pool, time.Now())
	if err != nil {
		t.Fatalf("RecoverCooldowns: %v", err)
	}
	found := false
	for _, id := range recovered {
		if id == schedSrcBack {
			found = true
		}
	}
	if !found {
		t.Fatalf("RecoverCooldowns не вернул %s (вернул %v)", schedSrcBack, recovered)
	}
	st3, strikes3, rate3, until3 := stateOf()
	if st3 != "active" || strikes3 != 1 {
		t.Fatalf("после истечения: state=%s strikes=%d (ожидали active/1)", st3, strikes3)
	}
	// cooldown_until — историческая метка конца кулдауна: может остаться
	// сохранённой, но обязана быть в прошлом (иначе запуск снова
	// блокировался бы).
	if until3 != nil && until3.After(time.Now()) {
		t.Fatalf("cooldown_until в будущем после истечения: %s", until3.Format(time.RFC3339))
	}
	checkRate(rate3, 0.5)

	// 4. Вторая капча → strikes=2, кулдаун = base×2 (2 мин), rate 0.5→0.25 (пол).
	rep3 := runner.Run(ctx, schedSrcBack, captcha, cfg)
	if rep3.Completeness != "failed" {
		t.Fatalf("run3: completeness=%s (ожидали failed)", rep3.Completeness)
	}
	_, strikes4, rate4, until4 := stateOf()
	if strikes4 != 2 {
		t.Fatalf("после второй капчи: strikes=%d (ожидали 2)", strikes4)
	}
	checkRate(rate4, 0.25)
	checkUntil(until4, 2*time.Minute)

	// 5. Полный скан → strikes сброшены, rate поднялся на шаг (×1.5).
	expire()
	_, _ = schedule.RecoverCooldowns(ctx, pool, time.Now())
	sb1, sb2 := schedListing("sb-1", 300000000, posted), schedListing("sb-2", 301000000, posted)
	rep4 := runner.Run(ctx, schedSrcBack, &fakeConn{id: schedSrcBack, listings: []Listing{sb1, sb2}}, cfg)
	if rep4.Completeness != "complete" || rep4.NewObjects != 2 {
		t.Fatalf("run4: completeness=%s new=%d (ожидали complete/2)", rep4.Completeness, rep4.NewObjects)
	}
	st5, strikes5, rate5, _ := stateOf()
	if st5 != "active" || strikes5 != 0 {
		t.Fatalf("после полного скана: state=%s strikes=%d (ожидали active/0)", st5, strikes5)
	}
	checkRate(rate5, 0.25*1.5) // 0.375

	// 6. Ещё полный скан (0 новых — тоже достоверный выход) → ещё шаг.
	rep5 := runner.Run(ctx, schedSrcBack, &fakeConn{id: schedSrcBack, listings: []Listing{sb1, sb2}}, cfg)
	if rep5.Completeness != "complete" || rep5.NewObjects != 0 {
		t.Fatalf("run5: completeness=%s new=%d (ожидали complete/0)", rep5.Completeness, rep5.NewObjects)
	}
	_, _, rate6, _ := stateOf()
	checkRate(rate6, 0.25*1.5*1.5) // 0.5625 (NUMERIC(4,3) — ±0.001)
}

// TestLiveSchedulePlanBudget — ТЗ §10.4: потолок max_requests_per_hour ×
// rate_factor жёсткий — в текущем часовом слоте уже max запросов, и
// источник в плане не участвует; после освобождения бюджета —
// участвует с остатком.
func TestLiveSchedulePlanBudget(t *testing.T) {
	pool := openTestPool(t)
	ctx := t.Context()
	t.Cleanup(func() { sweepSchedSources(t, context.Background(), pool) })
	sweepSchedSources(t, ctx, pool)
	cfgID := seedSchedSource(t, ctx, pool, schedSrcBudget, 2) // max=2/час

	s := schedSettings(1)
	find := func(opts []schedule.Option) *schedule.Option {
		for i := range opts {
			if opts[i].SourceID == schedSrcBudget {
				return &opts[i]
			}
		}
		return nil
	}

	// Два запуска в текущем часовом слоте (max=2) — бюджет исчерпан.
	for i := 0; i < 2; i++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO scan_runs (source_id, search_config_id, started_at, completeness, finished_at)
			VALUES ($1, $2, now(), 'complete', now())`, schedSrcBudget, cfgID); err != nil {
			t.Fatalf("seed scan_runs: %v", err)
		}
	}
	opts, err := schedule.Plan(ctx, pool, s, time.Now())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if o := find(opts); o != nil {
		t.Fatalf("источник в плане при исчерпанном бюджете (остаток 0): %+v", o)
	}

	// Освободим один запрос.
	if _, err := pool.Exec(ctx, `
		DELETE FROM scan_runs WHERE id = (
			SELECT id FROM scan_runs WHERE source_id = $1 ORDER BY id DESC LIMIT 1)`,
		schedSrcBudget); err != nil {
		t.Fatalf("освобождение бюджета: %v", err)
	}
	now := time.Now()
	opts, err = schedule.Plan(ctx, pool, s, now)
	if err != nil {
		t.Fatalf("Plan (2): %v", err)
	}
	o := find(opts)
	if o == nil {
		t.Fatal("источника нет в плане после освобождения бюджета (остаток 1)")
	}
	if o.Remaining != 1 {
		t.Fatalf("остаток = %d (ожидали 1)", o.Remaining)
	}
	if o.ConfigID != cfgID {
		t.Fatalf("config id = %d (ожидали %d)", o.ConfigID, cfgID)
	}
	if !o.WarmingUp {
		t.Fatal("warming_up=false при 0 накопленных полных сканах")
	}
	tm := now.In(pragueTZ(t))
	if want := schedule.SlotKey(int(tm.Weekday()), tm.Hour()); o.SlotKey != want {
		t.Fatalf("слот %q (ожидали %q — текущий час в Europe/Prague)", o.SlotKey, want)
	}
}

// pragueTZ — Europe/Prague (без дублирования протекции pragueNow).
func pragueTZ(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		t.Fatalf("Europe/Prague: %v", err)
	}
	return loc
}
