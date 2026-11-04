// attrules_live_test.go — live-тест оффлайн-прогона (C6: зеркало
// area_sqm) против живой БД. Паттерн internal/zones: PB_TEST_DSN не задан
// — тест пропускается; данные — country 'TT'; зачистка — t.Cleanup.
//
// Покрытие:
//   - чистое «N m²» в тексте (карточка/деталь) → attributes +
//     attribute_values (rule_text) + зеркальная колонка area_sqm;
//   - контекстный гвард: «Pozemek 1500 m²» без другого числа — хита нет;
//   - заполненное зеркало не перезаписывается (40 ≠ 65);
//   - идемпотентность: повторный прогон → 0 записей.
package attrules

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/db"
)

const testCountry = "TT"

// reAreaStd — тот же паттерн, что seed-шаблон в миграции 0031 (первый из
// пяти).
const reAreaStd = `(?P<val>[0-9][0-9\s]*(?:[,.][0-9]+)?)\s*(?:m[²2]|mkv\b|metry kvadratick\p{L}+)`

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
	// t.Cleanup идёт LIFO: unlock и sweep зарегистрированы позже Close —
	// чистка работает под локом, лок отпущен последним.
	t.Cleanup(func() { pool.Close() })
	t.Cleanup(unlock)
	t.Cleanup(func() { sweepTT(t, pool) })
	// Старт с чистого листа: остатки прерванного раннего прогона.
	sweepTT(t, pool)
	return pool
}

func sweepTT(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `DELETE FROM attribute_values
		WHERE object_id IN (SELECT id FROM objects WHERE country = $1)`, testCountry)
	_, _ = pool.Exec(ctx, `DELETE FROM objects WHERE country = $1`, testCountry)
	_, _ = pool.Exec(ctx, `DELETE FROM attribute_templates WHERE country = $1`, testCountry)
	_, _ = pool.Exec(ctx, `DELETE FROM attribute_registry WHERE country = $1`, testCountry)
}

func TestLiveRunAreaMirror(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
INSERT INTO attribute_registry
    (country, key, data_type, used_in_pricing, label_ru, label_en, source_evidence, value_min, value_max)
VALUES ($1, 'area_sqm', 'float', false, 'tt', 'tt', 'live test', 1, 5000)`, testCountry); err != nil {
		t.Fatalf("registry: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO attribute_templates
    (country, attr_key, pattern_type, pattern, value, status, confidence, source)
VALUES ($1, 'area_sqm', 'regex', $2, NULL, 'active', 0.80, 'seed')`, testCountry, reAreaStd); err != nil {
		t.Fatalf("template: %v", err)
	}

	var seq int
	mkObj := func(desc string, area any) int64 {
		t.Helper()
		seq++
		var id int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO objects
			    (country, deal_type, address, description_original, area_sqm, first_seen_at, last_seen_at)
			VALUES ($1, 'sale', $2, $3, $4, now(), now())
			RETURNING id`,
			testCountry, fmt.Sprintf("TT street %d", seq), desc, area).Scan(&id); err != nil {
			t.Fatalf("объект %q: %v", desc, err)
		}
		return id
	}

	id1 := mkObj("Prodej bytu 65 m²", nil) // чистое → 65
	id2 := mkObj("Pozemek 1500 m²", nil)   // только гвард → нет хита
	id3 := mkObj("Byt 65 m²", 40)          // зеркало заполнено → не трогаем
	id4 := mkObj("užitná plocha 48,5 m2", nil) // чешское число → 48.5

	rep, err := Run(ctx, pool, testCountry, 0, 0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Кандидаты: id1, id2, id4 (у id3 зеркало заполнено — EXISTS ложь).
	if rep.Scanned != 3 || rep.Written != 2 || rep.Values != 2 || rep.AreaMirror != 2 {
		t.Fatalf("Run = %+v, want Scanned=3 Written=2 Values=2 AreaMirror=2", rep)
	}

	getArea := func(id int64) (any, string) {
		t.Helper()
		var (
			area  *float64
			has   bool
			attr  map[string]any
		)
		if err := pool.QueryRow(ctx, `
SELECT o.area_sqm, (o.attributes ? 'area_sqm'), o.attributes
FROM objects o WHERE o.id = $1`, id).Scan(&area, &has, &attr); err != nil {
			t.Fatalf("объект %d: %v", id, err)
		}
		var v string
		if has {
			if f, ok := attr["area_sqm"].(float64); ok {
				v = fmt.Sprint(f)
			}
		}
		if area == nil {
			return nil, v
		}
		return *area, v
	}

	if a, av := getArea(id1); a == nil || a.(float64) != 65 || av != "65" {
		t.Fatalf("id1: area=%v attrs=%q, want 65/65 (зеркало + JSONB)", a, av)
	}
	if a, _ := getArea(id2); a != nil {
		t.Fatalf("id2: area=%v, want NULL (гвард: только «pozemek 1500 m²»)", a)
	}
	if a, av := getArea(id3); a == nil || a.(float64) != 40 || av != "" {
		t.Fatalf("id3: area=%v attrs=%q, want 40/'' (зеркало не перезаписано)", a, av)
	}
	if a, av := getArea(id4); a == nil || a.(float64) != 48.5 || av != "48.5" {
		t.Fatalf("id4: area=%v attrs=%q, want 48.5/48.5 (ParseCZNum «48,5»)", a, av)
	}

	// Provenance: rule_text + уверенность шаблона (0.80; NUMERIC(4,3)).
	var (
		prov string
		conf float64
	)
	if err := pool.QueryRow(ctx, `
SELECT extracted_by, confidence FROM attribute_values
WHERE object_id = $1 AND attr_key = 'area_sqm'`, id1).Scan(&prov, &conf); err != nil {
		t.Fatalf("attribute_values id1: %v", err)
	}
	if prov != "rule_text" || conf != 0.80 {
		t.Fatalf("provenance id1 = %s/%v, want rule_text/0.80", prov, conf)
	}
	// У id2 provenance нет (хита не было).
	var n2 int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM attribute_values WHERE object_id = $1`, id2).Scan(&n2); err != nil {
		t.Fatalf("attribute_values id2: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("id2: строк attribute_values %d, want 0 (гвард)", n2)
	}

	// Идемпотентность: повторный прогон ничего не пишет (id2 остаётся
	// кандидатом — у него нет хита, но и писать нечего).
	rep2, err := Run(ctx, pool, testCountry, 0, 0)
	if err != nil {
		t.Fatalf("Run повторный: %v", err)
	}
	if rep2.Written != 0 || rep2.Values != 0 || rep2.AreaMirror != 0 {
		t.Fatalf("повторный Run = %+v, want 0/0/0 (идемпотентность)", rep2)
	}
}
