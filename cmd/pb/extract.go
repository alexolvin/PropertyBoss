// pb extract — LLM-извлечение атрибутов из описаний объявлений
// (ТЗ v4 §4.3, шаг 3 порядка получения значений §4.2).
//
//	subcommands:
//
//	run [--limit N] [--country XX]   cron-точка: извлечь только атрибуты,
//		не полученные на шагах 1–2 (source_field/rule); ответ модели
//		программно проверяется по реестру, провалившее отбрасывается
//	status [--country XX]           состояние: вызовы, расход токенов,
//		бюджет, кандидаты в очереди
//
// Ключ не задан — явная ошибка, а не заглушка (ТЗ §0.4, §12.6):
// значения шагов 1–2 при этом не затрагиваются.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"propertyboss/internal/config"
	"propertyboss/internal/db"
	"propertyboss/internal/extract"
)

func runExtract(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("pb extract: нужен subcommand (run|status)")
	}
	switch args[0] {
	case "run":
		return runExtractRun(ctx, cfg, args[1:])
	case "status":
		return runExtractStatus(ctx, cfg, args[1:])
	default:
		return fmt.Errorf("pb extract: неизвестный subcommand %q", args[0])
	}
}

// extractConfig — зеркало config.Extract для пакета; пустой ключ —
// честная ошибка с инструкцией (ТЗ §0.4), а не молчаливый no-op.
func extractConfig(cfg *config.Config) (*extract.Config, error) {
	if cfg.Extract.APIKey == "" {
		return nil, fmt.Errorf("LLM-извлечение не настроено: extract.api_key в конфиге пуст. Требование ТЗ v4 §4.3 (шаг 3 получения значений) без ключа OpenAI-совместимого API не выполнимо. Значения шагов 1–2 (source_field/rule) не затрагиваются. Для включения: extract.api_key + extract.model в конфиге")
	}
	if cfg.Extract.Model == "" {
		return nil, fmt.Errorf("extract.model в конфиге пуст: извлечение не выполняется без имени модели")
	}
	return &extract.Config{
		BaseURL:         cfg.Extract.BaseURL,
		APIKey:          cfg.Extract.APIKey,
		Model:           cfg.Extract.Model,
		TimeoutSec:      cfg.Extract.TimeoutSec,
		MaxConfidence:   cfg.Extract.MaxConfidence,
		AttrListVersion: cfg.Extract.AttrListVersion,
		MaxChars:        cfg.Extract.MaxChars,
		BudgetTokens:    cfg.Extract.BudgetTokens,
		// Мининг (ТЗ v4 §4.3, гибридное извлечение): пороги из rules.*.
		MinedMinOccurrences: cfg.Rules.MinMinedOccurrences,
		MinedConfidenceMax:  cfg.Rules.MinedConfidenceMax,
		MinedExampleObjects: cfg.Rules.ExampleObjects,
		MinedAutoPromote:    cfg.Rules.AutoPromote,
		// Теневой контроль (C8): доля объектов, уходящих в LLM повторно.
		ShadowSamplePct: cfg.Rules.ShadowSamplePct,
	}, nil
}

func runExtractRun(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("extract run", flag.ContinueOnError)
	limit := fs.Int("limit", 0, "сколько объектов за прогон (дефолт: без лимита)")
	country := fs.String("country", "", "один рынок (CZ|IT|NL); пусто — все")
	objID := fs.Int64("id", 0, "один объект (id в objects)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ec, err := extractConfig(cfg)
	if err != nil {
		return err
	}
	if available, _, err := extract.CheckHealth(ctx, cfg.Extract.BaseURL); !available {
		log.Printf("extract run: LLM-сервер %s недоступен (%v) — пропуск запуска", cfg.Extract.BaseURL, err)
		return nil
	}
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	var rep *extract.Report
	if *objID > 0 {
		rep, err = extract.ProcessObject(ctx, pool, ec, *objID)
	} else {
		rep, err = extract.Run(ctx, pool, ec, *limit, *country)
	}
	if err != nil {
		return err
	}
	log.Printf("extract run: кандидатов %d, вызовов %d, пропущено (идемп.) %d, слишком длинных %d, без пропущенных атрибутов %d, ошибок вызова %d, не разобрано %d, значений записано %d, отброшено %d, токенов %d",
		rep.Candidates, rep.Extracted, rep.Skipped, rep.TooLong, rep.NoMissing,
		rep.CallErrors, rep.ParseFailed, rep.Valid, rep.Dropped, rep.Tokens)
	log.Printf("extract: мининга %d, без цитаты %d, промоутед шаблонов %d, готово к промоушну (ждёт ревизии) %d, зеркал заполнено %d",
		rep.Mined, rep.NoQuote, rep.Promoted, rep.Ready, rep.MirrorFilled)
	if rep.ShadowDone+rep.ShadowMismatch+rep.ShadowPending+rep.ShadowErrors > 0 {
		log.Printf("extract: теневой контроль: совпало %d, расхождений %d, pending %d, ошибок %d",
			rep.ShadowDone, rep.ShadowMismatch, rep.ShadowPending, rep.ShadowErrors)
	}
	if rep.BudgetHit {
		log.Printf("extract: бюджет %d токенов исчерпан — необработанные задачи остаются в очереди", cfg.Extract.BudgetTokens)
	}
	return nil
}

func runExtractStatus(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("extract status", flag.ContinueOnError)
	country := fs.String("country", "", "один рынок (CZ|IT|NL); пусто — все")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	var calls int
	var tokens int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(SUM(tokens), 0) FROM llm_extractions`).Scan(&calls, &tokens); err != nil {
		return fmt.Errorf("extract status: %w", err)
	}
	rows, err := pool.Query(ctx, `
		SELECT model, count(*), COALESCE(SUM(tokens), 0)
		FROM llm_extractions GROUP BY model ORDER BY 3 DESC`)
	if err != nil {
		return fmt.Errorf("extract status: %w", err)
	}
	msg := fmt.Sprintf("extract status: вызовов %d, токенов %d", calls, tokens)
	if cfg.Extract.BudgetTokens > 0 {
		rest := cfg.Extract.BudgetTokens - int(tokens)
		if rest < 0 {
			rest = 0
		}
		msg += fmt.Sprintf(", бюджет %d (остаток %d)", cfg.Extract.BudgetTokens, rest)
	}
	log.Print(msg)
	for rows.Next() {
		var model string
		var n int
		var t int64
		if err := rows.Scan(&model, &n, &t); err != nil {
			rows.Close()
			return fmt.Errorf("extract status: %w", err)
		}
		log.Printf("  модель %s: вызовов %d, токенов %d", model, n, t)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("extract status: %w", err)
	}
	rows.Close()

	cands, err := extract.CountCandidates(ctx, pool, *country)
	if err != nil {
		return fmt.Errorf("extract status: %w", err)
	}
	log.Printf("extract status: кандидатов в очереди %d", cands)
	if cfg.Extract.APIKey == "" {
		log.Printf("extract status: LLM-извлечение НЕ настроено (extract.api_key пуст) — run не будет выполнять извлечение (ТЗ v4 §4.3)")
	}
	return nil
}
