// Package doctor — слой инвариантов над живой БД (волна 09.08, ответ на
// вопрос «почему проверка пропустила грубые ошибки»).
//
// Тест пишется ПОСЛЕ того, как дефект найден, и кодирует «этого бага
// больше нет»; в зазоре между тестами рождается следующая ошибка.
// Doctor — обратное: он проверяет, что конвейер данных должен
// держать ВСЕГДА — зеркала колонок, покрытие, сердечник сканера,
// очередь уведомлений, расписание, состояние модели. Каждый чек —
// именованный инвариант с порогами из конфига (C23: пороги не
// плавают).
//
// Первый живой прогон (09.08) подтвердил, что слой работает: поймал
// два реальных дефекта — 55 активных объектов без детальной страницы
// (дренаж источника, ждёт C7) и расписание с нулём активных конфигов
// поиска (источник активен, сканов нет — план пустой).
//
// Doctor — read-only: никаких изменений БД. Exit code через
// cmd/pb: 0 — нет FAIL, 1 — есть.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/config"
	"propertyboss/internal/extract"
	"propertyboss/internal/ratelimit"
)

// Status — результат чека. FAIL — нарушен инвариант (exit 1).
type Status string

const (
	OK   Status = "OK"
	FAIL Status = "FAIL"
	INFO Status = "INFO"
)

// Check — один именованный инвариант.
type Check struct {
	Name   string
	Status Status
	Detail string
}

// Report — результат прогона doctor.
type Report struct {
	country string
	Checks  []Check
}

func (r *Report) add(name string, st Status, detail string) {
	r.Checks = append(r.Checks, Check{Name: name, Status: st, Detail: detail})
}

// HasFail — есть ли нарушенный инвариант.
func (r Report) HasFail() bool {
	for _, c := range r.Checks {
		if c.Status == FAIL {
			return true
		}
	}
	return false
}

// String — человекочитаемый отчёт (по одной строке на чек).
func (r Report) String() string {
	var b strings.Builder
	if r.country == "web" {
		// Чек раздачи (TZ UI v5 U04) не ходит в БД — заголовок не врёт
		// «над живой БД».
		fmt.Fprintf(&b, "doctor: web — инвариант раздачи (без БД)\n")
	} else {
		fmt.Fprintf(&b, "doctor: %s — слой инвариантов над живой БД\n", r.country)
	}
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "%-4s %-28s %s\n", c.Status, c.Name, c.Detail)
	}
	return b.String()
}

func ts(t time.Time) string { return t.Format("02.01.2006 15:04") }

// Run — все инвариантные чеки по живой БД. country — рабочий корпус
// (property_country); для свежих объектов допустимо не иметь
// детальной страницы первые 24 часа (они легитимно в очереди enrich).
func Run(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, country string) (*Report, error) {
	corp := fmt.Sprintf("o.country='%s' AND o.status='active' AND o.property_country='%s'", country, country)
	rep := &Report{country: country}

	if err := checkCorpus(ctx, pool, rep, country); err != nil {
		return rep, err
	}
	if err := checkMirrors(ctx, pool, rep, corp); err != nil {
		return rep, err
	}
	if err := checkNotModeled(ctx, pool, rep, corp, cfg.Valuation.MaxNotModeledShare); err != nil {
		return rep, err
	}
	if err := checkLost(ctx, pool, rep, corp); err != nil {
		return rep, err
	}
	if err := checkSchedule(ctx, pool, rep); err != nil {
		return rep, err
	}
	if err := checkBlocked(ctx, pool, rep); err != nil {
		return rep, err
	}
	if err := checkNotifs(ctx, pool, rep); err != nil {
		return rep, err
	}
	if err := checkValuations(ctx, pool, rep); err != nil {
		return rep, err
	}
	checkLimiter(rep, cfg.Source.RequestIntervalMS)
	// Девятый чек — инвариант раздачи (TZ UI v5 U04): без БД, read-only.
	checkWeb(rep, cfg)
	// Десятый и одиннадцатый чеки — бэкап и тест восстановления (ТЗ-R1 R0.3, R0.4).
	checkBackup(rep, cfg)
	checkRestore(rep, cfg)
	// Двенадцатый чек — доступность LLM-сервера (ТЗ-R1 R1.5).
	checkLLM(ctx, rep, cfg)
	return rep, nil
}

// checkCorpus — знаменатель с происхождением (C24: метрика обязана
// нести, ЧТО это за популяция). INFO — состав, не нарушение.
func checkCorpus(ctx context.Context, pool *pgxpool.Pool, rep *Report, country string) error {
	var active, corpus, foreign, delisted int
	err := pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE status='active'),
       count(*) FILTER (WHERE status='active' AND property_country=$1),
       count(*) FILTER (WHERE status='active' AND (property_country IS NULL OR property_country <> $1)),
       count(*) FILTER (WHERE status='delisted')
FROM objects WHERE country=$1`, country).
		Scan(&active, &corpus, &foreign, &delisted)
	if err != nil {
		return err
	}
	rep.add("corpus", INFO, fmt.Sprintf(
		"active=%d (корпус property_country=%s: %d, иностранные/не определены: %d), delisted=%d",
		active, country, corpus, foreign, delisted))
	return nil
}

// checkMirrors — зеркальные колонки objects (area_sqm, rooms, seller_type)
// обязаны совпадать с attributes JSONB (урок C14: разрыв 208/170/1865
// прожил две волны невидимым). Три счётчика на атрибут:
// (1) значение в колонке, но ключа нет в JSONB;
// (2) ключ в JSONB, но колонка NULL;
// (3) значения различаются.
func checkMirrors(ctx context.Context, pool *pgxpool.Pool, rep *Report, corp string) error {
	q := `
SELECT 'area_sqm',
  count(*) FILTER (WHERE o.area_sqm IS NOT NULL AND NOT (o.attributes ? 'area_sqm')),
  count(*) FILTER (WHERE o.area_sqm IS NULL AND (o.attributes ? 'area_sqm')),
  count(*) FILTER (WHERE o.area_sqm IS NOT NULL AND (o.attributes ? 'area_sqm')
      AND (o.attributes->>'area_sqm')::numeric IS DISTINCT FROM o.area_sqm)
FROM objects o WHERE ` + corp + `
UNION ALL
SELECT 'rooms',
  count(*) FILTER (WHERE o.rooms IS NOT NULL AND NOT (o.attributes ? 'rooms')),
  count(*) FILTER (WHERE o.rooms IS NULL AND (o.attributes ? 'rooms')),
  count(*) FILTER (WHERE o.rooms IS NOT NULL AND (o.attributes ? 'rooms')
      AND (o.attributes->>'rooms')::int IS DISTINCT FROM o.rooms)
FROM objects o WHERE ` + corp + `
UNION ALL
SELECT 'seller_type',
  count(*) FILTER (WHERE o.seller_type IS NOT NULL AND NOT (o.attributes ? 'seller_type')),
  count(*) FILTER (WHERE o.seller_type IS NULL AND (o.attributes ? 'seller_type')),
  count(*) FILTER (WHERE o.seller_type IS NOT NULL AND (o.attributes ? 'seller_type')
      AND o.seller_type IS DISTINCT FROM o.attributes->>'seller_type')
FROM objects o WHERE ` + corp
	rows, err := pool.Query(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	type m struct {
		name        string
		colOnly, jsOnly, mismatch int64
	}
	var ms []m
	for rows.Next() {
		var x m
		if err := rows.Scan(&x.name, &x.colOnly, &x.jsOnly, &x.mismatch); err != nil {
			return err
		}
		ms = append(ms, x)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	bad := false
	var parts []string
	for _, x := range ms {
		parts = append(parts, fmt.Sprintf("%s %d/%d/%d", x.name, x.colOnly, x.jsOnly, x.mismatch))
		if x.colOnly+x.jsOnly+x.mismatch > 0 {
			bad = true
		}
	}
	st := OK
	detail := strings.Join(parts, ", ")
	if bad {
		st = FAIL
		detail += " — разрыв зеркала (урок C14)"
	}
	rep.add("mirrors", st, detail)
	return nil
}

// checkNotModeled — доля объектов без уровня геокода (выводимый флаг
// not_modeled = geo_confidence IS NULL; зеркальной колонки нет — урок
// C14). Порог по последствию, не по проценту с уровнем (C25, ТЗ
// v4-2 §1.2): объект с флагом оценок не показывает; рост доли выше
// порога — дефект и разбор.
func checkNotModeled(ctx context.Context, pool *pgxpool.Pool, rep *Report, corp string, maxShare float64) error {
	var nm, total int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE o.geo_confidence IS NULL), count(*)
FROM objects o WHERE `+corp).Scan(&nm, &total); err != nil {
		return err
	}
	share := 0.0
	if total > 0 {
		share = float64(nm) / float64(total)
	}
	st := OK
	detail := fmt.Sprintf("%d/%d = %.2f%% (порог %.2f%%)", nm, total, share*100, maxShare*100)
	if total > 0 && share > maxShare {
		st = FAIL
		detail += " — доля выше порога (C25): разбор и причина"
	}
	rep.add("not_modeled", st, detail)
	return nil
}

// checkLost — активные объекты корпуса без детальной страницы. Первые
// 24 часа после first_seen_at допустимо (очередь enrich), старше —
// объект либо утонул в источнике, либо дренаж не дошёл до него; в
// модели он сидит как not_modeled и занимает место в знаменателе.
// Класс 55 объектов (08.31) — именно здесь.
func checkLost(ctx context.Context, pool *pgxpool.Pool, rep *Report, corp string) error {
	var n int
	var oldest *time.Time
	err := pool.QueryRow(ctx, `
SELECT count(*), min(o.first_seen_at)
FROM objects o WHERE `+corp+`
  AND o.detail_fetched_at IS NULL
  AND o.first_seen_at < now() - interval '24 hours'`).
		Scan(&n, &oldest)
	if err != nil {
		return err
	}
	if n == 0 {
		rep.add("lost", OK, "0 — очередь enrich не копит")
		return nil
	}
	failDetail := fmt.Sprintf("%d активных объектов без детальной страницы >24 ч", n)
	if oldest != nil {
		failDetail += fmt.Sprintf(" (самый старый first_seen %s)", ts(*oldest))
	}
	failDetail += " — дренаж или утонувшие в источнике; ждёт C7"

	// C36: если все источники lost-объектов намеренно paused — это не
	// противоречие, дренажа не будет. Связь — object_listings (таблицы
	// object_links нет: 0011 создаёт object_listings).
	rows, err := pool.Query(ctx, `
SELECT DISTINCT s.state, s.paused_at, s.pause_reason
FROM objects o
JOIN object_listings ol ON ol.object_id = o.id
JOIN sources s ON s.id = ol.source_id
WHERE `+corp+`
  AND o.detail_fetched_at IS NULL
  AND o.first_seen_at < now() - interval '24 hours'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type linked struct {
		state  string
		paused *time.Time
		reason *string
	}
	var srcs []linked
	for rows.Next() {
		var s linked
		if err := rows.Scan(&s.state, &s.paused, &s.reason); err != nil {
			return err
		}
		srcs = append(srcs, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	allPaused := len(srcs) > 0
	for _, s := range srcs {
		if s.state != "paused" {
			allPaused = false
			break
		}
	}
	if allPaused {
		detail := fmt.Sprintf("%d активных объектов без детальной страницы >24 ч — источники paused", n)
		s0 := srcs[0]
		if s0.paused != nil {
			detail += fmt.Sprintf(" с %s", ts(*s0.paused))
		}
		if s0.reason != nil && *s0.reason != "" {
			detail += ": " + *s0.reason
		}
		rep.add("lost", INFO, detail)
		return nil
	}
	rep.add("lost", FAIL, failDetail)
	return nil
}

// checkSchedule — сердечник сканера. Источник активен, а активного
// конфига поиска нет — план расписания пуст, система стоит (флаг
// волны 09.08: config 11 выключен, сканов нет с 08.31). Последний
// скан старше 48 ч при активном конфиге — источник молчит.
func checkSchedule(ctx context.Context, pool *pgxpool.Pool, rep *Report) error {
	rows, err := pool.Query(ctx, `
SELECT id, state, cooldown_strikes, cooldown_until, paused_at, pause_reason
FROM sources ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type src struct {
		id          string
		state       string
		strikes     int
		cooldownUnt *time.Time
		pausedAt    *time.Time
		pauseReason *string
	}
	var ss []src
	for rows.Next() {
		var s src
		if err := rows.Scan(&s.id, &s.state, &s.strikes, &s.cooldownUnt, &s.pausedAt, &s.pauseReason); err != nil {
			return err
		}
		ss = append(ss, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, s := range ss {
		name := "schedule:" + s.id
		switch s.state {
		case "paused":
			detail := "paused"
			if s.pausedAt != nil {
				detail = fmt.Sprintf("paused с %s", ts(*s.pausedAt))
			}
			if s.pauseReason != nil && *s.pauseReason != "" {
				detail += ": " + *s.pauseReason
			}
			rep.add(name, INFO, detail)
		case "active":
			var cfgCount int
			if err := pool.QueryRow(ctx, `
SELECT count(*) FROM search_configs WHERE source_id=$1 AND active`, s.id).
				Scan(&cfgCount); err != nil {
				return err
			}
			if cfgCount == 0 {
				rep.add(name, FAIL, "активный источник без активного конфига поиска — план расписания пуст, скан не запускается")
				continue
			}
			var last *time.Time
			if err := pool.QueryRow(ctx, `
SELECT max(started_at) FROM scan_runs WHERE source_id=$1`, s.id).Scan(&last); err != nil {
				return err
			}
			if last == nil {
				rep.add(name, FAIL, fmt.Sprintf("активных конфигов %d, но сканов не было ни одного", cfgCount))
				continue
			}
			age := time.Since(*last)
			if age > 48*time.Hour {
				rep.add(name, FAIL, fmt.Sprintf("последний скан %s (%.0f ч назад > 48 ч)", ts(*last), age.Hours()))
				continue
			}
			rep.add(name, OK, fmt.Sprintf("последний скан %s (%.1f ч назад), активных конфигов %d", ts(*last), age.Hours(), cfgCount))
		default: // cooldown и прочие
			detail := fmt.Sprintf("state=%s, strikes=%d", s.state, s.strikes)
			if s.cooldownUnt != nil {
				detail += fmt.Sprintf(", cooldown до %s", ts(*s.cooldownUnt))
			}
			rep.add(name, INFO, detail)
		}
	}
	return nil
}

// checkBlocked — ТЗ-SRC-CZ-1 A8: 3 подряд 403/429/челлендж → sources.state=blocked,
// doctor FAIL blocked:<id>. Не blocked — OK.
func checkBlocked(ctx context.Context, pool *pgxpool.Pool, rep *Report) error {
	rows, err := pool.Query(ctx, `SELECT id, state FROM sources ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, state string
		if err := rows.Scan(&id, &state); err != nil {
			return err
		}
		name := "blocked:" + id
		if state == "blocked" {
			rep.add(name, FAIL, "state=blocked — circuit breaker; повтор не раньше 24 ч")
			continue
		}
		rep.add(name, OK, "state="+state)
	}
	return rows.Err()
}

// checkNotifs — очередь уведомлений не должна застывать: pending
// старше 5 минут — класс дефекта C19.2 (алерт о сбое, не доставленный
// в сломанной очереди). failed-строки — история, для оператора.
func checkNotifs(ctx context.Context, pool *pgxpool.Pool, rep *Report) error {
	var stale, pending, failed int
	err := pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE status='pending' AND created_at < now() - interval '5 minutes'),
       count(*) FILTER (WHERE status='pending'),
       count(*) FILTER (WHERE status='failed')
FROM notifications`).Scan(&stale, &pending, &failed)
	if err != nil {
		return err
	}
	detail := fmt.Sprintf("pending=%d (стальных >5 мин: %d), failed=%d (история)", pending, stale, failed)
	if stale > 0 {
		rep.add("notifs", FAIL, detail+" — очередь застыла (класс C19.2)")
		return nil
	}
	rep.add("notifs", OK, detail)
	return nil
}

// checkValuations — состояние последней модели: версия, свежесть,
// доля NULL-предсказаний, R². INFO — для чтения человеком; R² всегда
// подписывается «внутривыборочная» (честный статус, C21).
func checkValuations(ctx context.Context, pool *pgxpool.Pool, rep *Report) error {
	var version string
	var n int
	var newest *time.Time
	err := pool.QueryRow(ctx, `
SELECT model_version, count(*), max(computed_at)
FROM valuations
GROUP BY model_version
ORDER BY max(computed_at) DESC
LIMIT 1`).Scan(&version, &n, &newest)
	if err != nil {
		return err
	}
	var nulls int
	var r2 *float64
	if err := pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE predicted_price_minor IS NULL), max(r_squared)
FROM valuations WHERE model_version=$1`, version).Scan(&nulls, &r2); err != nil {
		return err
	}
	detail := fmt.Sprintf("%s: строк %d, NULL-предсказаний %d (%.1f%%)", version, n, nulls, 100*float64(nulls)/float64(n))
	if newest != nil {
		detail += fmt.Sprintf(", свежая %s", ts(*newest))
	}
	if r2 != nil {
		detail += fmt.Sprintf(", R² %.4f (внутривыборочная — C21)", *r2)
	}
	rep.add("valuations", INFO, detail)
	return nil
}

// checkLimiter — D20: все исходящие пути к источнику проходят через
// общий лимитер. Конфигурационный интервал + счётчик вызовов в
// процессе (0 в read-only-процессе — штатно).
func checkLimiter(rep *Report, intervalMS int) {
	rep.add("limiter", INFO, fmt.Sprintf(
		"интервал %d мс (source.request_interval_ms), вызовов WaitSource в процессе: %d (0 в read-only — штатно)",
		intervalMS, ratelimit.WaitCount()))
}

// checkBackup — статус последней резервной копии (ТЗ-R1 R0.3).
// FAIL при возрасте последней успешной копии > 26 ч или при ошибке.
func checkBackup(rep *Report, cfg *config.Config) {
	stateFile := filepath.Join(cfg.Backup.Dir, "backup_state.json")
	data, err := os.ReadFile(stateFile)
	if err != nil {
		rep.add("backup", FAIL, "нет файла состояния резервных копий (pb backup run ещё не выполнялся)")
		return
	}
	var st struct {
		LastRun     time.Time `json:"last_run"`
		LastSuccess time.Time `json:"last_success"`
		LastError   string    `json:"last_error"`
		DumpFile    string    `json:"dump_file"`
		SHA256      string    `json:"sha256"`
		SizeBytes   int64     `json:"size_bytes"`
	}
	if err := json.Unmarshal(data, &st); err != nil {
		rep.add("backup", FAIL, fmt.Sprintf("повреждён %s: %v", stateFile, err))
		return
	}
	if st.LastError != "" {
		rep.add("backup", FAIL, fmt.Sprintf("последний бэкап завершился ошибкой: %s", st.LastError))
		return
	}
	if st.LastSuccess.IsZero() {
		rep.add("backup", FAIL, "нет успешных резервных копий")
		return
	}
	age := time.Since(st.LastSuccess)
	if age > 26*time.Hour {
		rep.add("backup", FAIL, fmt.Sprintf("возраст последней успешной копии %.1f ч (> 26 ч), файл: %s", age.Hours(), st.DumpFile))
		return
	}
	rep.add("backup", OK, fmt.Sprintf("возраст %.1f ч (< 26 ч), размер %.1f МБ, SHA-256 %.12s...",
		age.Hours(), float64(st.SizeBytes)/(1024*1024), st.SHA256))
}

// checkRestore — статус последнего теста восстановления (ТЗ-R1 R0.4).
// FAIL при возрасте последнего успешного теста > 8 суток (192 ч) или при ошибке.
func checkRestore(rep *Report, cfg *config.Config) {
	stateFile := filepath.Join(cfg.Backup.Dir, "restore_state.json")
	data, err := os.ReadFile(stateFile)
	if err != nil {
		rep.add("restore", FAIL, "тест восстановления ещё не проводился (pb backup verify)")
		return
	}
	var st struct {
		LastRun     time.Time        `json:"last_run"`
		LastSuccess time.Time        `json:"last_success"`
		LastError   string           `json:"last_error"`
		DumpFile    string           `json:"dump_file"`
		Status      string           `json:"status"`
		Counts      map[string]int64 `json:"counts"`
	}
	if err := json.Unmarshal(data, &st); err != nil {
		rep.add("restore", FAIL, fmt.Sprintf("повреждён %s: %v", stateFile, err))
		return
	}
	if st.Status != "PASS" {
		rep.add("restore", FAIL, fmt.Sprintf("последний тест восстановления завершился со статусом %s: %s", st.Status, st.LastError))
		return
	}
	age := time.Since(st.LastSuccess)
	if age > 8*24*time.Hour {
		rep.add("restore", FAIL, fmt.Sprintf("последний успешный тест восстановления был %.1f сут назад (> 8 суток)", age.Hours()/24))
		return
	}
	rep.add("restore", OK, fmt.Sprintf("пройден %.1f ч назад, проверено таблиц: %d", age.Hours(), len(st.Counts)))
}

// checkLLM — доступность LLM-сервера (ТЗ-R1 R1.5).
// GET {extract.base_url}/models, таймаут 5 с.
// Недоступность — INFO со временем начала недоступности, не FAIL: Выпуск 1 не зависит от LLM.
func checkLLM(ctx context.Context, rep *Report, cfg *config.Config) {
	baseURL := cfg.Extract.BaseURL
	if baseURL == "" {
		rep.add("llm", INFO, "extract.base_url не настроен")
		return
	}
	ok, st, err := extract.CheckHealth(ctx, baseURL)
	if ok {
		rep.add("llm", OK, fmt.Sprintf("доступен (%s)", baseURL))
		return
	}
	sinceStr := ""
	if st != nil && st.UnavailableSince != nil {
		sinceStr = fmt.Sprintf(" с %s", st.UnavailableSince.Format("02.01.2006 15:04"))
	}
	rep.add("llm", INFO, fmt.Sprintf("недоступен%s (%s: %v)", sinceStr, baseURL, err))
}
