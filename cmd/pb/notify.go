// pb notify — уведомления (этап 8, ТЗ §2, §3.2).
//
//	subcommands:
//
//	send [--limit N]    доставить pending-очередь (cron-точка, ТЗ §3.4)
//	test                тестовое сообщение (проверить токен и чат)
//	object <id>         снимок объекта: оценка + вероятность ухода
//	check-disk          свободное место (ТЗ §3.2), алерт при пороге
//	status [--limit N]  состояние очереди
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/config"
	"propertyboss/internal/db"
	"propertyboss/internal/notify"
)

// prodNS — production-namespace очереди (C19.3): прямой SQL-вставки
// триггеров и этот флош ходят в один namespace; тесты — в свои.
const prodNS = "prod"

func runNotify(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("pb notify: нужен subcommand (send|test|object|check-disk|status)")
	}
	switch args[0] {
	case "send":
		return runNotifySend(ctx, cfg, args[1:])
	case "test":
		return runNotifyTest(ctx, cfg)
	case "object":
		return runNotifyObject(ctx, cfg, args[1:])
	case "check-disk":
		return runNotifyCheckDisk(ctx, cfg)
	case "status":
		return runNotifyStatus(ctx, cfg, args[1:])
	default:
		return fmt.Errorf("pb notify: неизвестный subcommand %q", args[0])
	}
}

// tgClient — клиент Bot API; явная ошибка, если Telegram не настроен.
func tgClient(cfg *config.Config) (*notify.Client, error) {
	if !cfg.Telegram.Enabled || cfg.Telegram.Token == "" || cfg.Telegram.ChatID == "" {
		return nil, fmt.Errorf("Telegram не настроен (telegram.enabled/token/chat_id) — уведомления копятся в очереди; получите токен у @BotFather и заполните config.yaml")
	}
	return &notify.Client{
		BaseURL: cfg.Telegram.BaseURL,
		Token:   cfg.Telegram.Token,
		ChatID:  cfg.Telegram.ChatID,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// runNotifySend — доставка очереди. Частичная обработка — нормальный
// исход (cron-точка, ТЗ §3.4): недоставленные останутся pending и
// уйдут следующим прогоном.
func runNotifySend(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("notify send", flag.ContinueOnError)
	limit := fs.Int("limit", 0, "сколько сообщений за прогон (дефолт: notify.flush_limit из конфига)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *limit <= 0 {
		*limit = cfg.Notify.FlushLimit
	}

	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	pending, err := notify.PendingCount(ctx, pool, prodNS)
	if err != nil {
		return err
	}
	if pending == 0 {
		log.Printf("notify send: очередь пуста")
		return nil
	}

	cl, err := tgClient(cfg)
	if err != nil {
		return fmt.Errorf("notify send: в очереди %d уведомл. но %v", pending, err)
	}
	sent, failed, err := notify.Flush(ctx, pool, cl, *limit, prodNS)
	log.Printf("notify send: доставлено %d, не доставлено %d, осталось pending %d",
		sent, failed, pending-sent-failed)
	if err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("notify send: %d не доставлено — смотрите pb notify status", failed)
	}
	return nil
}

// Пороги глубины очереди (C19.2): порог — решение оператора
// («>0 дольше 5 минут»); окно повтора 60 минут — допущение
// исполнителя (анти-шум: состояние «флош сломан» не меняется за минуты,
// как и для disk_low).
const (
	depthLimitMinutes   = 5
	depthRealertMinutes = 60
)

// flushQueueAndAlertDepth (C19.2 + волна 09.08, решение 2) —
// регулярный флош prod-очереди и алерт на глубину; вызывается из
// каждой cron-точки (schedule run), ДО раннего возврата «нечего
// сканировать» — флош обязан работать независимо от исхода скана
// (D17: весь прогон очередь не дренировалась).
//
//  1. Глубина: есть pending старше depthLimitMinutes (delivered=false
//     дольше 5 минут) — тревога вторым каналом, в обход очереди
//     (notify.CheckUndelivered): прямой отправкой, а при срыве —
//     КРИТИЧЕСКОЙ строкой в журнал. Старая схема (только очередь)
//     сама была недоставляема, когда доставка сломана.
//  2. Флош: pending → Telegram (limit notify.flush_limit). Telegram не
//     настроен — не ошибка скана: лог (production: telegram.enabled=false,
//     очередь копится и видна в pb notify status).
func flushQueueAndAlertDepth(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool) {
	var (
		pending int
		older   int
		oldest  *time.Time
	)
	err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE created_at < now() - interval '`+strconv.Itoa(depthLimitMinutes)+` minutes'),
		       count(*),
		       min(created_at)
		FROM notifications WHERE queue_ns = $1 AND status = 'pending'`, prodNS).
		Scan(&older, &pending, &oldest)
	if err != nil {
		log.Printf("schedule: глубина очереди: %v", err)
		return
	}
	if pending == 0 {
		return
	}

	cl, tgErr := tgClient(cfg)

	// 1) Тревога «delivered=false дольше 5 минут» вторым каналом
	//    (волна 09.08): прямой отправкой в обход очереди; при срыве —
	//    КРИТИЧЕСКАЯ строка в журнал. Окно повтора и история — в строке
	//    queue_depth (notify.CheckUndelivered); при успехе прямого
	//    отправления строка помечается sent — флош не дублирует.
	added := 0
	if older > 0 {
		ua, err := notify.CheckUndelivered(ctx, pool, prodNS, cl, depthLimitMinutes, depthRealertMinutes)
		if err != nil {
			log.Printf("schedule: тревога недоставленных: %v", err)
		} else if ua != nil {
			if ua.DirectlyDelivered {
				log.Printf("schedule: недоставлено дольше %d мин: pending %d (старших: %d, самое старое %d мин) — тревога доставлена прямо, в обход очереди (второй канал)",
					depthLimitMinutes, ua.PendingTotal, ua.OlderTotal, ua.OldestAgeMinutes)
			} else {
				added = 1
				log.Printf("schedule: КРИТИЧНО (доставка): pending %d, самое старое %d мин (> %d мин, delivered=false); прямой канал: %v — смотрите журнал и конфигурацию Telegram",
					ua.PendingTotal, ua.OldestAgeMinutes, depthLimitMinutes, ua.DirectErr)
			}
		}
	}

	// 2) Флош.
	if tgErr != nil {
		log.Printf("schedule: в очереди %d уведомлений, но %v", pending+added, tgErr)
		return
	}
	sent, failed, err := notify.Flush(ctx, pool, cl, cfg.Notify.FlushLimit, prodNS)
	if err != nil {
		log.Printf("schedule: флош очереди: %v", err)
		return
	}
	log.Printf("schedule: флош очереди: доставлено %d, не доставлено %d, осталось %d",
		sent, failed, pending+added-sent-failed)
}

// runNotifyTest — проверка канала: токен, чат, сеть.
func runNotifyTest(ctx context.Context, cfg *config.Config) error {
	cl, err := tgClient(cfg)
	if err != nil {
		return err
	}
	text, err := notify.Render("test", []byte("{}"))
	if err != nil {
		return err
	}
	if err := cl.Send(ctx, text); err != nil {
		return fmt.Errorf("notify test: %v", err)
	}
	log.Printf("notify test: тестовое сообщение доставлено")
	return nil
}

// runNotifyObject — снимок объекта. Текст ВСЕГДА печатается в консоль
// (работает и без токена); в Telegram шлётся только если настроен —
// прямой отправкой, не через очередь (это запрос оператора, а не
// триггер события).
func runNotifyObject(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("pb notify object <id>")
	}
	var id int64
	if _, err := fmt.Sscanf(args[0], "%d", &id); err != nil || id < 1 {
		return fmt.Errorf("pb notify object: id — целое число, получено %q", args[0])
	}
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	snap, err := notify.ObjectSnapshotFor(ctx, pool, id)
	if err != nil {
		return fmt.Errorf("notify object: %v", err)
	}
	raw, err := json.Marshal(snap.Payload())
	if err != nil {
		return err
	}
	text, err := notify.Render("object_snapshot", raw)
	if err != nil {
		return err
	}
	fmt.Println(text)

	cl, tErr := tgClient(cfg)
	if tErr != nil {
		log.Printf("notify object: в Telegram не отправляю (%v)", tErr)
		return nil
	}
	if err := cl.Send(ctx, text); err != nil {
		return fmt.Errorf("notify object: консоль — выше, Telegram: %v", err)
	}
	log.Printf("notify object: отправлено в Telegram")
	return nil
}

// runNotifyCheckDisk — ТЗ §3.2: алерт ДО критического порога.
// Замер всегда печатается; в очередь — только при пороге И вне окна
// повтора.
func runNotifyCheckDisk(ctx context.Context, cfg *config.Config) error {
	path := cfg.Notify.DiskPath
	if path == "" {
		return fmt.Errorf("check-disk отключён: notify.disk_path не задан в конфиге")
	}
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	info, queued, err := notify.CheckDisk(ctx, pool, path, cfg.Notify.DiskCriticalPct, cfg.Notify.DiskRealertMinutes, prodNS)
	if err != nil {
		return err
	}
	log.Printf("check-disk: %s — свободно %.1f%% (%.1f из %.1f GiB), порог %.0f%%",
		info.Path, info.FreePct, info.FreeGiB, info.TotalGiB, cfg.Notify.DiskCriticalPct)
	switch {
	case queued:
		log.Printf("check-disk: порог превышен — алерт поставлен в очередь (уйдёт при pb notify send)")
	case info.FreePct < cfg.Notify.DiskCriticalPct:
		log.Printf("check-disk: порог превышен, но внутри окна повтора (%d мин) — не повторяю", cfg.Notify.DiskRealertMinutes)
	default:
		log.Printf("check-disk: порог не достигнут")
	}
	return nil
}

// runNotifyStatus — сводка по очереди.
func runNotifyStatus(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("notify status", flag.ContinueOnError)
	limit := fs.Int("limit", 10, "сколько последних строк показать")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	st, err := notify.QueueStatus(ctx, pool, *limit, prodNS)
	if err != nil {
		return err
	}
	log.Printf("notify status: pending=%d sent=%d failed=%d", st.Pending, st.Sent, st.Failed)
	for _, r := range st.Recent {
		sentAt := ""
		if r.SentAt != nil {
			sentAt = r.SentAt.UTC().Format("2006-01-02 15:04")
		}
		errStr := ""
		if r.Error != nil {
			errStr = "  " + *r.Error
		}
		log.Printf("  #%d %s %s  создано %s  отправлено %s%s",
			r.ID, r.Kind, r.Status,
			r.CreatedAt.UTC().Format("2006-01-02 15:04"), orDash(sentAt), errStr)
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
