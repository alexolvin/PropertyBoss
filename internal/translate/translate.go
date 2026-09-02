// Переводчик описаний (этап 10, ТЗ §11).
//
// Перевод — вне критического пути: `pb translate run` — отдельная
// cron-точка, дедупликация, ценовая модель и уведомления на неё не
// ждут (ТЗ §11). Прогон самовосстанавливающийся: берёт объекты с
// отсутствующими или устаревшими переводами и доводит их до состояния.
//
// Идемпотентность: ключ перевода — sha256(description_original) (ТЗ §11:
// повторный перевод того же текста не выполняется — это же защищает от
// повторных трат при пересканировании). Хеш сравнивается на стороне SQL
// (pgcrypto.digest), чтобы выборка кандидатов не тянула все объекты;
// перевод того же текста с другого объекта переиспользуется без
// обращения к LLM (индекс translations_hash_idx, миграция 0012).
//
// Честность: если перевод не получен (ошибка API, лимит 429) — строка не
// пишется: для UI перевод NULL, показывается оригинал с пометкой
// «перевод недоступен». Подстановка оригинала под видом перевода
// запрещена (ТЗ §11). Каждый сохранённый перевод хранит model,
// translated_at и token_cost.
package translate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pendingCondition — часть выборки кандидатов: объект, у которого нет
// свежего перевода хотя бы для одного целевого языка. Свежий — строка с
// хешом ТЕКУЩЕГО текста (описание изменилось — прежние переводы устарели).
// Сколько языков нужно, — по language_original: оригинал на ru/en требует
// только другой язык (1), прочие языки — оба (2).
const pendingCondition = `
  AND (SELECT count(*) FROM object_translations t
       WHERE t.object_id = o.id
         AND t.lang IN ('ru', 'en')
         AND t.source_hash = encode(digest(o.description_original, 'sha256'), 'hex')
     ) < CASE WHEN o.language_original IN ('ru', 'en') THEN 1 ELSE 2 END`

// Report — результат прогона переводчика.
type Report struct {
	Objects         int `json:"objects"`          // обработано кандидатов
	Translated      int `json:"translated"`       // новых переводов через LLM
	Reused          int `json:"reused"`           // переиспользовано с других объектов (тот же source_hash, без LLM)
	Skipped         int `json:"skipped"`          // перевод уже свежий
	Failed          int `json:"failed"`           // перевод не получен (ошибка LLM, не 429); повторит следующий cron
	LanguageUpdated int `json:"language_updated"` // language_original уточнено детектором
	Tokens          int `json:"tokens"`           // суммарная стоимость прогоном в токенах
}

// StatusReport — состояние конвейера переводов для `pb translate status`.
type StatusReport struct {
	ObjectsWithDesc int            `json:"objects_with_desc"`  // объектов с непустым описанием
	Translated      int            `json:"translated_objects"` // объектов хотя бы с одним свежим переводом
	Rows            int            `json:"rows"`               // всего строк object_translations
	RowsByLang      map[string]int `json:"rows_by_lang"`
	Pending         int            `json:"pending"`  // кандидатов в `run` (то же правило, что selectPending)
	TooLong         int            `json:"too_long"` // описаний длиннее max_chars (в `run` не попадают)
	Models          []string       `json:"models"`   // модели, которыми сделаны сохранённые переводы
	LastTranslated  *time.Time     `json:"last_translated"`
}

// HashDescription — sha256 оригинала, hex: ключ идемпотентности перевода
// (ТЗ §11). Совпадает с encode(digest(..., 'sha256'), 'hex') в SQL.
func HashDescription(desc string) string {
	sum := sha256.Sum256([]byte(desc))
	return hex.EncodeToString(sum[:])
}

// Targets — целевые языки для оригинала заданного языка: {ru, en} минус
// сам оригинал, если он один из них (ТЗ §11: языки UI — ru/en).
func Targets(originalLang string) []string {
	switch originalLang {
	case "ru":
		return []string{"en"}
	case "en":
		return []string{"ru"}
	default:
		return []string{"ru", "en"}
	}
}

// Run — прогон переводчика: добирает объекты с отсутствующим или
// устаревшим переводом (описание изменилось — хеш не сошёл) и заполняет
// ru/en. country == "" — все рынки, иначе — один (стоимость LLM по рынкам
// управляется отдельно, cron-точки по странам). limit <= 0 — без лимита.
// Описания длиннее maxChars в выборку не попадают: они не отправляются
// в LLM (ТЗ §11), и без фильтра занимали бы лимит прогона бесконечно.
//
// Возвращает отчёт и, при останове, причину:
//   - *ErrPermanent — ошибка конфигурации/API: прогон остановлен,
//     повторный запуск упрётся в ту же ошибку (оператору видно);
//   - 429 (ErrRateLimit) — мягкий останов БЕЗ ошибки: дальше по той же
//     выборке тот же лимит, продолжит следующий cron;
//   - прочие ошибки LLM не останавливают прогон: перевод объекта
//     остаётся NULL, следующий cron повторит.
func Run(ctx context.Context, pool *pgxpool.Pool, cl *Client, det Detector, maxChars, limit int, country string) (*Report, error) {
	rep := &Report{}
	q := `
SELECT o.id, o.description_original, o.language_original
FROM objects o
WHERE o.description_original IS NOT NULL
  AND btrim(o.description_original) <> ''
  AND length(o.description_original) <= $1`
	args := []any{maxChars}
	if country != "" {
		q += fmt.Sprintf("\n  AND o.country = $%d", len(args)+1)
		args = append(args, country)
	}
	q += "\n" + pendingCondition + "\nORDER BY o.id"
	if limit > 0 {
		q += fmt.Sprintf("\nLIMIT $%d", len(args)+1)
		args = append(args, limit)
	}
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return rep, fmt.Errorf("translate: выборка кандидатов: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id   int64
			desc string
			lang *string
		)
		if err := rows.Scan(&id, &desc, &lang); err != nil {
			return rep, fmt.Errorf("translate: чтение кандидата: %w", err)
		}
		langStr := ""
		if lang != nil {
			langStr = *lang
		}
		if err := processObject(ctx, pool, cl, det, id, desc, langStr, rep); err != nil {
			var rl *ErrRateLimit
			if errors.As(err, &rl) {
				log.Printf("translate: %v — прогон остановлен, продолжит следующий cron", rl)
				return rep, nil
			}
			return rep, err
		}
	}
	if err := rows.Err(); err != nil {
		return rep, fmt.Errorf("translate: перебор кандидатов: %w", err)
	}
	return rep, nil
}

// processObject — один объект: чистка устаревших переводов, уточнение
// language_original детектором, перевод недостающих целевых языков.
// Возвращает ошибку, которая останавливает прогон (см. Run); ошибки
// отдельных LLM-вызовов (не 429/4xx) здесь же учтены как Failed.
func processObject(ctx context.Context, pool *pgxpool.Pool, cl *Client, det Detector, id int64, desc, lang string, rep *Report) error {
	rep.Objects++

	// 1. Устаревшие переводы удаляем: описание изменилось, и перевод
	// прежнего текста показывать как текущий — подстановка (ТЗ §11).
	if _, err := pool.Exec(ctx, `
		DELETE FROM object_translations WHERE object_id = $1 AND source_hash <> $2`,
		id, HashDescription(desc)); err != nil {
		return fmt.Errorf("translate: чистка устаревших переводов (объект %d): %w", id, err)
	}

	// 2. Язык оригинала — детектором по тексту, а не по стране (ТЗ §11).
	// Обновляем только при чётком сигнале (conf >= порога) и отличии от
	// текущего значения; при неопределённости не гадать — константа
	// коннектора остаётся.
	detected, conf := det.Detect(desc)
	if detected != "" && conf >= detectConfThreshold && detected != lang {
		if _, err := pool.Exec(ctx, `
			UPDATE objects SET language_original = $2 WHERE id = $1`, id, detected); err != nil {
			return fmt.Errorf("translate: обновление language_original (объект %d): %w", id, err)
		}
		lang = detected
		rep.LanguageUpdated++
	}

	// 3. Существующие свежие строки (после п.1 это только текущий текст).
	have := map[string]bool{}
	exRows, err := pool.Query(ctx, `
		SELECT lang FROM object_translations WHERE object_id = $1`, id)
	if err != nil {
		return fmt.Errorf("translate: чтение переводов (объект %d): %w", id, err)
	}
	for exRows.Next() {
		var l string
		if err := exRows.Scan(&l); err != nil {
			exRows.Close()
			return fmt.Errorf("translate: чтение переводов (объект %d): %w", id, err)
		}
		have[l] = true
	}
	exRows.Close()

	// 4. Целевые языки: свежий — пропуск, тот же текст на другом объекте —
	// переиспользование без LLM, иначе — перевод.
	hash := HashDescription(desc)
	for _, target := range Targets(lang) {
		if have[target] {
			rep.Skipped++
			continue
		}
		var (
			text   string
			model  string
			tokens *int
		)
		err := pool.QueryRow(ctx, `
			SELECT text, model, token_cost FROM object_translations
			WHERE source_hash = $1 AND lang = $2 AND object_id <> $3
			LIMIT 1`, hash, target, id).Scan(&text, &model, &tokens)
		switch {
		case err == nil:
			// ТЗ §11: повторный перевод того же текста не выполняется.
			if err := insertTranslation(ctx, pool, id, target, hash, text, model, tokens); err != nil {
				return err
			}
			rep.Reused++
		case errors.Is(err, pgx.ErrNoRows):
			res, err := cl.Translate(ctx, desc, target)
			if err != nil {
				var (
					perm *ErrPermanent
					rl   *ErrRateLimit
				)
				if errors.As(err, &perm) || errors.As(err, &rl) ||
					errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				rep.Failed++
				log.Printf("translate: объект %d → %s не переведён: %v (повторит следующий cron)", id, target, err)
				continue
			}
			if err := insertTranslation(ctx, pool, id, target, hash, res.Text, res.Model, tokensOrNull(res.Tokens)); err != nil {
				return err
			}
			rep.Translated++
			rep.Tokens += res.Tokens
		default:
			return fmt.Errorf("translate: поиск переиспользуемого перевода (объект %d, %s): %w", id, target, err)
		}
	}
	return nil
}

// insertTranslation — вставка/обновление перевода (ON CONFLICT — на
// случай двух параллельных прогонов). translated_at — время записи,
// модель и стоимость — из ответа API.
func insertTranslation(ctx context.Context, pool *pgxpool.Pool, id int64, lang, hash, text, model string, tokens *int) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO object_translations (object_id, lang, source_hash, text, model, token_cost, translated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (object_id, lang) DO UPDATE
		SET source_hash = EXCLUDED.source_hash,
		    text        = EXCLUDED.text,
		    model       = EXCLUDED.model,
		    token_cost  = EXCLUDED.token_cost,
		    translated_at = EXCLUDED.translated_at`,
		id, lang, hash, text, model, tokens)
	if err != nil {
		return fmt.Errorf("translate: сохранение перевода (объект %d, %s): %w", id, lang, err)
	}
	return nil
}

// tokensOrNull — 0 (в ответе API нет usage) — NULL: стоимость неизвестна,
// а не нулевая (ТЗ §11: token_cost хранится у каждого перевода).
func tokensOrNull(t int) *int {
	if t == 0 {
		return nil
	}
	x := t
	return &x
}

// Status — сводка для `pb translate status`. maxChars — из конфига:
// описания длиннее него переводчиком не берутся и показываются
// отдельно (оператор видит, что это не зависло, а по лимиту).
func Status(ctx context.Context, pool *pgxpool.Pool, maxChars int) (*StatusReport, error) {
	rep := &StatusReport{RowsByLang: map[string]int{}}

	var withDesc, tooLong int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE description_original IS NOT NULL
			AND btrim(description_original) <> ''),
		       count(*) FILTER (WHERE description_original IS NOT NULL
			AND btrim(description_original) <> ''
			AND length(description_original) > $1)
		FROM objects`, maxChars).Scan(&withDesc, &tooLong); err != nil {
		return nil, fmt.Errorf("translate: статус по objects: %w", err)
	}
	rep.ObjectsWithDesc = withDesc
	rep.TooLong = tooLong

	if err := pool.QueryRow(ctx, `
		SELECT count(*), coalesce(max(translated_at))
		FROM object_translations`).Scan(&rep.Rows, &rep.LastTranslated); err != nil {
		return nil, fmt.Errorf("translate: статус по object_translations: %w", err)
	}

	lrows, err := pool.Query(ctx, `SELECT lang, count(*) FROM object_translations GROUP BY lang ORDER BY lang`)
	if err != nil {
		return nil, fmt.Errorf("translate: статус по языкам: %w", err)
	}
	for lrows.Next() {
		var l string
		var n int
		if err := lrows.Scan(&l, &n); err != nil {
			lrows.Close()
			return nil, fmt.Errorf("translate: статус по языкам: %w", err)
		}
		rep.RowsByLang[l] = n
	}
	lrows.Close()

	mrows, err := pool.Query(ctx, `SELECT DISTINCT model FROM object_translations ORDER BY model`)
	if err != nil {
		return nil, fmt.Errorf("translate: статус по моделям: %w", err)
	}
	for mrows.Next() {
		var m string
		if err := mrows.Scan(&m); err != nil {
			mrows.Close()
			return nil, fmt.Errorf("translate: статус по моделям: %w", err)
		}
		rep.Models = append(rep.Models, m)
	}
	mrows.Close()

	if err := pool.QueryRow(ctx, `
		SELECT count(DISTINCT o.id)
		FROM objects o
		JOIN object_translations t ON t.object_id = o.id
		WHERE t.source_hash = encode(digest(o.description_original, 'sha256'), 'hex')`).
		Scan(&rep.Translated); err != nil {
		return nil, fmt.Errorf("translate: статус переведённых объектов: %w", err)
	}

	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM objects o
		WHERE o.description_original IS NOT NULL
		  AND btrim(o.description_original) <> ''
		  AND length(o.description_original) <= $1
		  AND (SELECT count(*) FROM object_translations t
		       WHERE t.object_id = o.id
		         AND t.lang IN ('ru', 'en')
		         AND t.source_hash = encode(digest(o.description_original, 'sha256'), 'hex')
		   ) < CASE WHEN o.language_original IN ('ru', 'en') THEN 1 ELSE 2 END`, maxChars).
		Scan(&rep.Pending); err != nil {
		return nil, fmt.Errorf("translate: статус pending: %w", err)
	}

	return rep, nil
}
