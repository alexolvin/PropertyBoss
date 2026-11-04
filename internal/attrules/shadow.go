// shadow.go — теневой контроль промоутнутых шаблонов (C8).
//
// Число повторений не защищает от систематической ошибки после промоушна:
// покрытие считает неверное значение покрытым, и никто его не видит
// (класс C12). Защита: доля shadow_sample_pct (конфиг) объектов,
// обработанных ПРОМОУТНУТЫМ шаблоном (promoted_at задан), дополнительно
// уходят в LLM; расхождение (включая пустой ответ LLM) — автоматический
// демоушн шаблона (односторонний, до ручного pb rules set) и запись в
// счётчик shadow_mismatches.
//
// Seed-шаблоны в тень не идут: они не выучены из корпуса и не могут
// «стать» неверными массово (пометка в отчёте).
//
// Постановка в очередь — здесь (детерминированная выборка); обработка
// (реальные LLM-вызовы) — в internal/extract (Client живёт там, чтобы не
// создавать цикл импортов).
package attrules

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Enqueue — строка в очередь теневого контроля для хита ПРОМОУТНУТОГО
// шаблона. Детерминированная выборка: objectID % 100 < samplePct — тот же
// объект всегда попадает в одну и ту же выборку (нет генератора: выборка
// воспроизводима и не дублируется повторным обогащением). samplePct <= 0
// — тень выключена. ON CONFLICT DO NOTHING — повторная обработка объекта
// не создаёт дубля строки. Возвращает (в выборке?, err): false — объект
// не попал в выборку (или тень выключена).
func Enqueue(ctx context.Context, pool *pgxpool.Pool, templateID, objectID int64,
	attrKey string, value any, samplePct int) (bool, error) {
	if samplePct <= 0 {
		return false, nil
	}
	if objectID%100 >= int64(samplePct) {
		return false, nil
	}
	valJSON, err := json.Marshal(value)
	if err != nil {
		return false, fmt.Errorf("attrules: shadow value %s: %w", attrKey, err)
	}
	tag, err := pool.Exec(ctx, `
INSERT INTO template_shadow_checks (template_id, object_id, attr_key, template_value)
VALUES ($1, $2, $3, $4::jsonb)
ON CONFLICT (template_id, object_id, attr_key) DO NOTHING`,
		templateID, objectID, attrKey, string(valJSON))
	if err != nil {
		return false, fmt.Errorf("attrules: постановка в теневую очередь: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ValuesEqual — равенство двух JSON-значений (unmarshal → marshal →
// сравнить строки: 1 и 1.0 — одно, true и "true" — разные). Используется
// холдаут-проверкой и теневым контролем (internal/extract).
func ValuesEqual(a, b any) bool {
	return jsonEqual(a, b)
}

// Demote — автоматический демоушн по теневому расхождению (C8).
// Односторонний: из 'active' — в 'demoted', из любого другого статуса —
// статус не трогается (счётчик и момент фиксируются всё равно).
// Восстановление — только руками: pb rules set <id> --status active.
func Demote(ctx context.Context, pool *pgxpool.Pool, templateID int64) error {
	_, err := pool.Exec(ctx, `
UPDATE attribute_templates
SET status = CASE WHEN status = 'active' THEN 'demoted' ELSE status END,
    shadow_mismatches = shadow_mismatches + 1,
    demoted_at = now()
WHERE id = $1`, templateID)
	if err != nil {
		return fmt.Errorf("attrules: демоушн шаблона %d: %w", templateID, err)
	}
	return nil
}
