package scan

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/config"
	"propertyboss/internal/db"
)

// Тесты этапа 6 (ТЗ §8.2) против живой БД — тот же паттерн, что
// scan_test.go: PB_TEST_DSN не задан — тест пропускается.
//
// Прогоны создаются прямым SQL (scan_runs + raw_listings +
// object_listings) — это и есть неизменяемая история, с которой работает
// delist-пасс.

const (
	delSrcID   = "pb-del-test"
	delURLID   = "pb-del-url-test"
	delCountry = "DD"
	delBaseURL = "https://deltest.invalid/listing"
)

// delTestConfig — конфиг пасса: N=2, порог аномалии 50%, минимальный
// знаменатель раунда 10 (фикстуры — 20 объектов: аномалий-сценарии
// при Active=20 проходят гвард), таймаут 5 с, без пауз между URL-чеками.
func delTestConfig() *config.Config {
	c := &config.Config{}
	c.Delist.MinConsecutiveMisses = 2
	c.Delist.MaxDelistedSharePct = 50
	c.Delist.MinObjectsForShare = 10
	c.Delist.URLCheckTimeoutSec = 5
	return c
}

// delOpen — пул + чистка (до запуска и в Cleanup, пока пул жив).
func delOpen(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PB_TEST_DSN")
	if dsn == "" {
		t.Skip("PB_TEST_DSN не задан — тест требует живого Postgres")
	}
	ctx := t.Context()
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("открытие БД: %v", err)
	}
	unlock, err := db.LiveTestLock(ctx, pool)
	if err != nil {
		t.Fatalf("live lock: %v", err)
	}
	// t.Cleanup идёт LIFO: unlock и sweep зарегистрированы позже
	// Close — фикстуры удаляются, пока пул жив, под локом.
	t.Cleanup(func() { pool.Close() })
	t.Cleanup(unlock)
	t.Cleanup(func() { delSweep(t, pool) })
	delSweep(t, pool)
	return pool
}

// delSetup — тестовый источник (страна DD — песочница delist-тестов,
// отдельно от 'TT' пакета zones: бинари пакетов go test гоняет
// параллельно, общий country позволял cleanup одного стирать фикстуры
// другого) + активная конфигурация поиска + n активных объектов без
// ссылок. Возвращает id конфигурации и id объектов.
func delSetup(t *testing.T, pool *pgxpool.Pool, srcID string, urlCheck bool, nObjects int) (cfgID int64, objIDs []int64) {
	t.Helper()
	ctx := t.Context()
	_, err := pool.Exec(ctx, `
		INSERT INTO sources (id, name, domain, country, deal_types, kind, access_policy, url_check_allowed)
		VALUES ($1, 'delist test source', 'deltest.invalid', $2, ARRAY['sale'], 'simple', '{}', $3)`,
		srcID, delCountry, urlCheck)
	if err != nil {
		t.Fatalf("sources[%s]: %v", srcID, err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO search_configs (source_id, country, deal_type)
		VALUES ($1, $2, 'sale') RETURNING id`, srcID, delCountry).Scan(&cfgID); err != nil {
		t.Fatalf("search_configs[%s]: %v", srcID, err)
	}
	for i := 0; i < nObjects; i++ {
		var id int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO objects (country, deal_type, first_seen_at, last_seen_at)
			VALUES ($1, 'sale', now(), now()) RETURNING id`, delCountry).Scan(&id); err != nil {
			t.Fatalf("objects[%d]: %v", i, err)
		}
		objIDs = append(objIDs, id)
	}
	return cfgID, objIDs
}

// delMakeRun — прогон сканера тестового источника. found — объекты,
// которых прогон «увидел» (raw_listings + ссылка); остальные — нет.
// base — префикс URL объявлений (для URL-теста — httptest-сервер).
func delMakeRun(t *testing.T, pool *pgxpool.Pool, srcID string, cfgID int64, seq int, found []int64, base, completeness, kind string) {
	t.Helper()
	ctx := t.Context()
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(seq) * time.Hour)
	var kindParam any // nil → NULL: пустая строка нарушила бы CHECK
	if kind != "" {
		kindParam = kind
	}
	var runID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO scan_runs (source_id, search_config_id, started_at, finished_at, completeness, failure_kind, listings_found)
		VALUES ($1, $2, $3, $3, $4, $5, $6) RETURNING id`,
		srcID, cfgID, start, completeness, kindParam, len(found)).Scan(&runID); err != nil {
		t.Fatalf("scan_runs[%d]: %v", seq, err)
	}
	for _, objID := range found {
		external := fmt.Sprintf("del-%d", objID) // стабильный: одно и то же объявление во всех прогонах
		var rlID int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO raw_listings (scan_run_id, source_id, external_id, source_url, fetched_at)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			runID, srcID, external, base+"/"+external, start).Scan(&rlID); err != nil {
			t.Fatalf("raw_listings[%d/%d]: %v", seq, objID, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO object_listings (object_id, raw_listing_id, source_id, external_id, match_method, match_confidence)
			VALUES ($1, $2, $3, $4, 'source_external', 'high')
			ON CONFLICT (object_id, source_id, external_id) DO NOTHING`,
			objID, rlID, srcID, external); err != nil {
			t.Fatalf("object_listings[%d/%d]: %v", seq, objID, err)
		}
	}
}

// delSweep — удаление тестовых фикстур (порядок — по внешним ключам).
func delSweep(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	queries := []string{
		`DELETE FROM price_history WHERE object_id IN
		 (SELECT object_id FROM object_listings WHERE source_id IN ($1, $2))`,
		`DELETE FROM objects WHERE id IN
		 (SELECT object_id FROM object_listings WHERE source_id IN ($1, $2))`,
		`DELETE FROM object_listings WHERE source_id IN ($1, $2)`,
		`DELETE FROM raw_listings WHERE source_id IN ($1, $2)`,
		`DELETE FROM scan_runs WHERE source_id IN ($1, $2)`,
		`DELETE FROM search_configs WHERE source_id IN ($1, $2)`,
		`DELETE FROM sources WHERE id IN ($1, $2)`,
		`DELETE FROM notifications WHERE kind = 'delist_anomaly' AND payload->>'source_id' IN ($1, $2)`,
	}
	for i, q := range queries {
		if _, err := pool.Exec(ctx, q, delSrcID, delURLID); err != nil {
			t.Logf("delSweep[%d]: %v", i, err)
		}
	}
}

func delAssertStatus(t *testing.T, pool *pgxpool.Pool, objID int64, wantStatus, wantReason string) {
	t.Helper()
	var (
		status string
		reason *string
	)
	if err := pool.QueryRow(t.Context(), `
		SELECT status, delisted_reason FROM objects WHERE id = $1`, objID).Scan(&status, &reason); err != nil {
		t.Fatalf("objects[%d]: %v", objID, err)
	}
	if reason == nil != (wantReason == "") || (reason != nil && *reason != wantReason) {
		t.Fatalf("objects[%d]: delisted_reason = %v, ждали %q", objID, reason, wantReason)
	}
	if status != wantStatus {
		t.Fatalf("objects[%d]: status = %s, ждали %s (reason = %v)", objID, status, wantStatus, reason)
	}
}

// Защита №1 (ТЗ §8.2): неполные прогоны (partial/failed) не участвуют
// в вычислении промахов вообще.
func TestDelistIncompleteScansNotCounted(t *testing.T) {
	pool := delOpen(t)
	cfgID, objs := delSetup(t, pool, delSrcID, false, 1)
	x := objs[0]
	delMakeRun(t, pool, delSrcID, cfgID, 0, []int64{x}, delBaseURL, "complete", "")
	delMakeRun(t, pool, delSrcID, cfgID, 1, nil, delBaseURL, "partial", "http_429")
	delMakeRun(t, pool, delSrcID, cfgID, 2, nil, delBaseURL, "failed", "network")
	delMakeRun(t, pool, delSrcID, cfgID, 3, nil, delBaseURL, "complete", "")

	rep, err := RunDelistPass(t.Context(), pool, delTestConfig(), delSrcID)
	if err != nil {
		t.Fatalf("пасс: %v", err)
	}
	// Промашен только 1 полный скан (< 2) — и двух неполных между ними
	// не видно.
	if rep.Active != 1 || rep.Candidates != 0 || rep.Delisted != 0 || rep.Anomaly {
		t.Fatalf("rep = %+v, ждали Active=1 Candidates=0 Delisted=0", rep)
	}
	delAssertStatus(t, pool, x, "active", "")
}

// Защита №2 (ТЗ §8.2): delisted только после N=2 последовательных
// ПОЛНЫХ промахов; один промах — объект остаётся активным.
func TestDelistConsecutiveMissesRequired(t *testing.T) {
	pool := delOpen(t)
	cfgID, objs := delSetup(t, pool, delSrcID, false, 10)
	x := objs[0]
	others := objs[1:]
	delMakeRun(t, pool, delSrcID, cfgID, 0, objs, delBaseURL, "complete", "")

	// Прогон 1: x не найден (1 промах) — ещё недостаточно.
	delMakeRun(t, pool, delSrcID, cfgID, 1, others, delBaseURL, "complete", "")
	rep1, err := RunDelistPass(t.Context(), pool, delTestConfig(), delSrcID)
	if err != nil {
		t.Fatalf("пасс 1: %v", err)
	}
	if rep1.Candidates != 0 || rep1.Delisted != 0 {
		t.Fatalf("пасс 1: %+v, ждали Candidates=0 (1 промах < 2)", rep1)
	}
	delAssertStatus(t, pool, x, "active", "")

	// Прогон 2: x не найден снова (2 промаха) — delisted, 'unknown'.
	delMakeRun(t, pool, delSrcID, cfgID, 2, others, delBaseURL, "complete", "")
	rep2, err := RunDelistPass(t.Context(), pool, delTestConfig(), delSrcID)
	if err != nil {
		t.Fatalf("пасс 2: %v", err)
	}
	// 1 кандидат из 10 — 10% < 50%: аномалии нет, delisted применяется.
	if rep2.Active != 10 || rep2.Candidates != 1 || rep2.Delisted != 1 || rep2.Anomaly {
		t.Fatalf("пасс 2: %+v, ждали Active=10 Candidates=1 Delisted=1", rep2)
	}
	delAssertStatus(t, pool, x, "delisted", "unknown")
	for _, id := range others {
		delAssertStatus(t, pool, id, "active", "")
	}

	// Повторный пасс — идемпотентен: x уже не active.
	rep3, err := RunDelistPass(t.Context(), pool, delTestConfig(), delSrcID)
	if err != nil {
		t.Fatalf("пасс 3: %v", err)
	}
	if rep3.Active != 9 || rep3.Candidates != 0 || rep3.Delisted != 0 {
		t.Fatalf("пасс 3: %+v, ждали Active=9 Candidates=0 Delisted=0 (идемпотентность)", rep3)
	}
}

// Критерий готовности этапа 6: симуляция сбоя источника (все объекты
// исчезают в полных прогонах) НЕ приводит к массовой смене статусов —
// долевой барьер (защита №3) останавливает пасс.
func TestDelistMassAnomalyStopsPass(t *testing.T) {
	pool := delOpen(t)
	cfgID, objs := delSetup(t, pool, delSrcID, false, 20)
	delMakeRun(t, pool, delSrcID, cfgID, 0, objs, delBaseURL, "complete", "")
	// Сбой: два последовательных ПОЛНЫХ прогона, в которых наших
	// объектов нет. (Полный прогон с нулём нужных объявлений —
	// катастрофический сценарий ТЗ §8.2: смена вёрстки/диапазона, при
	// которой источник «успешно» вернул чужую выдачу. Тихий сбой,
	// дающий пустую выдачу, раннер уже помечает partial — защита №1.)
	delMakeRun(t, pool, delSrcID, cfgID, 1, nil, delBaseURL, "complete", "")
	delMakeRun(t, pool, delSrcID, cfgID, 2, nil, delBaseURL, "complete", "")

	rep, err := RunDelistPass(t.Context(), pool, delTestConfig(), delSrcID)
	if err != nil {
		t.Fatalf("пасс: %v", err)
	}
	if !rep.Anomaly || rep.Delisted != 0 || rep.Active != 20 || rep.Candidates != 20 {
		t.Fatalf("rep = %+v, ждали Anomaly=true Delisted=0 Active=20 Candidates=20", rep)
	}
	if rep.SharePct != 100 {
		t.Fatalf("SharePct = %v, ждали 100", rep.SharePct)
	}
	// Все объекты остались активными — массовой смены статусов нет.
	for _, id := range objs {
		delAssertStatus(t, pool, id, "active", "")
	}
	// Оператору записано уведомление.
	var (
		recipient string
		share     float64
	)
	if err := pool.QueryRow(t.Context(), `
		SELECT recipient, (payload->>'share_pct')::float8
		FROM notifications
		WHERE kind = 'delist_anomaly' AND payload->>'source_id' = $1
		ORDER BY id DESC LIMIT 1`, delSrcID).Scan(&recipient, &share); err != nil {
		t.Fatalf("notifications: %v", err)
	}
	if recipient != "operator" || share != 100 {
		t.Fatalf("уведомление: recipient=%s share=%v, ждали operator/100", recipient, share)
	}
}

// D18: малый раунд (активных объектов меньше min_objects_for_share) —
// доля аномалии не вычисляется и гвард №3 не срабатывает: массовое
// исчезновение в маленьком раунде не является сигналом сбоя источника,
// «100% из 1 объекта» невозможно. Делистинг идёт штатным путём:
// N последовательных промахов (защита №2) + URL-чек (защита №4, если
// источник разрешает). Здесь url_check_allowed=false — причина 'unknown'.
func TestDelistSmallRoundBelowMinNoAnomaly(t *testing.T) {
	pool := delOpen(t)
	cfgID, objs := delSetup(t, pool, delSrcID, false, 8)
	delMakeRun(t, pool, delSrcID, cfgID, 0, objs, delBaseURL, "complete", "")
	delMakeRun(t, pool, delSrcID, cfgID, 1, nil, delBaseURL, "complete", "")
	delMakeRun(t, pool, delSrcID, cfgID, 2, nil, delBaseURL, "complete", "")

	rep, err := RunDelistPass(t.Context(), pool, delTestConfig(), delSrcID)
	if err != nil {
		t.Fatalf("пасс: %v", err)
	}
	// 8 активных < min_objects_for_share=10: аномалии нет, несмотря на
	// 100% кандидатов; делистинг применён по штатному пути.
	if rep.Active != 8 || rep.Candidates != 8 || rep.Delisted != 8 || rep.Anomaly {
		t.Fatalf("rep = %+v, ждали Active=8 Candidates=8 Delisted=8 Anomaly=false", rep)
	}
	var notices int
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*) FROM notifications
		WHERE kind = 'delist_anomaly' AND payload->>'source_id' = $1`, delSrcID).Scan(&notices); err != nil {
		t.Fatalf("notifications: %v", err)
	}
	if notices != 0 {
		t.Fatalf("уведомлений об аномалии %d, ожидалось 0 (раунд < минимального N)", notices)
	}
	for _, id := range objs {
		delAssertStatus(t, pool, id, "delisted", "unknown")
	}
}

// delTestListingHTML / delTestSectionHTML — минимальные тела
// объявления/раздела по маркерам classifyPageBody (delist.go).
// Заголовок объявления — «объект + город» (без «inzerce»); заголовок
// раздела — «… inzerce».
const (
	delTestListingHTML = `<html><head><title>Prodej bytu Brno</title></head>` +
		`<body><H1 class="nadpisdetail">Prodej bytu, Brno</H1></body></html>`
	delTestSectionHTML = `<html><head><title>Prodej bytu Brno inzerce</title></head>` +
		`<body><div class="inzeraty inzeratyflex"></div></body></html>`
)

// Защита №4 (ТЗ §8.2) и C7 (09.09): URL-чек решает по смыслу ответа,
// а не по сырому статус-коду. 20 объектов, 6 кандидатов (30% < 50% —
// аномалии нет):
//   - живое объявление: 200, тело объявления, тот же URL — active;
//   - 404 — delisted 'unknown';
//   - редирект на другое объявление того же хоста — delisted
//     'relisted';
//   - редирект на раздел (механизм мёртвого объявления, 07.09: 30/30)
//     — delisted 'unknown', при этом тело раздела НЕ запрашивается
//     (семейство /inzeraty/ троттлится WAF, 429: чтение перегревает
//     троттлинг на каждом пассе, C7);
//   - 200 с телом раздела по URL объявления — delisted 'unknown';
//   - 200 с нераспознаваемой структурой (смена вёрстки) — решения
//     нет, active (счётчик url_failed).
func TestDelistURLCheck(t *testing.T) {
	pool := delOpen(t)
	cfgID, objs := delSetup(t, pool, delURLID, true, 20)
	A, B, C, D, E, F := objs[0], objs[1], objs[2], objs[3], objs[4], objs[5]
	rest := objs[6:]
	// URL — в форме объявления (/inzerat/<id>/): только такую цель
	// редиректа следует (C7).
	pathA := fmt.Sprintf("/inzerat/del-%d", A)
	pathB := fmt.Sprintf("/inzerat/del-%d", B)
	pathC := fmt.Sprintf("/inzerat/del-%d", C)
	pathD := fmt.Sprintf("/inzerat/del-%d", D)
	pathE := fmt.Sprintf("/inzerat/del-%d", E)
	pathF := fmt.Sprintf("/inzerat/del-%d", F)
	// var до инициализации: в Go переменная не входит в область видимости
	// в своём же инициализаторе (даже внутри замыкания).
	var ts *httptest.Server
	var sectionHits atomic.Int64
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathA, pathC + "-new":
			_, _ = w.Write([]byte(delTestListingHTML))
		case pathB:
			w.WriteHeader(http.StatusNotFound)
		case pathC:
			http.Redirect(w, r, ts.URL+pathC+"-new", http.StatusFound)
		case pathD:
			// Механизм мёртвого объявления: редирект на раздел.
			http.Redirect(w, r, "/inzeraty/prodej-bytu-brno/", http.StatusFound)
		case pathE:
			_, _ = w.Write([]byte(delTestSectionHTML))
		case pathF:
			_, _ = w.Write([]byte("нераспознаваемая структура"))
		default:
			// /inzeraty/… (раздел): запрашиваться НЕ должен (C7) —
			// счётчик подтверждает это ниже.
			sectionHits.Add(1)
			_, _ = w.Write([]byte(delTestSectionHTML))
		}
	}))
	t.Cleanup(ts.Close)

	base := ts.URL + "/inzerat"
	delMakeRun(t, pool, delURLID, cfgID, 0, objs, base, "complete", "")
	delMakeRun(t, pool, delURLID, cfgID, 1, rest, base, "complete", "")
	delMakeRun(t, pool, delURLID, cfgID, 2, rest, base, "complete", "")

	rep, err := RunDelistPass(t.Context(), pool, delTestConfig(), delURLID)
	if err != nil {
		t.Fatalf("пасс: %v", err)
	}
	if rep.Active != 20 || rep.Candidates != 6 || rep.Anomaly {
		t.Fatalf("rep = %+v, ждали Active=20 Candidates=6 (30%% < 50%%)", rep)
	}
	if rep.Delisted != 4 || rep.URLAlive != 1 || rep.URLFailed != 1 {
		t.Fatalf("rep = %+v, ждали Delisted=4 URLAlive=1 URLFailed=1", rep)
	}
	delAssertStatus(t, pool, A, "active", "")
	delAssertStatus(t, pool, B, "delisted", "unknown")
	delAssertStatus(t, pool, C, "delisted", "relisted")
	delAssertStatus(t, pool, D, "delisted", "unknown")
	delAssertStatus(t, pool, E, "delisted", "unknown")
	delAssertStatus(t, pool, F, "active", "")
	if n := sectionHits.Load(); n != 0 {
		t.Fatalf("раздел запрашивался %d раз(а) — C7: тело раздела за редиректом читать нельзя", n)
	}
}

// Повторное появление delisted-объекта под тем же external_id в
// следующем полном скане — статус откатывается (маркировка delisted
// обратима: ошибка пасса не застревает навсегда). Через настоящий
// раннер (matchListing).
func TestDelistReactivateOnReturn(t *testing.T) {
	pool := delOpen(t)
	cfgID, objs := delSetup(t, pool, delSrcID, false, 10)
	x := objs[0]
	others := objs[1:]
	delMakeRun(t, pool, delSrcID, cfgID, 0, objs, delBaseURL, "complete", "")
	delMakeRun(t, pool, delSrcID, cfgID, 1, others, delBaseURL, "complete", "")
	delMakeRun(t, pool, delSrcID, cfgID, 2, others, delBaseURL, "complete", "")
	if _, err := RunDelistPass(t.Context(), pool, delTestConfig(), delSrcID); err != nil {
		t.Fatalf("пасс: %v", err)
	}
	delAssertStatus(t, pool, x, "delisted", "unknown")

	// Объект вернулся в выдачу под тем же external_id — реальный
	// прогон раннера.
	sc, err := LoadSearchConfig(t.Context(), pool, cfgID)
	if err != nil {
		t.Fatalf("LoadSearchConfig: %v", err)
	}
	external := fmt.Sprintf("del-%d", x)
	runner := NewRunner(pool, map[string]config.DedupeParams{
		delCountry: {RadiusM: 50, AreaTolerancePct: 10, AddressSimilarity: 0.9},
	}, nil) // расписание (этап 11) здесь не проверяется
	rep := runner.Run(t.Context(), delSrcID, &fakeConn{
		id:       delSrcID,
		listings: []Listing{{ExternalID: external, URL: delBaseURL + "/" + external}},
	}, sc)
	if rep.Err != nil {
		t.Fatalf("раннер: %v", rep.Err)
	}
	if rep.Completeness != "complete" {
		t.Fatalf("раннер: completeness = %s, ждали complete", rep.Completeness)
	}
	delAssertStatus(t, pool, x, "active", "")
}
