// pb — бинарь PropertyBoss: миграции, курсы, API-сервер, сканер, зоны,
// оценка, ликвидность, delisted, расписание, уведомления, переводы.
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

	// Путь к конфигу: --config | $PB_CONFIG | config/config.yaml.
	// Глобальный флаг — ДО субкоманды: Go flag не парсит флаги после
	// первого позиционного аргумента, поэтому `pb <sub> --config` молча
	// игнорировало путь (исправлено на этапе 10 — раньше работал только
	// $PB_CONFIG).
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

	// Субкоманда — первый позиционный аргумент; после неё — только флаги
	// этой субкоманды.
	sub := flag.Arg(0)
	subArgs := flag.Args()[1:]

	switch sub {
	case "migrate":
		if err := runMigrate(ctx, cfg); err != nil {
			log.Fatal(err)
		}
	case "fx":
		if len(subArgs) < 1 || subArgs[0] != "sync" {
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
		if err := runScan(ctx, cfg, subArgs); err != nil {
			log.Fatal(err)
		}
	case "zones":
		if err := runZones(ctx, cfg, subArgs); err != nil {
			log.Fatal(err)
		}
	case "valuate":
		if err := runValuate(ctx, cfg, subArgs); err != nil {
			log.Fatal(err)
		}
	case "liquidity":
		if err := runLiquidity(ctx, cfg, subArgs); err != nil {
			log.Fatal(err)
		}
	case "delist":
		if err := runDelist(ctx, cfg, subArgs); err != nil {
			log.Fatal(err)
		}
	case "schedule":
		if err := runSchedule(ctx, cfg, subArgs); err != nil {
			log.Fatal(err)
		}
	case "notify":
		if err := runNotify(ctx, cfg, subArgs); err != nil {
			log.Fatal(err)
		}
	case "translate":
		if err := runTranslate(ctx, cfg, subArgs); err != nil {
			log.Fatal(err)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `pb — PropertyBoss backend

Использование:
  pb [--config PATH] <субкоманда> [флаги]

  migrate               применить миграции
  fx sync               загрузить курсы ЕЦБ в fx_rates
  serve                 API-сервер (этап 2)
  scan -source ID -search-config ID   прогон сканера (этап 3)
  scan -list            зарегистрированные коннекторы
  zones import -file PATH -country XX -source "NAME"   полигоны зон (этап 4)
  zones quotazioni -file PATH [-country IT]   котировки зон (этап 4)
  zones assign          привязка объектов к зонам (этап 4)
  zones link -country XX -level L   parent_id по геометрии (этап 4)
  zones list [-country XX] [-level L]   просмотр зон (этап 4)
  valuate [-country XX] [-deal-type T]   гедоническая оценка (этап 5, ТЗ §7.2–7.3)
  liquidity [-country XX] [-deal-type T]   модель ликвидности (этап 7, ТЗ §9)
  delist [-source ID]    прогон маркировки delisted (этап 6, ТЗ §8.2)
  schedule show         состояние расписания: веса, warming_up, бюджет (этап 11, ТЗ §10)
  schedule run [-dry]   следующий скан по расписанию, cron-точка (этап 11, ТЗ §10)
  schedule init-windows -source ID [-timezone TZ] [-dow 0-6] [-hours 0-24] [-max N]   окна сканирования (этап 11, ТЗ §10)
  notify send [--limit N]   доставка pending-очереди в Telegram, cron-точка (этап 8, ТЗ §2, §3.4)
  notify test               тестовое сообщение: токен/чат/сеть (этап 8)
  notify object <id>        снимок объекта: оценка + вероятность ухода (этап 8)
  notify check-disk         свободное место, алерт при пороге (этап 8, ТЗ §3.2)
  notify status [--limit N] состояние очереди уведомлений (этап 8)
  translate run [--limit N] [--country XX]   cron-точка: переводы ru/en описаний (этап 10, ТЗ §11)
  translate status          состояние конвейера переводов (этап 10)

Конфиг: --config PATH (глобальный флаг, ДО субкоманды) | $PB_CONFIG | config/config.yaml`)
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
