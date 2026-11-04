// mine.go — мининг: LLM-успех с дословным доказательством становится
// программным шаблоном (ТЗ v4 §4.2, гибридное извлечение). Арсенал растёт
// без пересборки: шаблон — данные в БД (source='llm_mined').
//
// Минится только literal-шаблон (дословная фраза). Гарды (CheckMine)
// делают срыв честным — при любом нарушении шаблон не создаётся:
//   - pattern ≥ 2 символов;
//   - без цифр (фразы с числами не обобщаются — это зона Go-регуляров/LLM);
//   - pattern ⊆ quote ⊆ текст (после Normalize) — дословность: цитата
//     действительно из текста, pattern — подмножество цитаты.
//
// Уверенность mined-шаблона ограничена mined_confidence_max (конфиг),
// как и llm_extraction (ТЗ v4 §4.3). Авто-промоушн candidate → active —
// только при пороге повторений (min_mined_occurrences), нулевых
// конфликтах И 100% совпадении с LLM-значением на холдауте (последние 2
// подтверждения — машина проверяет, что шаблон СРАБАТЫВАЕТ движком
// Apply и даёт то же значение, что LLM; promote_holdout.go, C8).
// Конфликт — другое значение при том же паттерне — блокирует промоушн,
// решение за оператором (pb rules list).
package attrules

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CheckMine — гарды мининга (чистая функция, для unit-тестов). Возвращает
// nil, если mined-шаблон можно создать.
func CheckMine(pattern, quote, text string) error {
	p := Normalize(pattern)
	q := Normalize(quote)
	t := Normalize(text)
	if utf8.RuneCountInString(p) < 2 {
		return fmt.Errorf("attrules: pattern короче 2 символов (%q)", pattern)
	}
	if hasDigit(p) {
		return fmt.Errorf("attrules: pattern содержит цифры — не обобщается (%q)", pattern)
	}
	if !strings.Contains(q, p) {
		return fmt.Errorf("attrules: pattern не входит дословно в quote")
	}
	if !strings.Contains(t, q) {
		return fmt.Errorf("attrules: quote не входит дословно в текст объявления")
	}
	return nil
}

func hasDigit(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// Mine — записать (или усилить) mined-шаблон из LLM-успеха.
//
//	pattern  — кратчайшая дословная фраза из quote (хранится как есть,
//	           после TrimSpace; сопоставление — по Normalize);
//	quote    — дословная цитата-доказательство (колонка evidence);
//	text     — полный текст объявления (для ⊆-проверки);
//	value    — типизированное значение (bool/строка enum; чисел быть не
//	           может — цифры в pattern запрещены);
//	confidence — уже ограничен mined_confidence_max (вызывающий);
//	objectID — id объекта-доказательства (примеры для ревизии);
//	maxExamples — сколько примеров хранить (rules.example_objects).
//
// Возвращает (mined, err): mined=false без err — гард сработал (честный
// срыв, не ошибка прогона); err — сбой БД. Семантика upsert:
//   - нет строки → candidate, occurrences=1, evidence=quote;
//   - строка с тем же значением и объект ещё не учтён (не в
//     occurrence_ids) → occurrences+1, id — в конец occurrence_ids
//     (порядок подтверждений; дедупликация строгая, без ограничения
//     длины — повторный запуск прогона по тому же объекту не
//     наращивает счётчик, C8);
//   - другое значение → conflicts+1, status→'conflict' (авто-промоушн
//     невозможен, решение за оператором); id в occurrence_ids при
//     конфликте НЕ попадает (объект — не подтверждение этого значения);
//   - confidence = LEAST(старая, новая), примеры (example_object_ids) —
//     distinct-сложение с ограничением maxExamples (для ревизии).
func Mine(ctx context.Context, pool *pgxpool.Pool, country, attrKey string,
	value any, confidence float64, text, quote, pattern string,
	objectID int64, maxExamples int) (bool, error) {
	// Уверенность <= 0 — сигнала нет: модель заявила, что не доверяет
	// извлечению, а шаблон с нулевой уверенностью не создаётся
	// (constraint confidence > 0). Честный срыв, как и гарды CheckMine.
	// Само значение при этом может быть записано в objects.attributes —
	// это честная запись с нулевой уверенностью, из моделей она исключена
	// порогом min_confidence (ТЗ v4 §4.5).
	if confidence <= 0 {
		return false, nil
	}
	if err := CheckMine(pattern, quote, text); err != nil {
		return false, nil
	}
	valJSON, err := json.Marshal(value)
	if err != nil {
		return false, fmt.Errorf("attrules: value %s: %w", attrKey, err)
	}
	p := strings.TrimSpace(pattern)
	_, err = pool.Exec(ctx, `
INSERT INTO attribute_templates
    (country, attr_key, pattern_type, pattern, value, status,
     confidence, source, evidence, occurrences, conflicts,
     example_object_ids, occurrence_ids)
VALUES ($1, $2, 'literal', $3, $4::jsonb, 'candidate', $5,
        'llm_mined', $6, 1, 0, ARRAY[$7]::bigint[], ARRAY[$7]::bigint[])
ON CONFLICT (country, attr_key, pattern) DO UPDATE SET
    -- EXCLUDED.occurrence_ids = ARRAY[новый objectID]; объект новый, если
    -- его id ещё не в порядке подтверждений.
    occurrences = CASE
        WHEN attribute_templates.value = EXCLUDED.value
         AND NOT ((EXCLUDED.occurrence_ids)[1] = ANY(attribute_templates.occurrence_ids))
        THEN attribute_templates.occurrences + 1
        ELSE attribute_templates.occurrences END,
    occurrence_ids = CASE
        WHEN attribute_templates.value = EXCLUDED.value
         AND NOT ((EXCLUDED.occurrence_ids)[1] = ANY(attribute_templates.occurrence_ids))
        THEN attribute_templates.occurrence_ids || EXCLUDED.occurrence_ids
        ELSE attribute_templates.occurrence_ids END,
    conflicts   = CASE WHEN attribute_templates.value = EXCLUDED.value
                       THEN attribute_templates.conflicts
                       ELSE attribute_templates.conflicts + 1 END,
    status      = CASE WHEN attribute_templates.value = EXCLUDED.value
                       THEN attribute_templates.status
                       WHEN attribute_templates.status IN ('candidate','active')
                       THEN 'conflict'
                       ELSE attribute_templates.status END,
    confidence  = LEAST(attribute_templates.confidence, EXCLUDED.confidence),
    example_object_ids = (
        SELECT (ARRAY(
            SELECT d.x
            FROM (
                SELECT DISTINCT u.x
                FROM unnest(
                    attribute_templates.example_object_ids || ARRAY[$7]::bigint[]
                ) AS u(x)
            ) d
            ORDER BY d.x
        ))[1:$8]
    )`,
		country, attrKey, p, string(valJSON), confidence,
		strings.TrimSpace(quote), objectID, maxExamples)
	if err != nil {
		return false, err
	}
	return true, nil
}

// Promote — авто-промоушн с холдаут-проверкой, см. promote_holdout.go.

// ReadyCount — сколько кандидатов готовы к промоушну (те же условия, что у
// Promote: порога повторений достигли, конфликтов нет), БЕЗ смены status.
// Для отчёта, когда промоушн удержан до ревизии (rules.auto_promote=false):
// оператор видит, какие кандидаты можно включать (pb rules set). country —
// как у Promote ("" — все страны). minOccurrences <= 0 — 0 (порог не задан).
func ReadyCount(ctx context.Context, pool *pgxpool.Pool, country string, minOccurrences int) (int, error) {
	if minOccurrences <= 0 {
		return 0, nil
	}
	var (
		query string
		args  []any
	)
	if country != "" {
		query = `
SELECT count(*) FROM attribute_templates
WHERE country = $1
  AND status = 'candidate'
  AND conflicts = 0
  AND occurrences >= $2`
		args = []any{country, minOccurrences}
	} else {
		query = `
SELECT count(*) FROM attribute_templates
WHERE status = 'candidate'
  AND conflicts = 0
  AND occurrences >= $1`
		args = []any{minOccurrences}
	}
	var n int
	if err := pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("attrules: готовых к промоушну: %w", err)
	}
	return n, nil
}
