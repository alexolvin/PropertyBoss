// pb — бинарь PropertyBoss: миграции, загрузка курсов, (этап 2: API-сервер).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"propertyboss/internal/api"
	"propertyboss/internal/config"
	"propertyboss/internal/db"
	"propertyboss/internal/fx"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if len(os.Args) < 2 {
		usage()
	}

	// Путь к конфигу: --config | $PB_CONFIG | config/config.yaml
	cfgPath := flag.String("config", "", "путь к YAML-конфигу")
	flag.Parse()
	if *cfgPath == "" {
		*cfgPath = os.Getenv("PB_CONFIG")
	}
	if *cfgPath == "" {
		*cfgPath = "config/config.yaml"
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch os.Args[1] {
	case "migrate":
		if err := runMigrate(ctx, cfg); err != nil {
			log.Fatal(err)
		}
	case "fx":
		if len(os.Args) < 3 || os.Args[2] != "sync" {
			usage()
		}
		if err := runFxSync(ctx, cfg); err != nil {
			log.Fatal(err)
		}
	case "serve":
		if err := runServe(ctx, cfg); err != nil {
			log.Fatal(err)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `pb — PropertyBoss backend

Использование:
  pb migrate [--config PATH]   применить миграции
  pb fx sync   [--config PATH] загрузить курсы ЕЦБ в fx_rates
  pb serve     [--config PATH] API-сервер (этап 2)

Конфиг: --config | $PB_CONFIG | config/config.yaml`)
	os.Exit(2)
}

func runMigrate(ctx context.Context, cfg *config.Config) error {
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	applied, err := db.Migrate(ctx, pool, "migrations")
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		log.Printf("migrate: новых миграций нет (каталог %s)", "migrations")
		return nil
	}
	for _, name := range applied {
		log.Printf("migrate: применено %s", name)
	}
	return nil
}

func runServe(ctx context.Context, cfg *config.Config) error {
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	return api.New(pool, cfg).Serve(ctx, cfg.Dashboard.Listen)
}

func runFxSync(ctx context.Context, cfg *config.Config) error {
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	client := &fx.ECBClient{
		BaseURL:   cfg.FX.BaseURL,
		UserAgent: cfg.FX.UserAgent,
		HTTP:      &http.Client{Timeout: 60 * time.Second},
	}
	var mirror *fx.FrankfurterClient
	if cfg.FX.FallbackBaseURL != "" {
		mirror = &fx.FrankfurterClient{
			BaseURL: cfg.FX.FallbackBaseURL,
			HTTP:    &http.Client{Timeout: 60 * time.Second},
		}
	}
	rep, err := fx.Sync(ctx, client, mirror, pool, cfg.FX.BackfillDays)
	if err != nil {
		return err
	}
	// Ошибка одного канала — не молчим (ТЗ §0.4): данные неполны, и это видно.
	if rep.ECBErr != nil {
		log.Printf("fx sync: ВНИМАНИЕ: канал «XML ЕЦБ» недоступен: %v", rep.ECBErr)
	}
	if rep.MirrorErr != nil {
		log.Printf("fx sync: ВНИМАНИЕ: канал «frankfurter» недоступен: %v", rep.MirrorErr)
	}
	log.Printf("fx sync: записано строк %d, пропущено валют вне справочника %d (окно %d дней)",
		rep.Loaded, rep.SkippedUnknown, cfg.FX.BackfillDays)
	return nil
}
