// run.go — оффлайн-прогон шаблонного слоя по существующим объектам
// (ТЗ v4 §4.2, гибридное извлечение). Текст берётся из БД
// (objects.description_original) — без сети: 8k объектов за секунды, в
// отличие от ~10 ч полного LLM-прогона. У не-обогащённых объектов это
// текст карточки объявления (createObject) — явный источник, использовать
// правомерно (detail_fetched_at — не требование). Пишет только в
// пропущенные атрибуты реестра + в пустое зеркало area_sqm →
// идемпотентно (повторный прогон → 0). rooms остаётся исключённым: чисел
// комнат из текста не берём.
package attrules

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RunReport — результат оффлайн-прогона.
type RunReport struct {
	Scanned    int // объектов-кандидатов (пропущенный атрибут или пустое зеркало area_sqm)
	Written    int // объектов, получивших шаблонные значения
	Values     int // строк attribute_values записано
	AreaMirror int // заполнено пустых зеркал objects.area_sqm
	// SellerMirror (D15): заполнено пустых зеркал objects.seller_type —
	// шаблонный слой согласован с enrich/extract: писатели зеркал
	// единые, «только в пустую колонку».
	SellerMirror int
	// ShadowEnqueued (C8): новых строк очереди теневого контроля
	// (хиты ПРОМОУТНУТЫХ шаблонов, детерминированная выборка).
	ShadowEnqueued int
}

// runCand — кандидат: объект с текстом (деталь или карточка).
type runCand struct {
	id      int64
	country string
	desc    string
	attrs   map[string]any // objects.attributes
	areaCol *float64       // objects.area_sqm (NULL — пустое зеркало)
}

// Run — прогон шаблонного слоя.
//
//	country — один рынок ("" — все); limit — потолок кандидатов (0 — все);
//	shadowSamplePct — доля объектов, хиты ПРОМОУТНУТЫХ шаблонов которых
//	уходят в теневой контроль (C8; 0 — тень выключена).
//
// Кандидат — активный объект с непустым описанием, у которого есть
// пропущенный атрибут реестра (ни в attributes, ни в attribute_values)
// либо пустое зеркало area_sqm. Движок применяется только к пропущенным
// ключам; хиты пишутся в attributes (слияние) + attribute_values
// (provenance, method 'rule_text' — ТЗ v4-2 §4.2); хит area_sqm
// дополнительно заполняет пустую зеркальную колонку (UPDATE ... WHERE
// area_sqm IS NULL — уже полученное значение не переписывается). rooms
// исключён.
func Run(ctx context.Context, pool *pgxpool.Pool, country string, limit, shadowSamplePct int) (*RunReport, error) {
	rep := &RunReport{}

	args := []any{country}
	q := `
SELECT o.id, o.country, o.description_original, o.attributes, o.area_sqm
FROM objects o
WHERE o.status = 'active'
  AND o.description_original IS NOT NULL
  AND o.description_original <> ''
  AND ($1::text = '' OR o.country = $1)
  AND EXISTS (
      SELECT 1 FROM attribute_registry ar
      WHERE ar.country = o.country
        AND ar.key <> 'rooms'
        AND (ar.key <> 'area_sqm' OR o.area_sqm IS NULL)
        AND NOT EXISTS (
            SELECT 1 FROM attribute_values av
            WHERE av.object_id = o.id AND av.attr_key = ar.key)
        AND NOT (o.attributes ? ar.key))
ORDER BY o.id`
	if limit > 0 {
		args = append(args, limit)
		q += "\nLIMIT $2"
	}
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("attrules: кандидаты: %w", err)
	}
	var cands []runCand
	for rows.Next() {
		var (
			c        runCand
			desc     string
			attrsRaw []byte
			area     *float64
		)
		if err := rows.Scan(&c.id, &c.country, &desc, &attrsRaw, &area); err != nil {
			rows.Close()
			return nil, fmt.Errorf("attrules: чтение кандидатов: %w", err)
		}
		c.desc = desc
		c.areaCol = area
		c.attrs = map[string]any{}
		if len(attrsRaw) > 0 {
			if err := json.Unmarshal(attrsRaw, &c.attrs); err != nil {
				rows.Close()
				return nil, fmt.Errorf("attrules: attributes объекта %d: %w", c.id, err)
			}
		}
		cands = append(cands, c)
	}
	scanErr := rows.Err()
	rows.Close()
	if scanErr != nil {
		return nil, fmt.Errorf("attrules: кандидаты: %w", scanErr)
	}
	rep.Scanned = len(cands)
	if len(cands) == 0 {
		return rep, nil
	}

	regKeys, err := regKeysByCountry(ctx, pool, candCountries(cands))
	if err != nil {
		return nil, err
	}
	avKeys, err := avKeysByID(ctx, pool, candIDs(cands))
	if err != nil {
		return nil, err
	}

	// Движки по странам — кэш на прогон (Load читает активные шаблоны).
	engines := map[string]*Engine{}
	engFor := func(c string) (*Engine, error) {
		if e, ok := engines[c]; ok {
			return e, nil
		}
		e, err := Load(ctx, pool, c)
		if err != nil {
			return nil, err
		}
		engines[c] = e
		return e, nil
	}

	for _, c := range cands {
		if ctx.Err() != nil {
			return rep, ctx.Err()
		}
		reg := regKeys[c.country]
		if len(reg) == 0 {
			continue
		}
		// Пропущенные ключи: ни в attributes, ни в attribute_values
		// (только их и заполняем — идемпотентность). rooms (зеркало)
		// исключён; area_sqm (зеркало) — только при пустой колонке.
		missing := map[string]bool{}
		av := avKeys[c.id]
		for k := range reg {
			if MirrorKeys[k] {
				if k == "area_sqm" && c.areaCol == nil {
					// Пустое зеркало: шаблонный хит правомерен (явный
					// источник в тексте), запись — только в NULL (ниже).
					if _, has := c.attrs[k]; has {
						continue
					}
					if av[k] {
						continue
					}
					missing[k] = true
				}
				continue
			}
			if _, has := c.attrs[k]; has {
				continue
			}
			if av[k] {
				continue
			}
			missing[k] = true
		}
		if len(missing) == 0 {
			continue
		}
		eng, err := engFor(c.country)
		if err != nil {
			return nil, fmt.Errorf("attrules: движок %s: %w", c.country, err)
		}
		hits := eng.Apply("", c.desc, missing)
		if len(hits) == 0 {
			continue
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return nil, fmt.Errorf("attrules: tx объект %d: %w", c.id, err)
		}
		merged := map[string]any{}
		for _, h := range hits {
			tag, err := tx.Exec(ctx, `
INSERT INTO attribute_values (object_id, attr_key, extracted_by, confidence)
VALUES ($1, $2, 'rule_text', $3)
ON CONFLICT (object_id, attr_key) DO NOTHING`,
				c.id, h.Key, h.Confidence)
			if err != nil {
				_ = tx.Rollback(ctx)
				return nil, fmt.Errorf("attrules: attribute_values %s объекта %d: %w", h.Key, c.id, err)
			}
			if tag.RowsAffected() > 0 {
				merged[h.Key] = h.Value
			}
		}
		if len(merged) == 0 {
			_ = tx.Rollback(ctx) // гонка: все хиты уже записаны
			continue
		}
		mergedJSON, err := json.Marshal(merged)
		if err != nil {
			_ = tx.Rollback(ctx)
			return nil, fmt.Errorf("attrules: attributes JSON объекта %d: %w", c.id, err)
		}
		if _, err := tx.Exec(ctx, `
UPDATE objects SET attributes = attributes || $2::jsonb
WHERE id = $1`, c.id, string(mergedJSON)); err != nil {
			_ = tx.Rollback(ctx)
			return nil, fmt.Errorf("attrules: attributes объекта %d: %w", c.id, err)
		}
		// Зеркало area_sqm: только в пустую колонку (IS NULL в WHERE —
		// идемпотентность + «уже полученное значение не переписывается»).
		if v, ok := merged["area_sqm"]; ok && c.areaCol == nil {
			if f, ok := v.(float64); ok {
				tag, err := tx.Exec(ctx, `
UPDATE objects SET area_sqm = $2
WHERE id = $1 AND area_sqm IS NULL`, c.id, f)
				if err != nil {
					_ = tx.Rollback(ctx)
					return nil, fmt.Errorf("attrules: area_sqm объекта %d: %w", c.id, err)
				}
				if tag.RowsAffected() > 0 {
					rep.AreaMirror++
				}
			}
		}
		// Зеркало seller_type (D15): те же правила — только в пустую
		// колонку; тип значения проверен реестром при создании шаблона.
		if v, ok := merged["seller_type"]; ok {
			if s, ok := v.(string); ok {
				tag, err := tx.Exec(ctx, `
UPDATE objects SET seller_type = $2
WHERE id = $1 AND seller_type IS NULL`, c.id, s)
				if err != nil {
					_ = tx.Rollback(ctx)
					return nil, fmt.Errorf("attrules: seller_type объекта %d: %w", c.id, err)
				}
				if tag.RowsAffected() > 0 {
					rep.SellerMirror++
				}
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("attrules: commit объекта %d: %w", c.id, err)
		}
		// Теневой контроль (C8): запись зафиксирована — хиты
		// ПРОМОУТНУТЫХ шаблонов в очередь (доля shadowSamplePct,
		// детерминированная выборка по id объекта).
		for _, h := range hits {
			if !h.Promoted {
				continue
			}
			ins, err := Enqueue(ctx, pool, h.TemplateID, c.id, h.Key, h.Value, shadowSamplePct)
			if err != nil {
				return nil, fmt.Errorf("attrules: теневая очередь объекта %d: %w", c.id, err)
			}
			if ins {
				rep.ShadowEnqueued++
			}
		}
		rep.Written++
		rep.Values += len(merged)
	}
	return rep, nil
}

// regKeysByCountry — ключи реестра по странам (кандидат → допустимые
// атрибуты).
func regKeysByCountry(ctx context.Context, pool *pgxpool.Pool, countries []string) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	if len(countries) == 0 {
		return out, nil
	}
	rows, err := pool.Query(ctx, `
SELECT country, key FROM attribute_registry WHERE country = ANY($1)`, countries)
	if err != nil {
		return nil, fmt.Errorf("attrules: реестр: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			ct  string
			key string
		)
		if err := rows.Scan(&ct, &key); err != nil {
			return nil, fmt.Errorf("attrules: реестр: %w", err)
		}
		if out[ct] == nil {
			out[ct] = map[string]bool{}
		}
		out[ct][key] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("attrules: реестр: %w", err)
	}
	return out, nil
}

// avKeysByID — provenance (attribute_values) по объектам: какие атрибуты
// уже получены шагами 1–2 / предыдущими прогоннами.
func avKeysByID(ctx context.Context, pool *pgxpool.Pool, ids []int64) (map[int64]map[string]bool, error) {
	out := map[int64]map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := pool.Query(ctx, `
SELECT object_id, attr_key FROM attribute_values WHERE object_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("attrules: provenance: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			oid int64
			key string
		)
		if err := rows.Scan(&oid, &key); err != nil {
			return nil, fmt.Errorf("attrules: provenance: %w", err)
		}
		if out[oid] == nil {
			out[oid] = map[string]bool{}
		}
		out[oid][key] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("attrules: provenance: %w", err)
	}
	return out, nil
}

func candCountries(cands []runCand) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range cands {
		if !seen[c.country] {
			seen[c.country] = true
			out = append(out, c.country)
		}
	}
	return out
}

func candIDs(cands []runCand) []int64 {
	out := make([]int64, len(cands))
	for i, c := range cands {
		out[i] = c.id
	}
	return out
}
