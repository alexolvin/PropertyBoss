package notify

// Live-тесты тревоги «delivered=false дольше 5 минут» (волна 09.08,
// решение 2): PB_TEST_DSN + live-лок; Telegram — mock-сервер.
// Факт доставки против НАСТОЯЩЕГО Bot API — TestLiveTelegramRealDelivery
// (честный skip, когда креденшелы в конфиге не заданы).

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"propertyboss/internal/config"
)

// TestLiveCheckUndeliveredDirect — прямой успех: тревога дошла в обход
// очереди (1 запрос к mock), строка queue_depth сразу 'sent' (флош не
// дублирует), старая строка остаётся pending (флош здесь не выполняется).
func TestLiveCheckUndeliveredDirect(t *testing.T) {
	pool := notifyPool(t)
	ctx := t.Context()
	const ns = "test-undelivered-direct"
	nsCleanup(t, pool, ns)

	id, err := Enqueue(ctx, pool, ns, "test", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE notifications SET created_at = now() - interval '10 minutes' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	srv, calls := mockTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		okResponse(w)
	})
	cl := notifyClientTest(t, srv)
	ua, err := CheckUndelivered(ctx, pool, ns, cl, 5, 60)
	if err != nil {
		t.Fatalf("CheckUndelivered: %v", err)
	}
	if ua == nil {
		t.Fatal("тревоги нет при строке старше 5 минут")
	}
	if !ua.DirectlyDelivered || ua.DirectErr != nil {
		t.Fatalf("прямая доставка: delivered=%v err=%v", ua.DirectlyDelivered, ua.DirectErr)
	}
	if ua.PendingTotal != 1 || ua.OlderTotal != 1 {
		t.Errorf("счёты: pending=%d older=%d, ждали 1/1", ua.PendingTotal, ua.OlderTotal)
	}
	if ua.OldestAgeMinutes < 5 {
		t.Errorf("возраст старшего = %d мин, ждали >= 5", ua.OldestAgeMinutes)
	}
	if len(*calls) != 1 {
		t.Fatalf("mock получил запросов %d, ждали 1 (прямая доставка)", len(*calls))
	}
	if !strings.Contains((*calls)[0].Text, "Глубина очереди") {
		t.Errorf("текст тревоги:\n%s", (*calls)[0].Text)
	}

	var stDepth string
	var sentAt *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT status, sent_at FROM notifications WHERE kind = 'queue_depth' AND queue_ns = $1`, ns).
		Scan(&stDepth, &sentAt); err != nil {
		t.Fatal(err)
	}
	if stDepth != "sent" || sentAt == nil {
		t.Errorf("queue_depth: status=%q sent_at=%v, ждали sent + метку времени", stDepth, sentAt)
	}
	var stOld string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM notifications WHERE id = $1`, id).Scan(&stOld); err != nil {
		t.Fatal(err)
	}
	if stOld != "pending" {
		t.Errorf("старая строка: status=%q, ждали pending (флош не выполнялся)", stOld)
	}

	// Окно повтора 60 минут: второй вызов — тишина.
	ua2, err := CheckUndelivered(ctx, pool, ns, cl, 5, 60)
	if err != nil {
		t.Fatalf("CheckUndelivered (2-й): %v", err)
	}
	if ua2 != nil {
		t.Error("повтор тревоги внутри окна 60 минут — ждали тишины")
	}
	var nDepth int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE kind = 'queue_depth' AND queue_ns = $1`, ns).Scan(&nDepth); err != nil {
		t.Fatal(err)
	}
	if nDepth != 1 {
		t.Errorf("queue_depth строк: %d, ждали 1", nDepth)
	}
}

// TestLiveCheckUndeliveredDirectFailed — срыв прямого (500): строка
// остаётся pending (после восстановления флош доставит её — тревога
// с опозданием, но дойдёт), DirectErr заполнен.
func TestLiveCheckUndeliveredDirectFailed(t *testing.T) {
	saved := retryWait
	retryWait = []time.Duration{0, 0}
	t.Cleanup(func() { retryWait = saved })

	pool := notifyPool(t)
	ctx := t.Context()
	const ns = "test-undelivered-failed"
	nsCleanup(t, pool, ns)

	id, err := Enqueue(ctx, pool, ns, "test", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE notifications SET created_at = now() - interval '10 minutes' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	srv, _ := mockTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":500,"description":"boom"}`))
	})
	cl := notifyClientTest(t, srv)
	ua, err := CheckUndelivered(ctx, pool, ns, cl, 5, 60)
	if err != nil {
		t.Fatalf("CheckUndelivered: %v", err)
	}
	if ua == nil {
		t.Fatal("тревоги нет при строке старше 5 минут")
	}
	if ua.DirectlyDelivered || ua.DirectErr == nil {
		t.Fatalf("прямой срыв: delivered=%v err=%v, ждали false + ошибку", ua.DirectlyDelivered, ua.DirectErr)
	}
	var stDepth string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM notifications WHERE kind = 'queue_depth' AND queue_ns = $1`, ns).Scan(&stDepth); err != nil {
		t.Fatal(err)
	}
	if stDepth != "pending" {
		t.Errorf("queue_depth: status=%q, ждали pending (доставится после восстановления)", stDepth)
	}
}

// TestLiveCheckUndeliveredNoClient — Telegram не настроен (cl=nil):
// строка ставится (окно + история), DirectErr=ErrDeliveryNotConfigured;
// КРИТИЧЕСКАЯ строка в журнал — дело вызывающего.
func TestLiveCheckUndeliveredNoClient(t *testing.T) {
	pool := notifyPool(t)
	ctx := t.Context()
	const ns = "test-undelivered-noclient"
	nsCleanup(t, pool, ns)

	id, err := Enqueue(ctx, pool, ns, "test", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE notifications SET created_at = now() - interval '10 minutes' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	ua, err := CheckUndelivered(ctx, pool, ns, nil, 5, 60)
	if err != nil {
		t.Fatalf("CheckUndelivered: %v", err)
	}
	if ua == nil {
		t.Fatal("тревоги нет при строке старше 5 минут")
	}
	if ua.DirectlyDelivered {
		t.Error("прямая доставка без клиента невозможна")
	}
	if !errors.Is(ua.DirectErr, ErrDeliveryNotConfigured) {
		t.Errorf("DirectErr = %v, ждали ErrDeliveryNotConfigured", ua.DirectErr)
	}
	var stDepth string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM notifications WHERE kind = 'queue_depth' AND queue_ns = $1`, ns).Scan(&stDepth); err != nil {
		t.Fatal(err)
	}
	if stDepth != "pending" {
		t.Errorf("queue_depth: status=%q, ждали pending (история)", stDepth)
	}
}

// TestLiveCheckUndeliveredQuiet — тишина: пустая очередь и очередь без
// старых строк (моложе 5 минут) — тревоги нет, строк queue_depth нет.
func TestLiveCheckUndeliveredQuiet(t *testing.T) {
	pool := notifyPool(t)
	ctx := t.Context()
	const ns = "test-undelivered-quiet"
	nsCleanup(t, pool, ns)

	srv, _ := mockTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		okResponse(w)
	})
	cl := notifyClientTest(t, srv)

	if ua, err := CheckUndelivered(ctx, pool, ns, cl, 5, 60); err != nil {
		t.Fatalf("CheckUndelivered (пусто): %v", err)
	} else if ua != nil {
		t.Error("тревога на пустой очереди — ждали nil")
	}

	if _, err := Enqueue(ctx, pool, ns, "test", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if ua, err := CheckUndelivered(ctx, pool, ns, cl, 5, 60); err != nil {
		t.Fatalf("CheckUndelivered (свежая): %v", err)
	} else if ua != nil {
		t.Error("тревога на свежей строке (< 5 мин) — ждали nil")
	}
	var nDepth int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE kind = 'queue_depth' AND queue_ns = $1`, ns).Scan(&nDepth); err != nil {
		t.Fatal(err)
	}
	if nDepth != 0 {
		t.Errorf("queue_depth строк: %d, ждали 0", nDepth)
	}
}

// TestLiveTelegramRealDelivery — факт доставки против НАСТОЯЩЕГО Bot API
// (волна 09.08, решение 2: «живой тест обязан утверждать факт доставки,
// а не постановку в очередь и не запись в журнал»). ok:true от Telegram —
// сообщение принято API и доставлено в чат (read-рецептов у Bot API нет —
// принятие — самый сильный доступный факт).
//
// Как и прочие live-тесты пакета, требует PB_TEST_DSN (прогоняется
// через tmp_livetests) — обычный unit-прогон не пингует настоящий
// Bot API. Креденшелы в конфиге не заданы (telegram.enabled=false /
// пустые token/chat_id) — тест честно пропускается. С волны 09.08
// реальные креденшелы в config.yaml: тест шлёт настоящее сообщение в
// чат оператора (chat_id «id/N» — в топик N), и ok:true от Bot API —
// факт доставки.
func TestLiveTelegramRealDelivery(t *testing.T) {
	if os.Getenv("PB_TEST_DSN") == "" {
		t.Skip("PB_TEST_DSN не задан — live-тест отключён (прогоняется через tmp_livetests)")
	}
	cfgPath := os.Getenv("PB_CONFIG")
	if cfgPath == "" {
		cfgPath = filepath.Join("..", "..", "config", "config.yaml")
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Skipf("конфиг %s не читается: %v — пропускаю (деталь развёртывания, не срыв теста)", cfgPath, err)
	}
	if !cfg.Telegram.Enabled || cfg.Telegram.Token == "" || cfg.Telegram.ChatID == "" {
		t.Skip("telegram.enabled=false или не заданы token/chat_id — реальной доставки в конфиге нет; " +
			"заполните креденшелы — тест утвердит доставку")
	}
	cl := &Client{
		BaseURL: cfg.Telegram.BaseURL,
		Token:   cfg.Telegram.Token,
		ChatID:  cfg.Telegram.ChatID,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
	text, err := Render("test", []byte(`{}`))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := cl.Send(t.Context(), text); err != nil {
		t.Fatalf("реальная доставка не подтверждена: %v", err)
	}
	t.Log("доставлено: настоящее Telegram-сообщение принято Bot API")
}
