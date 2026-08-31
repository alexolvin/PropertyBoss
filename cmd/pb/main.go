// pb — бинарь PropertyBoss: миграции, курсы, API-сервер, сканер, зоны.
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

	// Коннекторы регистрируют себя в init() (этап 3).
	_ "propertyboss/internal/connectors/bazos"
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
	case "scan":
		if err := runScan(ctx, cfg, os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "zones":
		if err := runZones(ctx, cfg, os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "valuate":
		if err := runValuate(ctx, cfg, os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "liquidity":
		if err := runLiquidity(ctx, cfg, os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "delist":
		if err := runDelist(ctx, cfg, os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "schedule":
		if err := runSchedule(ctx, cfg, os.Args[2:]); err != nil {
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
  pb scan -source ID -search-config ID [--config PATH]  прогон сканера (этап 3)
  pb scan -list  [--config PATH]  зарегистрированные коннекторы
  pb zones import -file PATH -country XX -source "NAME" [--config PATH]  полигоны зон (этап 4)
  pb zones quotazioni -file PATH [-country IT] [--config PATH]  котировки зон (этап 4)
  pb zones assign [--config PATH]  привязка объектов к зонам (этап 4)
  pb zones link -country XX -level L [--config PATH]  parent_id по геометрии (этап 4)
  pb zones list [-country XX] [-level L] [--config PATH]  просмотр зон (этап 4)
  pb valuate [-country XX] [-deal-type T] [--config PATH]  гедоническая оценка (этап 5, ТЗ §7.2–7.3)
  pb liquidity [-country XX] [-deal-type T] [--config PATH]  модель ликвидности (этап 7, ТЗ §9)
  pb delist [-source ID] [--config PATH]  прогон маркировки delisted (этап 6, ТЗ §8.2)
  pb schedule show [--config PATH]  состояние расписания: веса, warming_up, бюджет (этап 11, ТЗ §10)
  pb schedule run [-dry] [--config PATH]  следующий скан по расписанию, cron-точка (этап 11, ТЗ §10)
  pb schedule init-windows -source ID [-timezone TZ] [-dow 0-6] [-hours 0-24] [-max N]  окна сканирования (этап 11, ТЗ §10)

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
