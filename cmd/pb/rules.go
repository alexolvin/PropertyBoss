// pb rules — управление шаблонным слоем (ТЗ v4 §4.2, гибридное извлечение).
//
// Арсенал программных шаблонов (attribute_templates): быстрые миллисекундные
// прогоны по корпусу без LLM, растёт без пересборки (данные в БД). Мined-
// шаблоны — из LLM-успехов с дословным доказательством (pb extract run).
//
//	subcommands:
//
//	run [--country XX] [--limit N]   оффлайн-прогон по существующим объектам
//		(текст из БД, без сети): заполнить пропущенные не-зеркальные
//		атрибуты активными шаблонами
//	status [--country XX]           покрытие по атрибутам (по extracted_by) +
//		счётчики шаблонов по status + top-candidates
//	list  [--status s] [--country XX] [--attr key]
//	                              шаблоны с примерами и доказательством
//	set <id> --status <s>           ручное управление статусом шаблона
//		(s: active|candidate|conflict|disabled|demoted)
//
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/attrules"
	"propertyboss/internal/config"
	"propertyboss/internal/db"
)

func runRules(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("pb rules: нужен subcommand (run|status|list|set)")
	}
	switch args[0] {
	case "run":
		return runRulesRun(ctx, cfg, args[1:])
	case "status":
		return runRulesStatus(ctx, cfg, args[1:])
	case "list":
		return runRulesList(ctx, cfg, args[1:])
	case "set":
		return runRulesSet(ctx, cfg, args[1:])
	default:
		return fmt.Errorf("pb rules: неизвестный subcommand %q", args[0])
	}
}

func rulesPool(ctx context.Context, cfg *config.Config) (*pgxpool.Pool, error) {
	return db.Open(ctx, cfg.Database.DSN)
}

// --- run -----------------------------------------------------------------

func runRulesRun(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("rules run", flag.ContinueOnError)
	limit := fs.Int("limit", 0, "сколько объектов за прогон (0 — все)")
	country := fs.String("country", "", "один рынок (CZ|IT|NL); пусто — все")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pool, err := rulesPool(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	rep, err := attrules.Run(ctx, pool, *country, *limit, cfg.Rules.ShadowSamplePct)
	if err != nil {
		return err
	}
	log.Printf("rules run: сканировано %d, записано объектов %d, значений %d, зеркал area_sqm %d, зеркал seller_type %d, теневой очереди %d",
		rep.Scanned, rep.Written, rep.Values, rep.AreaMirror, rep.SellerMirror, rep.ShadowEnqueued)
	return nil
}

// --- status --------------------------------------------------------------

func runRulesStatus(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("rules status", flag.ContinueOnError)
	country := fs.String("country", "", "один рынок (CZ|IT|NL); пусто — все")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pool, err := rulesPool(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	// 1) Покрытие по атрибутам: активных объектов страны, у которых значение
	// есть, разбитое по extracted_by (шаги цепочки значений §4.2).
	rows, err := pool.Query(ctx, `
SELECT ar.country, ar.key,
       count(DISTINCT o.id) AS active,
       count(DISTINCT CASE WHEN av.object_id IS NOT NULL THEN av.object_id END) AS covered,
       count(DISTINCT CASE WHEN av.extracted_by = 'source_field'   THEN av.object_id END) AS by_source,
       count(DISTINCT CASE WHEN av.extracted_by = 'rule_markup'    THEN av.object_id END) AS by_markup,
       count(DISTINCT CASE WHEN av.extracted_by = 'rule_text'      THEN av.object_id END) AS by_text,
       count(DISTINCT CASE WHEN av.extracted_by = 'llm_extraction' THEN av.object_id END) AS by_llm
FROM attribute_registry ar
JOIN objects o ON o.country = ar.country AND o.status = 'active'
LEFT JOIN attribute_values av ON av.object_id = o.id AND av.attr_key = ar.key
WHERE $1::text = '' OR ar.country = $1
GROUP BY ar.country, ar.key
ORDER BY ar.country, ar.key`, *country)
	if err != nil {
		return fmt.Errorf("rules status: покрытие: %w", err)
	}
	log.Printf("rules status: покрытие по атрибутам (активные объекты; по extracted_by):")
	for rows.Next() {
		var (
			ct, key     string
			active      int
			covered     int
			bySource    int
			byMarkup    int
			byText      int
			byLLM       int
		)
		if err := rows.Scan(&ct, &key, &active, &covered, &bySource, &byMarkup, &byText, &byLLM); err != nil {
			rows.Close()
			return fmt.Errorf("rules status: покрытие: %w", err)
		}
		missing := active - covered
		log.Printf("  %s %s: покрыто %d/%d (source %d, rule_markup %d, rule_text %d, llm %d; отсутствует %d)",
			ct, key, covered, active, bySource, byMarkup, byText, byLLM, missing)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("rules status: покрытие: %w", err)
	}
	rows.Close()

	// 2) Счётчики шаблонов по status.
	st, err := pool.Query(ctx, `
SELECT status, count(*) FROM attribute_templates
WHERE $1::text = '' OR country = $1
GROUP BY status ORDER BY status`, *country)
	if err != nil {
		return fmt.Errorf("rules status: шаблоны: %w", err)
	}
	var sb strings.Builder
	for st.Next() {
		var (
			s   string
			n   int
		)
		if err := st.Scan(&s, &n); err != nil {
			st.Close()
			return fmt.Errorf("rules status: шаблоны: %w", err)
		}
		if sb.Len() > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "%s=%d", s, n)
	}
	st.Close()
	if err := st.Err(); err != nil {
		return fmt.Errorf("rules status: шаблоны: %w", err)
	}
	if sb.Len() == 0 {
		sb.WriteString("(шаблонов нет)")
	}
	log.Printf("rules status: шаблоны: %s", sb.String())

	// 3) Top-candidates: кандидаты с наибольшим числом повторений (кандидаты
	// в ручную ревизию / авто-промоушн).
	tc, err := pool.Query(ctx, `
SELECT id, attr_key, pattern, value, occurrences, conflicts
FROM attribute_templates
WHERE status = 'candidate' AND ($1::text = '' OR country = $1)
ORDER BY occurrences DESC, id
LIMIT 20`, *country)
	if err != nil {
		return fmt.Errorf("rules status: кандидаты: %w", err)
	}
	log.Printf("rules status: top-candidates (к ручному промоушну / авто):")
	for tc.Next() {
		var (
			id      int64
			key     string
			pattern string
			value   []byte
			occ     int
			conf    int
		)
		if err := tc.Scan(&id, &key, &pattern, &value, &occ, &conf); err != nil {
			tc.Close()
			return fmt.Errorf("rules status: кандидаты: %w", err)
		}
		log.Printf("  [#%d] %s %q = %s (повторений %d, конфликтов %d)",
			id, key, pattern, string(value), occ, conf)
	}
	if err := tc.Err(); err != nil {
		tc.Close()
		return fmt.Errorf("rules status: кандидаты: %w", err)
	}
	tc.Close()
	return nil
}

// --- list ----------------------------------------------------------------

func runRulesList(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("rules list", flag.ContinueOnError)
	status := fs.String("status", "", "фильтр по status (active|candidate|conflict|disabled); пусто — все")
	country := fs.String("country", "", "один рынок; пусто — все")
	attr := fs.String("attr", "", "фильтр по attr_key; пусто — все")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pool, err := rulesPool(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	rows, err := pool.Query(ctx, `
SELECT id, country, attr_key, pattern_type, pattern, value, status,
       confidence, source, evidence, occurrences, conflicts, example_object_ids,
       occurrence_ids, shadow_mismatches, demoted_at
FROM attribute_templates
WHERE ($1::text = '' OR status = $1)
  AND ($2::text = '' OR country = $2)
  AND ($3::text = '' OR attr_key = $3)
ORDER BY country, attr_key, id`, *status, *country, *attr)
	if err != nil {
		return fmt.Errorf("rules list: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var (
			id64     int64
			ct2, key2, pt2 string
			pat        string
			value      []byte
			st2        string
			conf       float64
			src        string
			evidence   *string
			occ, con   int
			examples   []int64
			occIDs     []int64
			shadowMism int
			demotedAt  *time.Time
		)
		if err := rows.Scan(&id64, &ct2, &key2, &pt2, &pat, &value, &st2,
			&conf, &src, &evidence, &occ, &con, &examples,
			&occIDs, &shadowMism, &demotedAt); err != nil {
			return fmt.Errorf("rules list: %w", err)
		}
		n++
		log.Printf("[%d] %s %s %s status=%s occ=%d conflicts=%d",
			id64, ct2, key2, pt2, st2, occ, con)
		log.Printf("    pattern: %s", pat)
		log.Printf("    value:   %s", string(value))
		if evidence != nil && *evidence != "" {
			log.Printf("    quote:   %s", *evidence)
		}
		log.Printf("    source=%s conf=%.3f examples=%v", src, conf, examples)
		// C8: подтверждения (полный список, хвост — холдаут) и теневой контроль.
		tail := occIDs
		if len(tail) > 5 {
			tail = tail[len(tail)-5:]
		}
		log.Printf("    подтверждений=%d (хвост: %v), теневых расхождений=%d, демоушен=%s",
			len(occIDs), tail, shadowMism, demotedAtStr(demotedAt))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rules list: %w", err)
	}
	if n == 0 {
		log.Printf("rules list: шаблонов не найдено (фильтры: status=%q country=%q attr=%q)", *status, *country, *attr)
	}
	return nil
}

// demotedAtStr — время авто-демоушна (C8) или «—».
func demotedAtStr(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return t.Format("2006-01-02 15:04")
}

// --- set -----------------------------------------------------------------

func runRulesSet(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("pb rules set: нужен id шаблона")
	}
	var id int64
	if _, err := fmt.Sscanf(args[0], "%d", &id); err != nil {
		return fmt.Errorf("pb rules set: id %q: %v", args[0], err)
	}
	fs := flag.NewFlagSet("rules set", flag.ContinueOnError)
	status := fs.String("status", "", "новый статус (active|candidate|conflict|disabled|demoted)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *status == "" {
		return fmt.Errorf("pb rules set: нужен --status (active|candidate|conflict|disabled|demoted)")
	}
	switch *status {
	case "active", "candidate", "conflict", "disabled", "demoted":
	default:
		return fmt.Errorf("pb rules set: недопустимый статус %q", *status)
	}
	pool, err := rulesPool(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	tag, err := pool.Exec(ctx, `
UPDATE attribute_templates
SET status = $2,
    promoted_at = CASE WHEN $2 = 'active'
                       THEN COALESCE(promoted_at, now())
                       ELSE NULL END
WHERE id = $1`, id, *status)
	if err != nil {
		return fmt.Errorf("pb rules set: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("pb rules set: шаблон %d не найден", id)
	}
	log.Printf("rules set: шаблон %d → status=%s", id, *status)
	return nil
}
