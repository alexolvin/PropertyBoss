// pb translate — асинхронный переводчик описаний (этап 10, ТЗ §11).
//
//	subcommands:
//
//	run [--limit N] [--country XX]  cron-точка: перевести недостающие/устаревшие ru/en
//	status                          состояние конвейера: pending, переводы, модели, лимит
//
// Переводы хранятся в object_translations (идемпотентно по
// sha256(description_original)); UI читает из БД, без обращения к LLM.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"propertyboss/internal/config"
	"propertyboss/internal/db"
	"propertyboss/internal/translate"
)

func runTranslate(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("pb translate: нужен subcommand (run|status)")
	}
	switch args[0] {
	case "run":
		return runTranslateRun(ctx, cfg, args[1:])
	case "status":
		return runTranslateStatus(ctx, cfg, args[1:])
	default:
		return fmt.Errorf("pb translate: неизвестный subcommand %q", args[0])
	}
}

// translateClient — клиент LLM; явная ошибка, если переводчик не
// настроен (ТЗ §0.4: не заглушка, а понятная инструкция).
func translateClient(cfg *config.Config) (*translate.Client, error) {
	if cfg.Translate.APIKey == "" {
		return nil, fmt.Errorf("переводчик не настроен: translate.api_key пуст в конфиге — заполните api_key и model (OpenAI-совместимый API), переводы останутся NULL, UI покажет оригинал с пометкой «перевод недоступен»")
	}
	timeout := time.Duration(cfg.Translate.TimeoutSec) * time.Second
	return &translate.Client{
		BaseURL: cfg.Translate.BaseURL,
		APIKey:  cfg.Translate.APIKey,
		Model:   cfg.Translate.Model,
		HTTP:    &http.Client{Timeout: timeout},
	}, nil
}

// runTranslateRun — cron-точка. Частичный прогон — нормальный исход:
// недопереведённое осталось кандидатом и уйдёт следующим прогоном.
func runTranslateRun(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("translate run", flag.ContinueOnError)
	limit := fs.Int("limit", 0, "сколько объектов за прогон (дефолт: без лимита — довести очередь до нуля)")
	country := fs.String("country", "", "один рынок (CZ|IT|NL); пусто — все")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cl, err := translateClient(cfg)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	rep, err := translate.Run(ctx, pool, cl, translate.NewDetector(), cfg.Translate.MaxChars, *limit, *country)
	if err != nil {
		return err
	}
	log.Printf("translate run: объектов %d, переведено %d, переиспользовано %d, пропущено (уже свеж.) %d, не переведено %d, language_original уточнено %d, токенов %d",
		rep.Objects, rep.Translated, rep.Reused, rep.Skipped, rep.Failed, rep.LanguageUpdated, rep.Tokens)
	return nil
}

// runTranslateStatus — сводка; работает и без api_key (читает только БД).
func runTranslateStatus(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("translate status", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	st, err := translate.Status(ctx, pool, cfg.Translate.MaxChars)
	if err != nil {
		return err
	}
	log.Printf("translate status: объектов с описанием %d, со свежим переводом %d, pending %d, слишком длинных (> %d симв.) %d",
		st.ObjectsWithDesc, st.Translated, st.Pending, cfg.Translate.MaxChars, st.TooLong)
	log.Printf("translate status: строк в object_translations %d: %v; моделей: %v",
		st.Rows, st.RowsByLang, st.Models)
	last := "-"
	if st.LastTranslated != nil {
		last = st.LastTranslated.UTC().Format("2006-01-02 15:04")
	}
	log.Printf("translate status: последний перевод %s", last)
	if cfg.Translate.APIKey == "" {
		log.Printf("translate status: переводчик НЕ настроен (translate.api_key пуст) — run не будет выполнять переводы")
	}
	return nil
}
