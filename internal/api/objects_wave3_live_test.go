package api_test

// U25–U30 (TZ UI v5, Волна 3) — live-тесты на scratch-БД pb_ui_v5_w3.
// PB_TEST_DSN — DSN основной БД (нужен для CREATE DATABASE scratch);
// без переменной тесты пропускаются (паттерн doctor_live_test.go).
//
// Фикстуры: 12 «special»-объектов (10 point + 1 area + 1 none, известные
// ключи для сортировок/фильтров) + 5000 bulk-точек в отдельной части
// корпуса (режим cells U30).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/api"
	"propertyboss/internal/config"
)

const scratchDB = "pb_ui_v5_w3"

// scratchDSN — имя БД в DSN меняем на scratch (URL- и key=value-формы).
func scratchDSN(main string) string {
	if u, err := url.Parse(main); err == nil && (strings.HasPrefix(main, "postgres://") || strings.HasPrefix(main, "postgresql://")) {
		u.Path = "/" + scratchDB
		return u.String()
	}
	if i := strings.Index(main, "dbname="); i >= 0 {
		rest := main[i+len("dbname="):]
		if j := strings.IndexAny(rest, " \t"); j >= 0 {
			return main[:i+len("dbname=")] + scratchDB + rest[j:]
		}
		return main[:i+len("dbname=")] + scratchDB
	}
	return main
}

func setupScratch(t *testing.T) *pgxpool.Pool {
	t.Helper()
	main := os.Getenv("PB_TEST_DSN")
	if main == "" {
		t.Skip("PB_TEST_DSN не задан — live-тесты отключены")
	}
	u, err := url.Parse(main)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatalf("PB_TEST_DSN: нужна URL-форма (postgres://…): %v", err)
	}
	mainDB := strings.TrimPrefix(u.Path, "/")
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(mainDB) {
		t.Fatalf("неожиданное имя основной БД %q", mainDB)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	// Scratch-БД = копия основной по TEMPLATE: схема (с расширениями, для
	// которых у app-пользователя нет прав SUPERUSER) готова, данные
	// сбрасываем TRUNCATE. CREATE DATABASE выполняем из «postgres» (из
	// основной БД шаблон копировать нельзя — она занята сессией).
	maint := *u
	maint.Path = "/postgres"
	admin, err := pgxpool.New(ctx, maint.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+scratchDB+" WITH (FORCE)"); err != nil {
		admin.Close()
		t.Fatalf("DROP DATABASE %s: %v", scratchDB, err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+scratchDB+" TEMPLATE "+mainDB); err != nil {
		admin.Close()
		t.Fatalf("CREATE DATABASE %s TEMPLATE %s: %v (нужны права CREATEDB)", scratchDB, mainDB, err)
	}
	admin.Close()

	pool, err := pgxpool.New(ctx, scratchDSN(main))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	// Таблицы фикстур — в пустое состояние (шаблон принёс производственные
	// данные; тесты считаются по абсолютным числам).
	if _, err := pool.Exec(ctx,
		`TRUNCATE objects, zones, valuations, object_photos,
		        price_history, object_listings, object_translations CASCADE`); err != nil {
		t.Fatalf("TRUNCATE scratch: %v", err)
	}
	// Миграция Волны 3 (pg_trgm + индексы) — на scratch, чтобы EXPLAIN
	// (приёмки U28/U30) видел индексы. Побочный бонус: проверяется, что
	// 0036 применяется app-пользователем (pg_trgm trusted, без SUPERUSER).
	// Если шаблон уже содержит 0036 (живая БД после Волны 3) — не
	// применять повторно: CREATE INDEX без IF NOT EXISTS падает 42P07.
	var has0036 bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name = $1)`,
		"0036_ui_v5_wave3.sql").Scan(&has0036); err != nil {
		t.Fatalf("schema_migrations 0036: %v", err)
	}
	if !has0036 {
		mig, err := os.ReadFile("../../migrations/0036_ui_v5_wave3.sql")
		if err != nil {
			t.Fatalf("0036: %v", err)
		}
		if _, err := pool.Exec(ctx, string(mig)); err != nil {
			t.Fatalf("migrate 0036 scratch: %v", err)
		}
	}
	return pool
}

// seedFixtures — 12 special (id 1..12) + 5000 bulk (id 10001..15000).
// Спецификация строк:
//
//	id | conf    | цена(minor) | площадь | комнаты | продавец         | dev
//	1  | street  | 10000       | 50      | 2       | private          | -0.20
//	2  | building| 20000       | 50      | 3       | agency           | -0.10
//	3  | street  | 30000       | 55      | 2       | developer        | 0.00
//	4  | building| 40000       | 60      | 1       | private          | +0.10
//	5  | street  | 50000       | 100     | 4       | auction_organizer| +0.30
//	6  | street  | 60000       | 25      | 2       | private          | —
//	7  | street  | NULL        | NULL    | NULL    | private          | —
//	8  | building| 80000       | 10      | 2       | private          | —
//	9  | street  | 1000000     | 1000    | 2       | private          | —
//	10 | street  | 5000        | 9.5     | 2       | private          | —
//	11 | zone    | 15000       | 33      | NULL    | agency (zone 1)  | -0.07
//	12 | NULL    | 70000       | 40      | NULL    | private          | —
//
// first_seen = 2026-01-01 + id суток → date_asc = 1..12, date_desc = 12..1.
// Фото — только у объекта 1 (has_photo).
func seedFixtures(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	must := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	must(`INSERT INTO zones (id, country, level, name, geom, source) VALUES
		(1, 'CZ', 'zone', 'Z-1', 'SRID=4326;POLYGON((14.3 50.0, 14.5 50.0, 14.5 50.2, 14.3 50.2, 14.3 50.0))'::geography, 'test'),
		(2, 'CZ', 'zone', 'Z-2', 'SRID=4326;POLYGON((14.0 49.0, 14.2 49.0, 14.2 49.2, 14.0 49.2, 14.0 49.0))'::geography, 'test')`)
	must(`INSERT INTO objects
		(id, country, deal_type, zone_id, geom, address, area_sqm, rooms,
		 current_price_minor, seller_type, geo_confidence, first_seen_at, last_seen_at)
	VALUES
		(1,  'CZ', 'sale', NULL, ST_SetSRID(ST_MakePoint(14.351, 50.051), 4326)::geography, 'Special Street 1',  50,   2,  10000,   'private',           'street',   '2026-01-01'::timestamptz + 1 * interval '1 day', '2026-01-02'::timestamptz),
		(2,  'CZ', 'sale', NULL, ST_SetSRID(ST_MakePoint(14.352, 50.052), 4326)::geography, 'Special Street 2',  50,   3,  20000,   'agency',            'building', '2026-01-01'::timestamptz + 2 * interval '1 day', '2026-01-03'::timestamptz),
		(3,  'CZ', 'sale', NULL, ST_SetSRID(ST_MakePoint(14.353, 50.053), 4326)::geography, 'Special Street 3',  55,   2,  30000,   'developer',         'street',   '2026-01-01'::timestamptz + 3 * interval '1 day', '2026-01-04'::timestamptz),
		(4,  'CZ', 'sale', NULL, ST_SetSRID(ST_MakePoint(14.354, 50.054), 4326)::geography, 'Special Street 4',  60,   1,  40000,   'private',           'building', '2026-01-01'::timestamptz + 4 * interval '1 day', '2026-01-05'::timestamptz),
		(5,  'CZ', 'sale', NULL, ST_SetSRID(ST_MakePoint(14.355, 50.055), 4326)::geography, 'Special Street 5',  100,  4,  50000,   'auction_organizer', 'street',   '2026-01-01'::timestamptz + 5 * interval '1 day', '2026-01-06'::timestamptz),
		(6,  'CZ', 'sale', NULL, ST_SetSRID(ST_MakePoint(14.356, 50.056), 4326)::geography, 'Special Street 6',  25,   2,  60000,   'private',           'street',   '2026-01-01'::timestamptz + 6 * interval '1 day', '2026-01-07'::timestamptz),
		(7,  'CZ', 'sale', NULL, ST_SetSRID(ST_MakePoint(14.357, 50.057), 4326)::geography, 'Special Street 7',  NULL, NULL, NULL,      'private',           'street',   '2026-01-01'::timestamptz + 7 * interval '1 day', '2026-01-08'::timestamptz),
		(8,  'CZ', 'sale', NULL, ST_SetSRID(ST_MakePoint(14.358, 50.058), 4326)::geography, 'Special Street 8',  10,   2,  80000,   'private',           'building', '2026-01-01'::timestamptz + 8 * interval '1 day', '2026-01-09'::timestamptz),
		(9,  'CZ', 'sale', NULL, ST_SetSRID(ST_MakePoint(14.359, 50.059), 4326)::geography, 'Special Street 9',  1000, 2,  1000000, 'private',           'street',   '2026-01-01'::timestamptz + 9 * interval '1 day', '2026-01-10'::timestamptz),
		(10, 'CZ', 'sale', NULL, ST_SetSRID(ST_MakePoint(14.360, 50.060), 4326)::geography, 'Special Street 10', 9.5,  2,  5000,    'private',           'street',   '2026-01-01'::timestamptz + 10 * interval '1 day', '2026-01-11'::timestamptz),
		(11, 'CZ', 'sale', 1,    ST_SetSRID(ST_MakePoint(14.400, 50.100), 4326)::geography, 'Special Street 11', 33,   NULL, 15000,  'agency',            'zone',     '2026-01-01'::timestamptz + 11 * interval '1 day', '2026-01-12'::timestamptz),
		(12, 'CZ', 'sale', NULL, NULL, 'Special Street 12', 40, NULL, 70000, 'private', NULL, '2026-01-01'::timestamptz + 12 * interval '1 day', '2026-01-13'::timestamptz)`)
	must(`SELECT setval('objects_id_seq', 10000)`)
	must(`INSERT INTO valuations (object_id, model_version, price_deviation, sample_size, zone_fallback, computed_at)
	VALUES (1, 'test-v1', -0.2, 100, false, now()),
	       (2, 'test-v1', -0.1, 100, false, now()),
	       (3, 'test-v1', 0.0, 100, false, now()),
	       (4, 'test-v1', 0.1, 100, false, now()),
	       (5, 'test-v1', 0.3, 100, false, now()),
	       (11, 'test-v1', -0.07, 100, false, now())`)
	must(`INSERT INTO object_photos (object_id, ord, original_url)
	VALUES (1, 1, 'https://example.com/special-1.jpg')`)
	// 5000 bulk-точек южнее special-bbox (lat 48.5..49.44 против 49.9..50.3).
	must(`INSERT INTO objects
		(country, deal_type, geom, address, current_price_minor, geo_confidence, first_seen_at, last_seen_at)
	SELECT 'CZ', 'sale',
		ST_SetSRID(ST_MakePoint(12.5 + (g % 400) * 0.01, 48.5 + floor(g / 400) * 0.08), 4326)::geography,
		'Bulk Street ' || g, 1000 + g, 'street',
		now() - g * interval '1 second', now()
	FROM generate_series(1, 5000) AS g`)
	// Статистики: без них планировщик оценивает btree-индекс по
	// geo_confidence «в лоб» и в параметризованном плане может выбрать
	// его вместо GIST (на живом корпусе stats свежие — не воспроизвести).
	must("ANALYZE objects")
}

type listItem struct {
	ID         int64    `json:"id"`
	Lat        *float64 `json:"lat"`
	Lng        *float64 `json:"lng"`
	GeomKind   string   `json:"geom_kind"`
	GeoConf    *string  `json:"geo_confidence"`
	PriceMinor *int64   `json:"price_minor"`
	AreaSqM    *float64 `json:"area_sqm"`
	Rooms      *int16   `json:"rooms"`
	ZoneID     *int64   `json:"zone_id"`
	SellerType *string  `json:"seller_type"`
	Valuation  *struct {
		PriceDeviation *float64 `json:"price_deviation"`
	} `json:"valuation"`
}

type listResp struct {
	Total   int        `json:"total"`
	Objects []listItem `json:"objects"`
}

type mapPointT struct {
	ID           int64   `json:"id"`
	Lat          float64 `json:"lat"`
	Lng          float64 `json:"lng"`
	DevClass     string  `json:"dev_class"`
	HasValuation bool    `json:"has_valuation"`
}

type mapCellT struct {
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	Count    int64   `json:"count"`
	DevClass *string `json:"dev_class"`
	Mixed    bool    `json:"mixed"`
}

type mapAreaT struct {
	ZoneID   int64   `json:"zone_id"`
	Count    int64   `json:"count"`
	DevClass *string `json:"dev_class"`
	Mixed    bool    `json:"mixed"`
}

type mapRespT struct {
	Mode   string      `json:"mode"`
	Total  int         `json:"total"`
	Points []mapPointT `json:"points"`
	Cells  []mapCellT  `json:"cells"`
	Areas  []mapAreaT  `json:"areas"`
}

func doGet(t *testing.T, u string) (*http.Response, string) {
	t.Helper()
	res, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res, string(body)
}

func TestWave3Live(t *testing.T) {
	pool := setupScratch(t)
	seedFixtures(t, pool)

	srv := api.New(pool, &config.Config{})
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()

	getList := func(t *testing.T, query string) (int, []listItem) {
		t.Helper()
		res, body := doGet(t, ts.URL+"/api/objects?"+query)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("/api/objects?%s: %d: %s", query, res.StatusCode, body)
		}
		var lr listResp
		if err := json.Unmarshal([]byte(body), &lr); err != nil {
			t.Fatalf("/api/objects?%s: JSON: %v", query, err)
		}
		return lr.Total, lr.Objects
	}
	checkList := func(t *testing.T, query string, want int) {
		t.Helper()
		total, items := getList(t, query)
		if total != want || len(items) != want {
			t.Errorf("%s: ждём %d, получено total=%d len=%d", query, want, total, len(items))
		}
	}
	check400 := func(t *testing.T, query string, subs ...string) {
		t.Helper()
		res, body := doGet(t, ts.URL+"/api/objects?"+query)
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: ждём 400, получено %d: %s", query, res.StatusCode, body)
			return
		}
		for _, s := range subs {
			if !strings.Contains(body, s) {
				t.Errorf("%s: в ответе нет %q: %s", query, s, body)
			}
		}
	}

	t.Run("U25_coordinates", func(t *testing.T) {
		getOne := func(id int) listItem {
			t.Helper()
			res, body := doGet(t, fmt.Sprintf("%s/api/objects/%d", ts.URL, id))
			if res.StatusCode != http.StatusOK {
				t.Fatalf("id=%d: %d: %s", id, res.StatusCode, body)
			}
			var o listItem
			if err := json.Unmarshal([]byte(body), &o); err != nil {
				t.Fatal(err)
			}
			return o
		}
		o1 := getOne(1)
		if o1.GeomKind != "point" {
			t.Errorf("id=1: geom_kind=%q, ждём point", o1.GeomKind)
		}
		if o1.Lat == nil || o1.Lng == nil {
			t.Errorf("id=1: lat/lng = null у point")
		} else if *o1.Lat < 50.0509 || *o1.Lat > 50.0511 || *o1.Lng < 14.3509 || *o1.Lng > 14.3511 {
			t.Errorf("id=1: координаты (%v, %v) ≠ (50.051, 14.351)", *o1.Lat, *o1.Lng)
		}
		if o1.GeoConf == nil || *o1.GeoConf != "street" {
			t.Errorf("id=1: geo_confidence = %v, ждём street", o1.GeoConf)
		}

		o11 := getOne(11)
		if o11.GeomKind != "area" {
			t.Errorf("id=11: geom_kind=%q, ждём area", o11.GeomKind)
		}
		if o11.Lat != nil || o11.Lng != nil {
			t.Errorf("id=11: lat/lng ≠ null у area")
		}
		if o11.ZoneID == nil || *o11.ZoneID != 1 {
			t.Errorf("id=11: zone_id=%v, ждём 1", o11.ZoneID)
		}
		if o11.GeoConf == nil || *o11.GeoConf != "zone" {
			t.Errorf("id=11: geo_confidence=%v, ждём zone", o11.GeoConf)
		}

		o12 := getOne(12)
		if o12.GeomKind != "none" {
			t.Errorf("id=12: geom_kind=%q, ждём none", o12.GeomKind)
		}
		if o12.Lat != nil || o12.Lng != nil {
			t.Errorf("id=12: lat/lng ≠ null у none")
		}
		if o12.GeoConf != nil {
			t.Errorf("id=12: geo_confidence = %v, ждём null", o12.GeoConf)
		}

		// Инвариант приёмки: не-point никогда не имеет непустых lat/lng.
		_, items := getList(t, "q=Special&per_page=50")
		if len(items) != 12 {
			t.Fatalf("q=Special: %d объектов, ждём 12", len(items))
		}
		for _, it := range items {
			if it.GeomKind != "point" && (it.Lat != nil || it.Lng != nil) {
				t.Errorf("id=%d kind=%s: lat/lng непустые", it.ID, it.GeomKind)
			}
		}
	})

	t.Run("U26_sorts", func(t *testing.T) {
		want := map[string][]int64{
			"date_desc":      {12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
			"date_asc":       {1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12},
			"price_asc":      {10, 1, 11, 2, 3, 4, 5, 6, 12, 8, 9, 7},
			"price_desc":     {9, 8, 12, 6, 5, 4, 3, 2, 11, 1, 10, 7},
			"deviation_asc":  {1, 2, 11, 3, 4, 5, 12, 10, 9, 8, 7, 6},
			"deviation_desc": {5, 4, 3, 11, 2, 1, 12, 10, 9, 8, 7, 6},
			"ppsm_asc":       {1, 2, 11, 5, 10, 3, 4, 9, 12, 6, 8, 7},
			"ppsm_desc":      {8, 6, 12, 9, 4, 3, 10, 5, 11, 2, 1, 7},
		}
		for sort, ids := range want {
			_, items := getList(t, "q=Special&per_page=50&sort="+sort)
			if len(items) != len(ids) {
				t.Errorf("sort=%s: %d строк, ждём %d", sort, len(items), len(ids))
				continue
			}
			for i, w := range ids {
				if items[i].ID != w {
					t.Errorf("sort=%s: позиция %d — ждём id %d, получено %d", sort, i, w, items[i].ID)
					break
				}
			}
		}
		check400(t, "sort=bogus", "sort")
		// Дефолт без sort = date_desc.
		_, items := getList(t, "q=Special&per_page=50")
		if items[0].ID != 12 {
			t.Errorf("дефолтная сортировка: первая строка id=%d, ждём 12 (date_desc)", items[0].ID)
		}
	})

	t.Run("U27_filters", func(t *testing.T) {
		base := "q=Special&per_page=50"
		checkList(t, base, 12)
		checkList(t, base+"&price_min=10001", 9)
		checkList(t, base+"&price_min=10001&price_max=25000", 2)
		checkList(t, base+"&area_min=55", 4)
		// ≤45 м²: id 6(25), 8(10), 10(9.5), 11(33), 12(40).
		checkList(t, base+"&area_max=45", 5)
		checkList(t, base+"&rooms=2", 6)
		checkList(t, base+"&zone_id=1", 1)
		checkList(t, base+"&zone_id=99", 0)
		checkList(t, base+"&seller_type=agency", 2)
		checkList(t, base+"&seller_type=agency&dev_class=below", 2)
		checkList(t, base+"&dev_class=far_below", 1)
		checkList(t, base+"&dev_class=below", 2)
		checkList(t, base+"&dev_class=near", 1)
		checkList(t, base+"&dev_class=above", 1)
		checkList(t, base+"&dev_class=far_above", 1)
		checkList(t, base+"&dev_class=none", 6)
		checkList(t, base+"&has_photo=true", 1)
		checkList(t, base+"&has_photo=false", 11)
		// «>» в JSON-ответе экранируется как \u003e — проверяем несанитизированные фрагменты.
		check400(t, base+"&price_min=50000&price_max=10000", "price_max: 50000", "10000")
		check400(t, base+"&area_min=50&area_max=25", "area_max: 50", "25")
		check400(t, base+"&seller_type=bogus", "seller_type")
		check400(t, base+"&dev_class=bogus", "dev_class")
		check400(t, base+"&has_photo=maybe", "has_photo")
		check400(t, base+"&rooms=0", "rooms")
		check400(t, base+"&rooms=99", "rooms")
		check400(t, base+"&price_min=-1", "price_min")
		check400(t, "foo=1", "foo")
	})

	t.Run("U28_search", func(t *testing.T) {
		// Регистр не важен: адреса 'Special …'.
		checkList(t, "q=special&per_page=50", 12)
		checkList(t, "q=SPECIAL%20STREET&per_page=50", 12)
		// Фрагмент улицы.
		checkList(t, "q=pecial%20Str&per_page=50", 12)
		// bulk-корпус: 5000 совпадений (total), в ответе — страница по 50.
		total, items := getList(t, "q=Bulk&per_page=50")
		if total != 5000 || len(items) != 50 {
			t.Errorf("q=Bulk: total=%d len=%d, ждём 5000/50", total, len(items))
		}
		check400(t, "q=a", "q")
	})

	t.Run("U29_total", func(t *testing.T) {
		total, items := getList(t, "q=Special&per_page=4")
		if total != 12 {
			t.Errorf("total=%d, ждём 12 (total в каждом ответе)", total)
		}
		if len(items) != 4 {
			t.Errorf("per_page=4: %d строк, ждём 4", len(items))
		}
		// Счётчик реагирует на фильтр: total пересчитан тем же ответом
		// (≤25000: id 1(10000), 2(20000), 10(5000), 11(15000)).
		total2, _ := getList(t, "q=Special&per_page=4&price_max=25000")
		if total2 != 4 {
			t.Errorf("total c price_max=25000: %d, ждём 4", total2)
		}
	})

	t.Run("U30_map", func(t *testing.T) {
		getMap := func(t *testing.T, query string) (int, mapRespT, string) {
			t.Helper()
			res, body := doGet(t, ts.URL+"/api/objects/map?"+query)
			var mr mapRespT
			if res.StatusCode != http.StatusOK {
				return res.StatusCode, mr, body
			}
			if err := json.Unmarshal([]byte(body), &mr); err != nil {
				t.Fatalf("map: JSON: %v: %s", err, body)
			}
			return 200, mr, body
		}

		// points: 10 point + 1 area в bbox (total = 11 ≤ 500).
		code, mr, body := getMap(t, "bbox=14.2,49.9,14.6,50.3&zoom=14")
		if code != 200 {
			t.Fatalf("points: %d: %s", code, body)
		}
		if mr.Mode != "points" || mr.Total != 11 {
			t.Errorf("mode=%s total=%d, ждём points/11", mr.Mode, mr.Total)
		}
		if len(mr.Points) != 10 || len(mr.Cells) != 0 {
			t.Errorf("points=%d cells=%d, ждём 10/0", len(mr.Points), len(mr.Cells))
		}
		if len(mr.Areas) != 1 || mr.Areas[0].ZoneID != 1 || mr.Areas[0].Count != 1 {
			t.Errorf("areas=%v, ждём одна зона 1 со счётом 1", mr.Areas)
		} else {
			if mr.Areas[0].DevClass == nil || *mr.Areas[0].DevClass != "below" || mr.Areas[0].Mixed {
				t.Errorf("areas[0]: dev_class=%v mixed=%v, ждём below/false", mr.Areas[0].DevClass, mr.Areas[0].Mixed)
			}
		}
		// Объект 11 (area) — в areas и одновременно отсутствует в points.
		var inPoints bool
		byID := map[int64]mapPointT{}
		for _, p := range mr.Points {
			byID[p.ID] = p
			if p.ID == 11 {
				inPoints = true
			}
		}
		if inPoints {
			t.Errorf("объект 11 (area) присутствует в points")
		}
		if p1, ok := byID[1]; !ok || p1.DevClass != "far_below" || !p1.HasValuation {
			t.Errorf("point id=1: dev_class/has_valuation = %v, ждём far_below/true", p1)
		}
		if p6, ok := byID[6]; !ok || p6.DevClass != "none" || p6.HasValuation {
			t.Errorf("point id=6: dev_class/has_valuation = %v, ждём none/false", p6)
		}

		// cells: 5000 bulk в bbox (total = 5000 > 500).
		code, mr, body = getMap(t, "bbox=12.4,48.4,16.6,49.7&zoom=10")
		if code != 200 {
			t.Fatalf("cells: %d: %s", code, body)
		}
		if mr.Mode != "cells" || mr.Total != 5000 {
			t.Errorf("mode=%s total=%d, ждём cells/5000", mr.Mode, mr.Total)
		}
		if len(mr.Points) != 0 || len(mr.Cells) == 0 {
			t.Errorf("points=%d cells=%d, ждём 0/>0", len(mr.Points), len(mr.Cells))
		}
		var sum int64
		for _, c := range mr.Cells {
			sum += c.Count
		}
		if sum != 5000 {
			t.Errorf("сумма счётчиков клеток = %d, ждём 5000", sum)
		}
		if len(mr.Areas) != 0 {
			t.Errorf("areas в bulk-bbox = %v, ждём пусто", mr.Areas)
		}

		// Фильтры U27 работают на карте: цены bulk = 1001..6000.
		code, mr, body = getMap(t, "bbox=12.4,48.4,16.6,49.7&zoom=10&price_min=5000")
		if code != 200 {
			t.Fatalf("cells+filter: %d: %s", code, body)
		}
		if mr.Total != 1001 {
			t.Errorf("total c price_min=5000: %d, ждём 1001", mr.Total)
		}

		// Отсечка: широкий bbox при низком zoom — 400.
		code, _, body = getMap(t, "bbox=-100,-50,100,50&zoom=5")
		if code != 400 {
			t.Errorf("отсечка: %d (%s), ждём 400", code, body)
		}
		// Узкий bbox при низком zoom — не отсечка.
		code, mr, body = getMap(t, "bbox=12.5,48.5,14.5,49.5&zoom=3")
		if code != 200 || mr.Mode != "cells" {
			t.Errorf("узкий bbox zoom 3: %d mode=%s, ждём 200/cells", code, mr.Mode)
		}

		// Валидация bbox/zoom.
		for _, q := range []string{
			"bbox=14,49&zoom=10",
			"bbox=a,b,c,d&zoom=10",
			"bbox=14.6,49.9,14.2,50.3&zoom=10", // minLng > maxLng
			"bbox=14.2,49.9,14.6,50.3",         // без zoom
			"zoom=10",                          // без bbox
			"bbox=14.2,49.9,14.6,50.3&zoom=99",
			"bbox=14.2,49.9,14.6,50.3&zoom=-1",
		} {
			code, _, _ = getMap(t, q)
			if code != 400 {
				t.Errorf("%s: %d, ждём 400", q, code)
			}
		}

		// Планы: EXPLAIN отдаёт план ПО СТРОКАМ (по строке на элемент),
		// и GUC (enable_seqscan) виден только внутри ОДНОЙ сессии —
		// поэтому одно соединение из пула на оба EXPLAIN.
		c, err := pool.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Release()
		explain := func(query string, args ...any) string {
			t.Helper()
			rows, err := c.Query(context.Background(), query, args...)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var b strings.Builder
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatal(err)
				}
				b.WriteString(line)
				b.WriteString("\n")
			}
			return b.String()
		}

		// GIST по geom (приёмка U30).
		plan := explain(
			`EXPLAIN (COSTS OFF) SELECT count(*) FROM objects
			 WHERE objects.geom && ST_MakeEnvelope($1::float8, $2::float8, $3::float8, $4::float8, 0)::geography
			   AND objects.geo_confidence IN ('building', 'street')`,
			14.2, 49.9, 14.6, 50.3)
		t.Log("EXPLAIN bbox:\n" + plan)
		if !strings.Contains(plan, "objects_geom_idx") {
			t.Errorf("GIST-индекс по geom не используется:\n%s", plan)
		}

		// GIN pg_trgm по lower(address) (приёмка U28). enable_seqscan=off:
		// на маленьком scratch-корпусе планировщик вправе выбрать seq scan;
		// приёмка — что индекс ДОСТУПЕН (на живом корпусе он и выбирается).
		if _, err := c.Exec(context.Background(), "SET enable_seqscan = off"); err != nil {
			t.Fatal(err)
		}
		plan2 := explain(
			`EXPLAIN (COSTS OFF) SELECT count(*) FROM objects
			 WHERE lower(address) LIKE '%' || $1 || '%' ESCAPE '\'`,
			"pecial street")
		t.Log("EXPLAIN trgm:\n" + plan2)
		if !strings.Contains(plan2, "objects_address_trgm_idx") {
			t.Errorf("trgm-индекс не используется:\n%s", plan2)
		}
	})
}
