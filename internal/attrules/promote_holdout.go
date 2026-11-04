// promote_holdout.go — авто-промоушн с холдаут-проверкой (C8).
//
// Порог повторений не защищает от систематической ошибки: баг «celkov»
// (шаблон, который не срабатывает в Apply, но проходил substring-гард
// мининга) повторился бы 160 раз и прошёл бы любой порог. Поэтому
// подтверждения каждого шаблона делятся на подгонку и проверку: холдаут
// = последние 2 подтверждения (при пороге >= 5 на подгонку остаётся
// >= 3). Шаблон промоутится автоматически только при 100% совпадении с
// LLM-значением на отложенных и conflicts = 0.
//
// Проверка машинная (ручного разбора не требует) и точная по
// построению: LLM видела только description_original (без заголовка),
// значит и холдаут проверяется Apply("", description_original);
// движок — тот же код пути применения (Load/Apply), не упрощённая
// substring-проверка мининга. Не удалось проверить (объекта/описания/
// LLM-строки нет) — не промоутируем: честный срыв, а не догадка
// (регламент п. 12).
package attrules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Promote — авто-промоушн candidate → active с холдаут-проверкой
// (конец прогона extract). Условия: occurrences >= minOccurrences,
// conflicts = 0, холдаут (последние 2 из occurrence_ids) — шаблон
// срабатывает в Apply на описании объекта и даёт значение, равное
// LLM-значению в attribute_values. country != "" — только шаблоны этой
// страны (прогон минит свою страну; чужие страны не трогаем — в т.ч.
// чтобы live-тест на песочнице VX не промоутил реальные кандидаты);
// country == "" — все страны. Возвращает число промоутированных
// шаблонов. minOccurrences <= 0 — промоушн выключен (порог не задан):
// иначе «>= 0» промоутировало бы всех.
func Promote(ctx context.Context, pool *pgxpool.Pool, country string, minOccurrences int) (int, error) {
	if minOccurrences <= 0 {
		return 0, nil
	}
	var (
		query string
		args  []any
	)
	if country != "" {
		query = `
SELECT t.id, t.attr_key, t.pattern_type, t.pattern, t.value, t.confidence,
       t.occurrence_ids, t.occurrences, ar.value_min, ar.value_max
FROM attribute_templates t
LEFT JOIN attribute_registry ar
       ON ar.country = t.country AND ar.key = t.attr_key
WHERE t.country = $1 AND t.status = 'candidate' AND t.conflicts = 0
  AND t.occurrences >= $2
ORDER BY t.id`
		args = []any{country, minOccurrences}
	} else {
		query = `
SELECT t.id, t.attr_key, t.pattern_type, t.pattern, t.value, t.confidence,
       t.occurrence_ids, t.occurrences, ar.value_min, ar.value_max
FROM attribute_templates t
LEFT JOIN attribute_registry ar
       ON ar.country = t.country AND ar.key = t.attr_key
WHERE t.status = 'candidate' AND t.conflicts = 0
  AND t.occurrences >= $1
ORDER BY t.id`
		args = []any{minOccurrences}
	}
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("attrules: кандидаты на промоушн: %w", err)
	}
	defer rows.Close()

	type cand struct {
		tpl      Template
		occIDs   []int64
		occTotal int
	}
	var cands []cand
	for rows.Next() {
		var (
			c   cand
			raw []byte
			mn  *float64
			mx  *float64
		)
		if err := rows.Scan(&c.tpl.ID, &c.tpl.AttrKey, &c.tpl.PatternType, &c.tpl.Pattern,
			&raw, &c.tpl.Confidence, &c.occIDs, &c.occTotal, &mn, &mx); err != nil {
			return 0, fmt.Errorf("attrules: чтение кандидата: %w", err)
		}
		c.tpl.Min, c.tpl.Max = mn, mx
		if len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &c.tpl.Value); err != nil {
				return 0, fmt.Errorf("attrules: value шаблона %d: %w", c.tpl.ID, err)
			}
		}
		if c.tpl.PatternType == "regex" {
			re, err := regexp.Compile(c.tpl.Pattern)
			if err != nil {
				return 0, fmt.Errorf("attrules: pattern шаблона %d: %w", c.tpl.ID, err)
			}
			if c.tpl.Value == nil && re.NumSubexp() == 0 {
				return 0, fmt.Errorf("attrules: динамический шаблон %d без числового capture", c.tpl.ID)
			}
			c.tpl.re = re
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("attrules: %w", err)
	}

	promoted := 0
	for _, c := range cands {
		if len(c.occIDs) < 2 {
			log.Printf("attrules: шаблон %d (%s %q) НЕ промоутится: подтверждений %d < 2 для холдаута (occurrences=%d)",
				c.tpl.ID, c.tpl.AttrKey, c.tpl.Pattern, len(c.occIDs), c.occTotal)
			continue
		}
		// Холдаут — последние 2 в порядке подтверждений; остальное —
		// подгонка (на пороге >= 5 остаётся >= 3).
		ok := true
		for _, objID := range c.occIDs[len(c.occIDs)-2:] {
			if err := verifyHoldoutObject(ctx, pool, &c.tpl, objID); err != nil {
				log.Printf("attrules: шаблон %d (%s %q) НЕ промоутится: холдаут объект %d: %v",
					c.tpl.ID, c.tpl.AttrKey, c.tpl.Pattern, objID, err)
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		tag, err := pool.Exec(ctx, `
UPDATE attribute_templates
SET status = 'active', promoted_at = now()
WHERE id = $1 AND status = 'candidate'`, c.tpl.ID)
		if err != nil {
			return promoted, fmt.Errorf("attrules: промоушн шаблона %d: %w", c.tpl.ID, err)
		}
		if tag.RowsAffected() == 1 {
			promoted++
			log.Printf("attrules: шаблон %d (%s %q) промоутится: холдаут 2/2 (подтверждений %d)",
				c.tpl.ID, c.tpl.AttrKey, c.tpl.Pattern, len(c.occIDs))
		}
	}
	return promoted, nil
}

// verifyHoldoutObject — машинная проверка одного отложенного объекта
// (C8): (a) текущий движок Apply срабатывает на description_original
// объекта (LLM видела только описание — без заголовка); (b) значение
// хита равно текущему значению объекта для этого атрибута. Любая
// невозможность проверить (нет объекта, нет описания, нет значения,
// нет срабатывания) — ошибка: не промоутируем.
func verifyHoldoutObject(ctx context.Context, pool *pgxpool.Pool, tpl *Template, objID int64) error {
	var (
		desc    *string
		attrRaw []byte
	)
	// Значение живёт только в objects.attributes (save-путь extract:
	// слияние attributes || $2::jsonb). attribute_values несёт только
	// провенанс (колонки значения нет) и пишется first-writer
	// (ON CONFLICT DO NOTHING) — он может расходиться с текущим
	// значением, поэтому фильтром не служит (D19). Опорное значение —
	// текущее в attributes: ближайший доступный прокси LLM-значения
	// (при мининге значение LLM было сохранено именно сюда).
	err := pool.QueryRow(ctx, `
SELECT description_original, attributes -> $2
FROM objects WHERE id = $1`, objID, tpl.AttrKey).Scan(&desc, &attrRaw)
	if err != nil {
		return fmt.Errorf("запрос: %w", err)
	}
	if desc == nil || *desc == "" {
		return errors.New("нет description_original — проверить срабатывание невозможно")
	}
	if attrRaw == nil || string(attrRaw) == "null" {
		return errors.New("нет текущего значения атрибута в attributes — сравнить не с чем")
	}
	var attrVal any
	if err := json.Unmarshal(attrRaw, &attrVal); err != nil {
		return fmt.Errorf("значение атрибута: %w", err)
	}
	// Один шаблон — тот же код пути, что применение (Apply).
	eng := &Engine{tpls: []Template{*tpl}}
	hits := eng.Apply("", *desc, map[string]bool{tpl.AttrKey: true})
	h, fired := hits[tpl.AttrKey]
	if !fired {
		return errors.New("шаблон не срабатывает в Apply на описании холдаута (проходил substring-гард мининга — класс бага C12)")
	}
	if !jsonEqual(h.Value, attrVal) {
		return fmt.Errorf("значение шаблона %s ≠ текущему значению объекта %s", jsonString(h.Value), jsonString(attrVal))
	}
	return nil
}

// jsonEqual — равенство двух JSON-значений: unmarshal → marshal → строки
// (1 и 1.0 — одно и то же; true и "true" — разные).
func jsonEqual(a, b any) bool {
	ab, e1 := json.Marshal(a)
	bb, e2 := json.Marshal(b)
	if e1 != nil || e2 != nil {
		return false
	}
	return string(ab) == string(bb)
}

func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
