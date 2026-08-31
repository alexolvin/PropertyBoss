package zones

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/db"
)

// Тесты зон против живой БД (тот же паттерн, что internal/scan):
// PB_TEST_DSN не задан — тест пропускается. Данные тестов — country 'TT',
// source 'pb-zones-test'; зачистка — t.Cleanup.

const (
	testCountry = "TT"
	testSource  = "pb-zones-test"
)

func openTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PB_TEST_DSN")
	if dsn == "" {
		t.Skip("PB_TEST_DSN не задан — тест требует живого Postgres")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	unlock, err := db.LiveTestLock(context.Background(), pool)
	if err != nil {
		t.Fatalf("live lock: %v", err)
	}
	// t.Cleanup идёт LIFO: unlock и sweep зарегистрированы позже
	// Close — чистка работает под локом, лок отпущен последним.
	t.Cleanup(func() { pool.Close() })
	t.Cleanup(unlock)
	t.Cleanup(func() { sweepTestZones(t, pool) })
	return pool
}

func sweepTestZones(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `DELETE FROM objects WHERE country = $1`, testCountry)
	_, _ = pool.Exec(ctx, `DELETE FROM zone_reference_prices WHERE zone_id IN
		(SELECT id FROM zones WHERE country = $1)`, testCountry)
	// Ссылки родителя внутри страны снимаем до удаления (self-FK).
	_, _ = pool.Exec(ctx, `UPDATE zones SET parent_id = NULL WHERE country = $1
		AND parent_id IN (SELECT id FROM zones WHERE country = $1)`, testCountry)
	_, _ = pool.Exec(ctx, `DELETE FROM zones WHERE country = $1`, testCountry)
}

// testGeoJSON — регион ⊃ муниципалитет ⊃ зона (координаты — тестовая область).
func testGeoJSON() string {
	return `{
	"type": "FeatureCollection",
	"features": [
	  {"type": "Feature",
	   "geometry": {"type": "Polygon", "coordinates": [[[10.0, 45.0], [10.2, 45.0], [10.2, 45.2], [10.0, 45.2], [10.0, 45.0]]]},
	   "properties": {"level": "region", "name": "Testregion", "external_code": "tt-r1"}},
	  {"type": "Feature",
	   "geometry": {"type": "Polygon", "coordinates": [[[10.05, 45.05], [10.10, 45.05], [10.10, 45.10], [10.05, 45.10], [10.05, 45.05]]]},
	   "properties": {"level": "municipality", "name": "Teststadt", "external_code": "tt-m1", "parent_external_code": "tt-r1"}},
	  {"type": "Feature",
	   "geometry": {"type": "Polygon", "coordinates": [[[10.06, 45.06], [10.08, 45.06], [10.08, 45.08], [10.06, 45.08], [10.06, 45.06]]]},
	   "properties": {"level": "zone", "name": "Testzone", "external_code": "tt-z1", "parent_external_code": "tt-m1"}}
	]}`
}

func writeGeoJSON(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "zones.geojson")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("запись GeoJSON: %v", err)
	}
	return p
}

func zoneID(t *testing.T, pool *pgxpool.Pool, code string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM zones WHERE country = $1 AND external_code = $2`,
		testCountry, code).Scan(&id); err != nil {
		t.Fatalf("зона %s не найдена: %v", code, err)
	}
	return id
}

func insertTestObject(t *testing.T, pool *pgxpool.Pool, wkt, address string) int64 {
	t.Helper()
	var id int64
	var err error
	if wkt == "" {
		err = pool.QueryRow(context.Background(), `
			INSERT INTO objects (country, deal_type, address, first_seen_at, last_seen_at)
			VALUES ($1, 'sale', $2, now(), now())
			RETURNING id`, testCountry, address).Scan(&id)
	} else {
		err = pool.QueryRow(context.Background(), `
			INSERT INTO objects (country, deal_type, geom, address, first_seen_at, last_seen_at)
			VALUES ($1, 'sale', ST_GeogFromText($2), $3, now(), now())
			RETURNING id`, testCountry, "POINT("+wkt+")", address).Scan(&id)
	}
	if err != nil {
		t.Fatalf("вставка тестового объекта %s: %v", address, err)
	}
	return id
}

func objectZone(t *testing.T, pool *pgxpool.Pool, objectID int64) *int64 {
	t.Helper()
	var z *int64
	if err := pool.QueryRow(context.Background(),
		`SELECT zone_id FROM objects WHERE id = $1`, objectID).Scan(&z); err != nil {
		t.Fatalf("чтение zone_id объекта %d: %v", objectID, err)
	}
	return z
}

func TestImportAndAssign(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	path := writeGeoJSON(t, testGeoJSON())

	// 1) Импорт: 3 зоны, иерархия выстроена.
	rep, err := Import(ctx, pool, path, "tt", testSource)
	if err != nil {
		t.Fatalf("импорт: %v", err)
	}
	if rep.Inserted != 3 || rep.Updated != 0 || rep.Unresolved != 0 {
		t.Fatalf("импорт: unexpected report %+v", rep)
	}
	idRegion, idMun, idZone := zoneID(t, pool, "tt-r1"), zoneID(t, pool, "tt-m1"), zoneID(t, pool, "tt-z1")
	var parentMun, parentZone *int64
	if err := pool.QueryRow(ctx, `SELECT parent_id FROM zones WHERE id = $1`, idMun).Scan(&parentMun); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT parent_id FROM zones WHERE id = $1`, idZone).Scan(&parentZone); err != nil {
		t.Fatal(err)
	}
	if parentMun == nil || *parentMun != idRegion {
		t.Fatalf("мunicipality: parent_id = %v, ожидается %d", parentMun, idRegion)
	}
	if parentZone == nil || *parentZone != idMun {
		t.Fatalf("zone: parent_id = %v, ожидается %d", parentZone, idMun)
	}

	// 2) Повторный импорт — идемпотентность.
	rep2, err := Import(ctx, pool, path, "tt", testSource)
	if err != nil {
		t.Fatalf("повторный импорт: %v", err)
	}
	if rep2.Updated != 3 || rep2.Inserted != 0 {
		t.Fatalf("повторный импорт: unexpected report %+v", rep2)
	}

	// Базовые данные всей таблицы objects: Assign — глобальная операция,
	// и БД может содержать реальные объекты (без координат). Предпосылка:
	// ни один реальный объект не имеет координат и зоны (на текущих этапах
	// коннекторы координаты не публикуют; если это изменится — обновить
	// ожидания ниже, т.к. глобальный assign начнёт их трогать).
	var baseNoGeom, baseWithGeom, baseWithZone int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE geom IS NULL),
		       count(*) FILTER (WHERE geom IS NOT NULL),
		       count(*) FILTER (WHERE zone_id IS NOT NULL)
		FROM objects`).Scan(&baseNoGeom, &baseWithGeom, &baseWithZone); err != nil {
		t.Fatal(err)
	}
	if baseWithGeom != 0 || baseWithZone != 0 {
		t.Fatalf("предпосылка нарушена: реальных объектов с координатами = %d или с зоной = %d — assign глобальный, ожидания теста устарели",
			baseWithGeom, baseWithZone)
	}

	// 3) Объекты: точка в зоне / в муниципалитете / в регионе / вне всех / без координат.
	oInZone := insertTestObject(t, pool, "10.07 45.07", "in zone")
	oInMun := insertTestObject(t, pool, "10.055 45.055", "in municipality")
	oInRegion := insertTestObject(t, pool, "10.01 45.01", "in region")
	oOutside := insertTestObject(t, pool, "20.0 50.0", "outside")
	oNoGeom := insertTestObject(t, pool, "", "no geom")

	// 4) Привязка: самая конкретная зона.
	arep, err := Assign(ctx, pool)
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if arep.WithGeom != 4 || arep.NoGeom != baseNoGeom+1 || arep.Changed != 3 || arep.Uncovered != 1 {
		t.Fatalf("assign: unexpected report %+v", arep)
	}
	if got := objectZone(t, pool, oInZone); got == nil || *got != idZone {
		t.Fatalf("точка в зоне: zone_id = %v, ожидается зона %d", got, idZone)
	}
	if got := objectZone(t, pool, oInMun); got == nil || *got != idMun {
		t.Fatalf("точка в муниципалитете: zone_id = %v, ожидается %d", got, idMun)
	}
	if got := objectZone(t, pool, oInRegion); got == nil || *got != idRegion {
		t.Fatalf("точка в регионе: zone_id = %v, ожидается %d", got, idRegion)
	}
	if got := objectZone(t, pool, oOutside); got != nil {
		t.Fatalf("точка вне зон: zone_id = %v, ожидается NULL", got)
	}
	if got := objectZone(t, pool, oNoGeom); got != nil {
		t.Fatalf("без координат: zone_id = %v, ожидается NULL", got)
	}

	// 5) Повторный assign — ничего не меняется (идемпотентность).
	arep2, err := Assign(ctx, pool)
	if err != nil {
		t.Fatalf("assign 2: %v", err)
	}
	if arep2.Changed != 0 || arep2.Cleared != 0 {
		t.Fatalf("assign 2: unexpected report %+v", arep2)
	}

	// 6) Точка ушла из зоны — zone_id обнуляется честно.
	if _, err := pool.Exec(ctx,
		`UPDATE objects SET geom = ST_GeogFromText('POINT(21.0 51.0)') WHERE id = $1`, oInMun); err != nil {
		t.Fatal(err)
	}
	arep3, err := Assign(ctx, pool)
	if err != nil {
		t.Fatalf("assign 3: %v", err)
	}
	if arep3.Cleared != 1 {
		t.Fatalf("assign 3: Cleared = %d, ожидается 1 (%+v)", arep3.Cleared, arep3)
	}
	if got := objectZone(t, pool, oInMun); got != nil {
		t.Fatalf("ушедшая точка: zone_id = %v, ожидается NULL", got)
	}
}

func TestImportValidation(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()

	cases := []struct {
		name    string
		body    string
		contain string
	}{
		{"не FeatureCollection", `{"type":"Feature","geometry":{}}`, "FeatureCollection"},
		{"плохой level", strings.Replace(testGeoJSON(),
			`"level": "region"`, `"level": "district"`, 1), "level"},
		{"region с родителем", strings.Replace(testGeoJSON(),
			`"external_code": "tt-r1"}`, `"external_code": "tt-r1", "parent_external_code": "x"}`, 1), "нет родителя"},
		{"дубль кода", strings.Replace(testGeoJSON(),
			`"external_code": "tt-m1"`, `"external_code": "tt-r1"`, 1), "дублируется"},
		{"геометрия-точка", strings.Replace(testGeoJSON(),
			`"geometry": {"type": "Polygon", "coordinates": [[[10.0, 45.0], [10.2, 45.0], [10.2, 45.2], [10.0, 45.2], [10.0, 45.0]]]}`,
			`"geometry": {"type": "Point", "coordinates": [10.0, 45.0]}`, 1), "Polygon/MultiPolygon"},
		{"пустой name", strings.Replace(testGeoJSON(),
			`"name": "Testregion"`, `"name": ""`, 1), "name"},
		{"пустой features", `{"type":"FeatureCollection","features":[]}`, "нет ни одной"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Import(ctx, pool, writeGeoJSON(t, tc.body), "tt", testSource)
			if err == nil {
				t.Fatalf("ожидалась ошибка")
			}
			if !strings.Contains(err.Error(), tc.contain) {
				t.Fatalf("ошибка %q не содержит %q", err, tc.contain)
			}
		})
	}

	// Ничего не записалось после неудачных попыток.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM zones WHERE country = $1`, testCountry).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("после неудачных импортов в zones осталось строк: %d", n)
	}
}

func TestQuotazioni(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	if _, err := Import(ctx, pool, writeGeoJSON(t, testGeoJSON()), "tt", testSource); err != nil {
		t.Fatalf("импорт зон: %v", err)
	}

	csvBody := `codzona,tipo,contratto,prezzo_min,prezzo_max,periodo
tt-z1,A,vendita,1450,1600,2025/1
tt-m1,A,affitto,8.50,9.20,2025-H2
tt-z1,S,vendita,,1200,2025/1
`
	csvPath := filepath.Join(t.TempDir(), "q.csv")
	if err := os.WriteFile(csvPath, []byte(csvBody), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := Quotazioni(ctx, pool, csvPath, "tt", testSource)
	if err != nil {
		t.Fatalf("quotazioni: %v", err)
	}
	if rep.Rows != 3 || rep.Inserted != 3 || rep.NullPrices != 1 {
		t.Fatalf("quotazioni: unexpected report %+v", rep)
	}

	// Проверка значений: EUR, exponent из справочника (2), точные минорные.
	idZone := zoneID(t, pool, "tt-z1")
	var (
		deal, ptype string
		minM, maxM  *int64
		periodStart time.Time
	)
	err = pool.QueryRow(ctx, `
		SELECT deal_type, property_type, price_min_minor, price_max_minor, period_start
		FROM zone_reference_prices
		WHERE zone_id = $1 AND property_type = 'A' AND deal_type = 'sale'`, idZone).
		Scan(&deal, &ptype, &minM, &maxM, &periodStart)
	if err != nil {
		t.Fatal(err)
	}
	if minM == nil || *minM != 145000 || maxM == nil || *maxM != 160000 {
		t.Fatalf("A/sale: min=%v max=%v, ожидается 145000/160000", minM, maxM)
	}
	if periodStart.Year() != 2025 || periodStart.Month() != 1 || periodStart.Day() != 1 {
		t.Fatalf("period_start = %s, ожидается 2025-01-01", periodStart)
	}
	// Полугодие 2 → 01.07; дробные цены — точные минорные.
	idMun := zoneID(t, pool, "tt-m1")
	err = pool.QueryRow(ctx, `
		SELECT price_min_minor, price_max_minor, period_start
		FROM zone_reference_prices
		WHERE zone_id = $1 AND deal_type = 'rent'`, idMun).
		Scan(&minM, &maxM, &periodStart)
	if err != nil {
		t.Fatal(err)
	}
	if minM == nil || *minM != 850 || maxM == nil || *maxM != 920 {
		t.Fatalf("rent: min=%v max=%v, ожидается 850/920", minM, maxM)
	}
	if periodStart.Year() != 2025 || periodStart.Month() != 7 || periodStart.Day() != 1 {
		t.Fatalf("period_start = %s, ожидается 2025-07-01", periodStart)
	}
	// Пустая ячейка — честный NULL.
	var minNULL *int64
	if err := pool.QueryRow(ctx, `
		SELECT price_min_minor FROM zone_reference_prices
		WHERE zone_id = $1 AND property_type = 'S'`, idZone).Scan(&minNULL); err != nil {
		t.Fatal(err)
	}
	if minNULL != nil {
		t.Fatalf("пустая ячейка: min = %v, ожидается NULL", minNULL)
	}

	// Идемпотентность.
	rep2, err := Quotazioni(ctx, pool, csvPath, "tt", testSource)
	if err != nil {
		t.Fatalf("повторный quotazioni: %v", err)
	}
	if rep2.Updated != 3 || rep2.Inserted != 0 {
		t.Fatalf("повторный quotazioni: unexpected report %+v", rep2)
	}

	// Неизвестный код зоны — явная ошибка, ничего не записано.
	badPath := filepath.Join(t.TempDir(), "bad.csv")
	if err := os.WriteFile(badPath, []byte(
		"codzona,tipo,contratto,prezzo_min,prezzo_max,periodo\ntt-nope,A,vendita,1,2,2025/1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Quotazioni(ctx, pool, badPath, "tt", testSource); err == nil ||
		!strings.Contains(err.Error(), "tt-nope") {
		t.Fatalf("ожидалась ошибка про tt-nope, получено: %v", err)
	}

	// Незнакомая колонка в заголовке — явная ошибка.
	badHdrPath := filepath.Join(t.TempDir(), "badhdr.csv")
	if err := os.WriteFile(badHdrPath, []byte(
		"codzona,tipo,contratto,prezzo_min,prezzo_max,periodo,extra\n"+
			"tt-z1,A,vendita,1,2,2025/1,x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Quotazioni(ctx, pool, badHdrPath, "tt", testSource); err == nil ||
		!strings.Contains(err.Error(), "extra") {
		t.Fatalf("ожидалась ошибка про колонку extra, получено: %v", err)
	}
}

// Тест на чистом уровне (без БД): цены и периоды.
func TestParseQuotPriceAndPeriod(t *testing.T) {
	cases := []struct {
		in  string
		exp int // экспонента
		out *int64
		err bool
	}{
		{"1450", 2, ptrI64(145000), false},
		{"1450.5", 2, ptrI64(145050), false},
		{"1450.50", 2, ptrI64(145050), false},
		{"8.5", 2, ptrI64(850), false},
		{"", 2, nil, false},
		{"1450.555", 2, nil, true}, // больше знаков, чем экспонента — не округляем
		{"-5", 2, nil, true},
		{"abc", 2, nil, true},
	}
	for _, tc := range cases {
		got, err := parseQuotPrice(tc.in, tc.exp)
		if tc.err {
			if err == nil {
				t.Errorf("parseQuotPrice(%q): ожидалась ошибка, получено %v", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseQuotPrice(%q): %v", tc.in, err)
			continue
		}
		if (got == nil) != (tc.out == nil) || (got != nil && *got != *tc.out) {
			t.Errorf("parseQuotPrice(%q) = %v, ожидается %v", tc.in, got, tc.out)
		}
	}

	p, err := parseQuotPeriod("2025/2")
	if err != nil || p.Year() != 2025 || p.Month() != 7 {
		t.Errorf("parseQuotPeriod(2025/2) = %v, %v", p, err)
	}
	p, err = parseQuotPeriod("2025-S1")
	if err != nil || p.Year() != 2025 || p.Month() != 1 {
		t.Errorf("parseQuotPeriod(2025-S1) = %v, %v", p, err)
	}
	if _, err = parseQuotPeriod("25/1"); err == nil {
		t.Errorf("parseQuotPeriod(25/1): ожидалась ошибка")
	}
}

func ptrI64(v int64) *int64 { return &v }
