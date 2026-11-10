// pb — бинарь PropertyBoss: миграции, курсы, API-сервер, сканер, зоны,
// оценка, ликвидность, delisted, расписание, уведомления, переводы,
// детальный проход (enrich).
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
	"propertyboss/internal/ratelimit"

	// Коннекторы регистрируют себя в init() (этап 3).
	_ "propertyboss/internal/connectors/bazos"
	_ "propertyboss/internal/connectors/bezrealitky"
	_ "propertyboss/internal/connectors/ulovdomov"
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

	// D20: общий ограничитель частоты запросов к источнику — одно место
	// для всех исходящих путей (сбор, обогащение, контроль, URL-чеки
	// делистинга, верификация).
	ratelimit.SetRequestInterval(time.Duration(cfg.Source.RequestIntervalMS) * time.Millisecond)

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
	case "enrich":
		if err := runEnrich(ctx, cfg, subArgs); err != nil {
			log.Fatal(err)
		}
	case "photos":
		if err := runPhotos(ctx, cfg, subArgs); err != nil {
			log.Fatal(err)
		}
	case "geocode":
		if err := runGeocode(ctx, cfg, subArgs); err != nil {
			log.Fatal(err)
		}
	case "extract":
		if err := runExtract(ctx, cfg, subArgs); err != nil {
			log.Fatal(err)
		}
	case "rules":
		if err := runRules(ctx, cfg, subArgs); err != nil {
			log.Fatal(err)
		}
	case "doctor":
		// `pb doctor web` — только инвариант раздачи (TZ UI v5 U04):
		// без БД, read-only; нужен make ui, где БД не поднимается.
		if len(subArgs) > 0 && subArgs[0] == "web" {
			if err := runDoctorWeb(ctx, cfg); err != nil {
				log.Fatal(err)
			}
		} else {
			if err := runDoctor(ctx, cfg, subArgs); err != nil {
				log.Fatal(err)
			}
		}
	case "sources":
		if err := runSources(ctx, cfg, subArgs); err != nil {
			log.Fatal(err)
		}
	case "probe":
		if err := runProbe(ctx, cfg, subArgs); err != nil {
			log.Fatal(err)
		}
	case "verify-sample":
		if err := runVerifySample(ctx, cfg, subArgs); err != nil {
			log.Fatal(err)
		}
	case "backup":
		if err := runBackup(ctx, cfg, subArgs); err != nil {
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
  enrich run [--limit N] [--country XX] [--id N] [--source ID]  детальный проход: поля, продавец, координаты, фото (ТЗ v4 §4)
  geocode run [--limit N] [--country XX] [--dry]  бэкфилл улицы из описания → Nominatim (ТЗ v4 §4.5)
  photos run [--limit N]      локальные WebP-копии фотографий (ТЗ v4 §4.4)
  extract run [--limit N] [--country XX] [--id N]  LLM-извлечение атрибутов из описаний (ТЗ v4 §4.3)
  extract status [--country XX]           вызовы, токены, бюджет, очередь (ТЗ v4 §4.3)
  rules run [--limit N] [--country XX]    оффлайн-прогон шаблонного слоя по объектам (ТЗ v4 §4.2)
  rules status [--country XX]             покрытие по атрибутам + счётчики шаблонов + top-candidates
  rules list [--status s] [--country XX] [--attr key]   шаблоны с примерами
  rules set <id> --status <s>             ручной статус шаблона (active|candidate|conflict|disabled)
  doctor [-country CZ] [--live]           слой инвариантов над живой БД: зеркала, покрытие, сканер, очередь, модель, раздача (волна 09.08)
  doctor web                              только инвариант раздачи web (без БД, TZ UI v5 U04): make ui
  sources add-candidate <domain> --via seed|owner|link:<домен>   кандидат площадки (ТЗ-SRC-CZ-1)
  sources report                          реестр docs/sources/CZ_REGISTRY.md из БД
  sources harvest --url URL --via link:<домен>   D2: домены со страницы-перечня
  probe <domain> | probe --pending        проба площадки P1–P12 (ТЗ-SRC-CZ-1)
  verify-sample <source-id> [--n 20] [--seed N]   сверка выборки с живой площадкой (G2.5)
  backup run                            создать резервную копию всей БД (ТЗ-R1 R0.3)
  backup verify [--dump PATH]           тест восстановления в pb_restore_test (ТЗ-R1 R0.4)

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
