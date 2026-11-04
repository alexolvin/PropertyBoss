// shadow.go — обработка теневого контроля (C8): pending-строки
// template_shadow_checks — реальные LLM-вызовы по одному атрибуту.
//
// Расхождение — это не только другое значение: LLM не вернула значение
// (отсутствует в ответе или отброшено валидацией) — тоже расхождение.
// Пустого ответа «не считается покрытым»: шаблон, который не подтверждает
// даже LLM, — неверный (класс C12: покрытие считает неверное значение
// покрытым). Ошибка обращения (сеть, 5xx) — НЕ расхождение: строка
// остаётся pending и повторится в следующем прогоне. ErrPermanent —
// остановка прогона с ошибкой (неверный ключ — дальше бессмысленно).
//
// Контракт LLM — тот же, что у основного извлечения: только
// description_original (без заголовка). Для mined-шаблонов это ровно
// то же, что видел LLM при мининге: гард мининга (pattern ⊆ quote ⊆
// описание) гарантирует, что хит mined-шаблона лежит в описании.
package extract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/attrules"
)

// shadowInto — теневой контроль в конце прогона (обёртка ProcessShadow
// с заполнением отчёта). Вызывается при ShadowSamplePct > 0 — и на пути
// «кандидатов нет» (теневая очередь независима от извлекательской).
// budgetLeft — остаток токенового бюджета прогона (0 — без лимита):
// теневого токены считаются в полёт, но в llm_extractions не пишутся
// (не извлечение; PK (object_id, model, attr_list_version, source_hash)
// было бы занято основным прогоном).
func shadowInto(rep *Report, ctx context.Context, pool *pgxpool.Pool, client *Client, c *Config, budgetLeft int) error {
	if c.ShadowSamplePct <= 0 {
		return nil
	}
	done, mism, pend, errs, err := ProcessShadow(ctx, pool, client, c.MaxConfidence, budgetLeft)
	if err != nil {
		return fmt.Errorf("extract: теневой контроль: %w", err)
	}
	rep.ShadowDone, rep.ShadowMismatch, rep.ShadowPending, rep.ShadowErrors = done, mism, pend, errs
	if done+mism > 0 {
		log.Printf("extract: теневой контроль: совпало %d, расхождений %d, pending %d, ошибок %d (демоушены — attribute_templates.status='demoted')",
			done, mism, pend, errs)
	}
	return nil
}

// ProcessShadow — обработка pending-строк теневого контроля. Возвращает
// (done, mismatch, pending, errors, err): err != nil — только
// ErrPermanent/ctx (остановка); pending — осталось (бюджет исчерпан
// либо строка ждёт поправки реестра — повторится в следующем прогоне).
func ProcessShadow(ctx context.Context, pool *pgxpool.Pool, client *Client,
	maxConf float64, budgetLeft int) (done, mismatch, pending, errs int, err error) {
	rows, err := pool.Query(ctx, `
SELECT sc.id, sc.template_id, sc.object_id, sc.attr_key, sc.template_value,
       o.description_original,
       ar.data_type, ar.allowed_values, ar.value_min, ar.value_max
FROM template_shadow_checks sc
JOIN attribute_templates t ON t.id = sc.template_id
JOIN objects o ON o.id = sc.object_id
LEFT JOIN attribute_registry ar
       ON ar.country = t.country AND ar.key = sc.attr_key
WHERE sc.status = 'pending'
ORDER BY sc.id`)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("теневой контроль: очередь: %w", err)
	}
	defer rows.Close()

	type check struct {
		id         int64
		templateID int64
		objectID   int64
		attrKey    string
		tplValue   any
		desc       *string
		dataType   *string
		allowed    []byte
		min, max   *float64
	}
	var checks []check
	for rows.Next() {
		var (
			ch      check
			tplRaw  []byte
			desc    *string
			dt      *string
			allowed []byte
			mn, mx  *float64
		)
		if err := rows.Scan(&ch.id, &ch.templateID, &ch.objectID, &ch.attrKey,
			&tplRaw, &desc, &dt, &allowed, &mn, &mx); err != nil {
			return 0, 0, 0, 0, fmt.Errorf("теневой контроль: чтение строки: %w", err)
		}
		ch.desc, ch.dataType, ch.allowed, ch.min, ch.max = desc, dt, allowed, mn, mx
		if len(tplRaw) > 0 && string(tplRaw) != "null" {
			if err := json.Unmarshal(tplRaw, &ch.tplValue); err != nil {
				return 0, 0, 0, 0, fmt.Errorf("теневой контроль: template_value строки %d: %w", ch.id, err)
			}
		}
		checks = append(checks, ch)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("теневой контроль: %w", err)
	}
	if len(checks) == 0 {
		return 0, 0, 0, 0, nil
	}
	log.Printf("extract: теневой контроль: строк в очереди %d", len(checks))

	left := func() int { return len(checks) - done - mismatch - errs }
	tokens := 0
	for _, ch := range checks {
		if ctx.Err() != nil {
			return done, mismatch, left(), errs, ctx.Err()
		}
		// Строка без реестра — аномалия данных (шаблоны существуют только
		// для атрибутов реестра): не расхождение и не совпадение — в
		// отчёт, строка остаётся pending, пока реестр не починят.
		if ch.dataType == nil {
			errs++
			log.Printf("extract: теневой контроль: строка %d (объект %d, %s): атрибут не в реестре — строка остаётся pending",
				ch.id, ch.objectID, ch.attrKey)
			continue
		}
		spec := attrSpec{Key: ch.attrKey, DataType: *ch.dataType, Min: ch.min, Max: ch.max}
		if len(ch.allowed) > 0 && string(ch.allowed) != "null" {
			if err := json.Unmarshal(ch.allowed, &spec.Values); err != nil {
				errs++
				log.Printf("extract: теневой контроль: строка %d: allowed_values: %v", ch.id, err)
				continue
			}
		}
		switch spec.DataType {
		case "bool":
			if len(spec.Values) == 0 {
				spec.Values = []string{"false", "true"}
			}
		case "enum":
			if len(spec.Values) == 0 {
				errs++
				log.Printf("extract: теневой контроль: строка %d: enum без allowed_values — реестр неполон", ch.id)
				continue
			}
		}
		// Пустое описание — LLM нечего спросить. Хит mined-шаблона в
		// пустом описании невозможен (мининг — из описания), значит
		// строка чужая (ручной шаблон) — расхождение, как пустой ответ.
		if ch.desc == nil || *ch.desc == "" {
			if e := shadowResult(ctx, pool, ch.id, "mismatch", nil); e != nil {
				return done, mismatch, left(), errs, e
			}
			if e := attrules.Demote(ctx, pool, ch.templateID); e != nil {
				return done, mismatch, left(), errs, e
			}
			mismatch++
			log.Printf("extract: теневой контроль: объект %d (%s): пустое описание — расхождение, шаблон %d демоушен",
				ch.objectID, ch.attrKey, ch.templateID)
			continue
		}
		user := fmt.Sprintf("Listing text (original):\n\"\"\"%s\"\"\"\n\nRequested attributes (key (type); constraints from the attribute registry):\n%s\n\nRespond with JSON: {\"%s\": {\"value\": ..., \"confidence\": ..., \"quote\": <verbatim phrase from the listing>, \"pattern\": <shortest verbatim substring of quote carrying the signal>}}",
			*ch.desc, promptAttrs([]attrSpec{spec}), ch.attrKey)
		text, _, tk, cErr := client.Complete(ctx, systemPrompt, user)
		if cErr != nil {
			var perm *ErrPermanent
			if errors.As(cErr, &perm) {
				return done, mismatch, left(), errs,
					fmt.Errorf("теневой контроль: объект %d: %w", ch.objectID, perm)
			}
			errs++
			log.Printf("extract: теневой контроль: объект %d (%s): %v — строка остаётся pending",
				ch.objectID, ch.attrKey, cErr)
			continue
		}
		tokens += tk
		var valid []validated
		got, pErr := parseResponse(text)
		if pErr != nil {
			// Ответ не JSON — LLM не вернула проверяемое значение.
			// Это НЕ «сбой обращения» (обращение состоялось): пустой
			// ответ LLM = расхождение (C8).
			log.Printf("extract: теневой контроль: объект %d: ответ модели не JSON — расхождение", ch.objectID)
		} else {
			valid, _ = validateAttrs(got, map[string]attrSpec{ch.attrKey: spec}, maxConf, nil)
		}
		var llmVal any
		if len(valid) > 0 {
			llmVal = valid[0].Value
		}
		if llmVal != nil && attrules.ValuesEqual(llmVal, ch.tplValue) {
			if e := shadowResult(ctx, pool, ch.id, "done", llmVal); e != nil {
				return done, mismatch, left(), errs, e
			}
			done++
		} else {
			// Расхождение (другое значение или пустой ответ) — авто-демоушн.
			if e := shadowResult(ctx, pool, ch.id, "mismatch", llmVal); e != nil {
				return done, mismatch, left(), errs, e
			}
			if e := attrules.Demote(ctx, pool, ch.templateID); e != nil {
				return done, mismatch, left(), errs, e
			}
			mismatch++
			log.Printf("extract: теневой контроль: объект %d (%s): шаблон %d — расхождение (шаблон %s, LLM %s) — демоушен",
				ch.objectID, ch.attrKey, ch.templateID, jsonShortValue(ch.tplValue), jsonShortValue(llmVal))
		}
		// Бюджет: теневого токены идут в полёт (см. пакетный комментарий).
		if budgetLeft > 0 && tokens >= budgetLeft {
			log.Printf("extract: теневой контроль: бюджет %d токенов исчерпан — остальные строки pending (повторится в следующем прогоне)", budgetLeft)
			return done, mismatch, left(), errs, nil
		}
	}
	return done, mismatch, 0, errs, nil
}

// shadowResult — результат проверки строки: статус done|mismatch,
// llm_value (NULL при пустом ответе LLM — расхождение), checked_at.
func shadowResult(ctx context.Context, pool *pgxpool.Pool, checkID int64, status string, llmVal any) error {
	var raw any
	if llmVal != nil {
		b, err := json.Marshal(llmVal)
		if err != nil {
			return fmt.Errorf("теневой контроль: llm_value: %w", err)
		}
		raw = b
	}
	_, err := pool.Exec(ctx, `
UPDATE template_shadow_checks
SET status = $2, llm_value = $3, checked_at = now()
WHERE id = $1`, checkID, status, raw)
	if err != nil {
		return fmt.Errorf("теневой контроль: строка %d: %w", checkID, err)
	}
	return nil
}

func jsonShortValue(v any) string {
	if v == nil {
		return "пусто"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
