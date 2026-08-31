package scan

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/config"
	"propertyboss/internal/db"
)

// Тесты каркаса сканера против живой БД (тот же паттерн, что internal/fx):
// PB_TEST_DSN не задан — тест пропускается.

const (
	testSrcID  = "pb-scan-test"
	testSrc2ID = "pb-scan-test-2"
)

func ptr[T any](v T) *T { return &v }

type fakeConn struct {
	id       string
	listings []Listing
	issue    *Issue
	err      error
}

func (f *fakeConn) SourceID() string { return f.id }
func (f *fakeConn) Scan(ctx context.Context, cfg SearchConfig) ([]Listing, *Issue, error) {
	return f.listings, f.issue, f.err
}

func listingA(priceMinor int64, posted time.Time) Listing {
	return Listing{
		ExternalID:   "a-1",
		URL:          "https://test.invalid/a-1",
		PriceMinor:   &priceMinor,
		Currency:     ptr("CZK"),
		AreaSqM:      ptr("62.50"),
		Rooms:        ptr(3),
		PropertyType: ptr("byt"),
		Lat:          ptr(50.08),
		Lng:          ptr(14.42),
		Address:      ptr("Karlova ul. 5, Praha"),
		PostedAt:     &posted,
		Attributes:   map[string]any{"has_gas": true, "unknown_attr": "x"},
	}
}

func listingB(posted time.Time) Listing {
	return Listing{
		ExternalID:   "b-1",
		URL:          "https://test.invalid/b-1",
		Address:      ptr("Nám. Republiky 100, Praha"),
		Rooms:        ptr(2),
		PropertyType: ptr("byt"),
		PostedAt:     &posted,
	}
}

func seedTestFixtures(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (cfgID, cfg2ID int64) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		INSERT INTO sources (id, name, domain, country, deal_types, kind, access_policy, state)
		VALUES ($1, 'test source', 'test.invalid', 'CZ', ARRAY['sale'], 'simple',
		        '{"note":"тестовый источник"}', 'active'),
		       ($2, 'test source 2', 'test2.invalid', 'CZ', ARRAY['sale'], 'simple',
		        '{"note":"тестовый источник"}', 'active')`,
		testSrcID, testSrc2ID)
	if err != nil {
		t.Fatalf("seed sources: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO search_configs (source_id, country, deal_type, currency)
		VALUES ($1, 'CZ', 'sale', 'CZK') RETURNING id`, testSrcID).Scan(&cfgID); err != nil {
		t.Fatalf("seed search_configs: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO search_configs (source_id, country, deal_type, currency)
		VALUES ($1, 'CZ', 'sale', 'CZK') RETURNING id`, testSrc2ID).Scan(&cfg2ID); err != nil {
		t.Fatalf("seed search_configs (2): %v", err)
	}
	// has_gas может уже быть в реестре (промышленная запись этапа 3 —
	// данные пользователя, ТЗ §6): тогда тест работает на ней, а свою
	// строку не создаёт (sweep удаляет только запись с тестовым
	// свидетельством — промышленную не трогает).
	_, err = pool.Exec(ctx, `
		INSERT INTO attribute_registry (country, key, data_type, label_ru, label_en, source_evidence)
		VALUES ('CZ', 'has_gas', 'bool', 'Газ', 'Gas', 'тестовое свидетельство')
		ON CONFLICT (country, key) DO NOTHING`)
	if err != nil {
		t.Fatalf("seed attribute_registry: %v", err)
	}
	return cfgID, cfg2ID
}

func sweepTestFixtures(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	// Порядок — по внешним ключам: сначала дети.
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
	}
	for i, q := range queries {
		if _, err := pool.Exec(ctx, q, testSrcID, testSrc2ID); err != nil {
			t.Logf("sweep[%d]: %v", i, err)
		}
	}
	// Тестовая запись реестра (без параметров).
	if _, err := pool.Exec(ctx, `
		DELETE FROM attribute_registry
		WHERE country = 'CZ' AND key = 'has_gas' AND source_evidence = 'тестовое свидетельство'`); err != nil {
		t.Logf("sweep registry: %v", err)
	}
}

func TestScanPipeline(t *testing.T) {
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
	// Close — фикстуры удаляются пока пул жив, под локом, и только
	// потом лок и пул закрываются.
	// (t.Context() к моменту Cleanup отменён — чистим по фону; defer
	// pool.Close() отменён: defer срабатывает ДО Cleanup.)
	t.Cleanup(func() { pool.Close() })
	t.Cleanup(unlock)
	t.Cleanup(func() { sweepTestFixtures(t, context.Background(), pool) })

	sweepTestFixtures(t, ctx, pool)
	cfgID, cfg2ID := seedTestFixtures(t, ctx, pool)

	runner := NewRunner(pool, map[string]config.DedupeParams{
		"CZ": {RadiusM: 50, AreaTolerancePct: 10, AddressSimilarity: 0.9},
	}, nil) // расписание (этап 11) здесь не проверяется
	cfg1, err := LoadSearchConfig(ctx, pool, cfgID)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg2, err := LoadSearchConfig(ctx, pool, cfg2ID)
	if err != nil {
		t.Fatalf("load config 2: %v", err)
	}

	postedA1 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	postedB := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	// 1. Полный скан: 2 записи → 2 новых объекта.
	rep1 := runner.Run(ctx, testSrcID, &fakeConn{
		id: testSrcID,
		listings: []Listing{
			listingA(123456789, postedA1),
			listingB(postedB),
		},
	}, cfg1)
	if rep1.Err != nil {
		t.Fatalf("run1: %v", rep1.Err)
	}
	if rep1.Completeness != "complete" || rep1.Listings != 2 || rep1.NewObjects != 2 {
		t.Fatalf("run1: completeness=%s listings=%d new=%d, ждали complete/2/2",
			rep1.Completeness, rep1.Listings, rep1.NewObjects)
	}

	objID := objectIDByLink(t, ctx, pool, testSrcID, "a-1")
	assertObjectFields(t, ctx, pool, objID, 123456789, `{"has_gas":true}`, `{"unknown_attr":"x"}`, false)
	assertScanRun(t, ctx, pool, rep1.RunID, "complete", "", 2, 2)
	var rawCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM raw_listings WHERE source_id = $1`, testSrcID).Scan(&rawCount); err != nil {
		t.Fatal(err)
	}
	if rawCount != 2 {
		t.Fatalf("raw_listings: %d, ждали 2", rawCount)
	}

	// 2. Повторный скан: цена A изменилась, дата публикации A ушла вперёд
	// (§14.5.1 → posted_date_unreliable).
	rep2 := runner.Run(ctx, testSrcID, &fakeConn{
		id: testSrcID,
		listings: []Listing{
			listingA(120000000, time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)),
			listingB(postedB),
		},
	}, cfg1)
	if rep2.Err != nil {
		t.Fatalf("run2: %v", rep2.Err)
	}
	if rep2.Completeness != "complete" || rep2.Listings != 2 || rep2.NewObjects != 0 {
		t.Fatalf("run2: completeness=%s listings=%d new=%d, ждали complete/2/0",
			rep2.Completeness, rep2.Listings, rep2.NewObjects)
	}
	assertObjectFields(t, ctx, pool, objID, 120000000, `{"has_gas":true}`, `{"unknown_attr":"x"}`, true)
	var phCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM price_history WHERE object_id = $1 AND source_id = $2`,
		objID, testSrcID).Scan(&phCount); err != nil {
		t.Fatal(err)
	}
	if phCount != 2 {
		t.Fatalf("price_history A: %d строк, ждали 2 (первая цена + изменение)", phCount)
	}
	// B: дата не двигалась — флаг не ставится.
	objB := objectIDByLink(t, ctx, pool, testSrcID, "b-1")
	assertObjectFields(t, ctx, pool, objB, 0, `{}`, `{}`, false)
	var bPrice *int64
	if err := pool.QueryRow(ctx, `SELECT current_price_minor FROM objects WHERE id = $1`, objB).Scan(&bPrice); err != nil {
		t.Fatal(err)
	}
	if bPrice != nil {
		t.Fatalf("B: current_price_minor = %v, ждали NULL (цены на источнике нет)", *bPrice)
	}

	// 3. Неполный скан (http_429 на второй странице): данные записываются,
	// completeness=partial — и такие прогоны не считаются полными (ТЗ §8.2.1).
	rep3 := runner.Run(ctx, testSrcID, &fakeConn{
		id:       testSrcID,
		listings: []Listing{listingB(postedB)},
		issue:    &Issue{Kind: FailHTTP429, Message: "страница 2 из 2: 429"},
	}, cfg1)
	if rep3.Err != nil {
		t.Fatalf("run3: %v", rep3.Err)
	}
	if rep3.Completeness != "partial" || rep3.FailureKind != FailHTTP429 || rep3.Listings != 1 {
		t.Fatalf("run3: completeness=%s kind=%s listings=%d, ждали partial/http_429/1",
			rep3.Completeness, rep3.FailureKind, rep3.Listings)
	}
	assertScanRun(t, ctx, pool, rep3.RunID, "partial", "http_429", 1, 0)

	// 4. Проваленный скан (сеть): failed + network, записей нет.
	rep4 := runner.Run(ctx, testSrcID, &fakeConn{
		id:  testSrcID,
		err: NewFail(FailNetwork, errors.New("dial tcp: connection refused")),
	}, cfg1)
	if rep4.Completeness != "failed" || rep4.FailureKind != FailNetwork || rep4.Listings != 0 {
		t.Fatalf("run4: completeness=%s kind=%s listings=%d, ждали failed/network/0",
			rep4.Completeness, rep4.FailureKind, rep4.Listings)
	}
	assertScanRun(t, ctx, pool, rep4.RunID, "failed", "network", 0, 0)

	// 5. Пустая выдача без ошибок: не complete (не отличима от тихого сбоя).
	rep5 := runner.Run(ctx, testSrcID, &fakeConn{id: testSrcID}, cfg1)
	if rep5.Completeness != "partial" || rep5.FailureKind != "" {
		t.Fatalf("run5: completeness=%s kind=%s, ждали partial/''",
			rep5.Completeness, rep5.FailureKind)
	}
	assertScanRun(t, ctx, pool, rep5.RunID, "partial", "", 0, 0)

	// 6. Дедупликация по координатам (другой источник, тот же объект):
	// c-1 в ~16 м от a-1, те же площадь/комнаты/тип → ссылка на тот же
	// объект, match_method='geo', новый объект не создаётся.
	rep6 := runner.Run(ctx, testSrc2ID, &fakeConn{
		id: testSrc2ID,
		listings: []Listing{{
			ExternalID:   "c-1",
			URL:          "https://test2.invalid/c-1",
			PriceMinor:   ptr(int64(150000000)),
			Currency:     ptr("CZK"),
			AreaSqM:      ptr("62.50"),
			Rooms:        ptr(3),
			PropertyType: ptr("byt"),
			Lat:          ptr(50.0801),
			Lng:          ptr(14.4201),
		}},
	}, cfg2)
	if rep6.Err != nil {
		t.Fatalf("run6: %v", rep6.Err)
	}
	if rep6.Completeness != "complete" || rep6.NewObjects != 0 {
		t.Fatalf("run6: completeness=%s new=%d, ждали complete/0", rep6.Completeness, rep6.NewObjects)
	}
	assertLinkMethod(t, ctx, pool, objID, testSrc2ID, "c-1", "geo", "high")

	// 7. Дедупликация по адресу (нет координат): сходство выше порога,
	// комнаты/тип совпадают → match_method='address', confidence='low'
	// (ТЗ §8.1: не сливается молча).
	rep7 := runner.Run(ctx, testSrc2ID, &fakeConn{
		id: testSrc2ID,
		listings: []Listing{{
			ExternalID:   "d-1",
			URL:          "https://test2.invalid/d-1",
			Address:      ptr("Nám Republiky 100 Praha 1"),
			Rooms:        ptr(2),
			PropertyType: ptr("byt"),
		}},
	}, cfg2)
	if rep7.Err != nil {
		t.Fatalf("run7: %v", rep7.Err)
	}
	if rep7.NewObjects != 0 {
		t.Fatalf("run7: new=%d, ждали 0 (адрес совпал с B)", rep7.NewObjects)
	}
	assertLinkMethod(t, ctx, pool, objB, testSrc2ID, "d-1", "address", "low")

	// 8. Тот же адрес, но комнаты не совпадают → НЕ совпадение, новый
	// объект (критерии §8.1 обязательны вместе с адресом).
	rep8 := runner.Run(ctx, testSrc2ID, &fakeConn{
		id: testSrc2ID,
		listings: []Listing{{
			ExternalID:   "e-1",
			URL:          "https://test2.invalid/e-1",
			Address:      ptr("Nám Republiky 100 Praha 2"),
			Rooms:        ptr(4),
			PropertyType: ptr("byt"),
		}},
	}, cfg2)
	if rep8.Err != nil {
		t.Fatalf("run8: %v", rep8.Err)
	}
	if rep8.NewObjects != 1 {
		t.Fatalf("run8: new=%d, ждали 1 (комнаты 4 ≠ 2 — не совпадение)", rep8.NewObjects)
	}

	// 9. В пределах ОДНОГО источника: тот же адрес, но другой
	// external_id — другой физический объект, НЕ сливается (идентичность
	// внутри источника — (source_id, external_id); адресная дедупликация
	// ТЗ §8.1 — только между источниками). Без исключения в кандидатов
	// f-1 слился бы с B — регрессия промышленного скана 2026-08-27.
	rep9 := runner.Run(ctx, testSrcID, &fakeConn{
		id: testSrcID,
		listings: []Listing{{
			ExternalID:   "f-1",
			URL:          "https://test.invalid/f-1",
			Address:      ptr("Nám. Republiky 100, Praha"),
			Rooms:        ptr(2),
			PropertyType: ptr("byt"),
		}},
	}, cfg1)
	if rep9.Err != nil {
		t.Fatalf("run9: %v", rep9.Err)
	}
	if rep9.NewObjects != 1 {
		t.Fatalf("run9: new=%d, ждали 1 (тот же источник, другой external_id)", rep9.NewObjects)
	}
	objF := objectIDByLink(t, ctx, pool, testSrcID, "f-1")
	if objF == objB {
		t.Fatal("f-1 слился с объектом B (тот же источник) — это другой физический объект")
	}

	var totalObjects int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM objects o
		WHERE EXISTS (SELECT 1 FROM object_listings ol
		             WHERE ol.object_id = o.id AND ol.source_id IN ($1, $2))`,
		testSrcID, testSrc2ID).Scan(&totalObjects); err != nil {
		t.Fatal(err)
	}
	if totalObjects != 4 {
		t.Fatalf("objects: %d, ждали 4 (A, B, E, F)", totalObjects)
	}
}

// --- вспомогательные проверки ---

func objectIDByLink(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sourceID, externalID string) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(ctx, `
		SELECT object_id FROM object_listings
		WHERE source_id = $1 AND external_id = $2`, sourceID, externalID,
	).Scan(&id)
	if err != nil {
		t.Fatalf("object_id по ссылке %s/%s: %v", sourceID, externalID, err)
	}
	return id
}

// assertObjectFields — цена, attributes, unmapped, флаг ненадёжности даты.
// wantPrice: 0 и wantPriceNil=true → ждём NULL (для B).
func assertObjectFields(t *testing.T, ctx context.Context, pool *pgxpool.Pool, objID, wantPrice int64, wantAttrs, wantUnmapped string, wantUnreliable bool) {
	t.Helper()
	var (
		price     *int64
		attrs     string
		unmapped  string
		unreliable bool
	)
	err := pool.QueryRow(ctx, `
		SELECT current_price_minor, attributes::text, attributes_unmapped::text,
		       posted_date_unreliable
		FROM objects WHERE id = $1`, objID,
	).Scan(&price, &attrs, &unmapped, &unreliable)
	if err != nil {
		t.Fatalf("objects[%d]: %v", objID, err)
	}
	if wantPrice == 0 {
		if price != nil {
			t.Fatalf("objects[%d]: price = %d, ждали NULL", objID, *price)
		}
	} else if price == nil || *price != wantPrice {
		t.Fatalf("objects[%d]: price = %v, ждали %d", objID, price, wantPrice)
	}
	// jsonb нормализует пробелы ({\"has_gas\": true}) — сравниваем по смыслу.
	if !jsonEqual(attrs, wantAttrs) {
		t.Fatalf("objects[%d]: attributes = %s, ждали %s", objID, attrs, wantAttrs)
	}
	if !jsonEqual(unmapped, wantUnmapped) {
		t.Fatalf("objects[%d]: attributes_unmapped = %s, ждали %s", objID, unmapped, wantUnmapped)
	}
	if unreliable != wantUnreliable {
		t.Fatalf("objects[%d]: posted_date_unreliable = %v, ждали %v", objID, unreliable, wantUnreliable)
	}
}

// jsonEqual — сравнение JSON-объектов по смыслу (jsonb нормализует формат).
func jsonEqual(a, b string) bool {
	var ma, mb map[string]any
	if err := json.Unmarshal([]byte(a), &ma); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(b), &mb); err != nil {
		return false
	}
	return reflect.DeepEqual(ma, mb)
}

func assertScanRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID int64, wantCompleteness, wantKind string, wantListings, wantNew int) {
	t.Helper()
	var (
		completeness string
		kind         *string
		listings     int
		newObjects   int
		finished     *time.Time
	)
	err := pool.QueryRow(ctx, `
		SELECT completeness, failure_kind, listings_found, new_objects, finished_at
		FROM scan_runs WHERE id = $1`, runID,
	).Scan(&completeness, &kind, &listings, &newObjects, &finished)
	if err != nil {
		t.Fatalf("scan_runs[%d]: %v", runID, err)
	}
	if finished == nil {
		t.Fatalf("scan_runs[%d]: finished_at NULL — скан не завершён", runID)
	}
	if completeness != wantCompleteness {
		t.Fatalf("scan_runs[%d]: completeness = %s, ждали %s", runID, completeness, wantCompleteness)
	}
	if kind == nil != (wantKind == "") || (kind != nil && *kind != wantKind) {
		t.Fatalf("scan_runs[%d]: failure_kind = %v, ждали %q", runID, kind, wantKind)
	}
	if listings != wantListings || newObjects != wantNew {
		t.Fatalf("scan_runs[%d]: listings=%d new=%d, ждали %d/%d",
			runID, listings, newObjects, wantListings, wantNew)
	}
}

func assertLinkMethod(t *testing.T, ctx context.Context, pool *pgxpool.Pool, objID int64, sourceID, externalID, wantMethod, wantConf string) {
	t.Helper()
	var method, conf string
	err := pool.QueryRow(ctx, `
		SELECT match_method, match_confidence FROM object_listings
		WHERE object_id = $1 AND source_id = $2 AND external_id = $3`,
		objID, sourceID, externalID,
	).Scan(&method, &conf)
	if err != nil {
		t.Fatalf("object_listings[%d/%s/%s]: %v", objID, sourceID, externalID, err)
	}
	if method != wantMethod || conf != wantConf {
		t.Fatalf("object_listings[%d/%s/%s]: %s/%s, ждали %s/%s",
			objID, sourceID, externalID, method, conf, wantMethod, wantConf)
	}
}

// --- чистые (без БД) тесты адресного сходства ---

func TestAddressSimilarity(t *testing.T) {
	// Равны после нормализации (регистр, пунктуация) → 1.
	if got := addressSimilarity("Karlova 5, Praha", "KARLOVA 5 PRAHA"); got != 1.0 {
		t.Errorf("совпавшие после нормализации: %v, ждали 1", got)
	}
	// «Nám» → «nm» (диакритика отбрасывается): нормализованные длины 19 и 20,
	// расстояние 1 → 1 − 1/20 = 0.95.
	if got := addressSimilarity("Nám. Republiky 100, Praha", "Nám Republiky 100 Praha 1");
		got < 1-1.0/20-1e-9 || got > 1-1.0/20+1e-9 {
		t.Errorf("один лишний символ: %v, ждали ≈%.4f", got, 1-1.0/20)
	}
	// Разные улицы — ниже порога 0.9 (точное значение не важно).
	if got := addressSimilarity("Karlova 5", "Masarykova 100"); got >= 0.9 {
		t.Errorf("разные адреса: %v, ждали < 0.9", got)
	}
	// Пустые.
	if got := addressSimilarity("", ""); got != 1.0 {
		t.Errorf("оба пустые: %v, ждали 1", got)
	}
	if got := addressSimilarity("a", ""); got != 0.0 {
		t.Errorf("один пустой: %v, ждали 0", got)
	}
}

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"kitten", "sitting", 3},
		{"abc", "abc", 0},
		{"abc", "", 3},
		{"flaw", "lawn", 2},
	}
	for _, c := range cases {
		if got := levenshtein(c.a, c.b); got != c.want {
			t.Errorf("levenshtein(%q, %q) = %d, ждали %d", c.a, c.b, got, c.want)
		}
	}
}
