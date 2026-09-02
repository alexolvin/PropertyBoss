package notify

// Live-тесты очереди уведомлений: PB_TEST_DSN + live-лок.
// Telegram подменяется mock-сервером (httptest) — live-доставка
// проверяется в интеграции с очередью, а не с настоящим API
// (токен в тестах быть не должен).

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/db"
)

func notifyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PB_TEST_DSN")
	if dsn == "" {
		t.Skip("PB_TEST_DSN не задан — live-тест пропускается")
	}
	pool, err := db.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	unlock, err := db.LiveTestLock(context.Background(), pool)
	if err != nil {
		t.Fatalf("live lock: %v", err)
	}
	// LIFO: чистка (регистрируется позже) — под локом, лок — последний.
	t.Cleanup(pool.Close)
	t.Cleanup(unlock)
	return pool
}

func liqPayload(country string) map[string]any {
	return map[string]any{
		"country":            country,
		"deal_type":          "sale",
		"model_version":      "liq-test-v1",
		"horizon_days":       30,
		"n_completed_events": 12,
		"min_events":         10,
		"n_person_periods":   100,
		"n_params":           8,
		"train_cutoff_at":    "2026-01-01T00:00:00Z",
		"n_train":            80,
		"n_test":             20,
		"c_index":            0.7,
		"brier_score":        0.1,
		"max_calib_dev":      0.05,
		"previous_status":    "insufficient_history",
	}
}

// TestLiveNotifyQueueFlow — постановка → доставка → status='sent'.
func TestLiveNotifyQueueFlow(t *testing.T) {
	pool := notifyPool(t)
	ctx := t.Context()

	kindToPayload := map[string]map[string]any{
		"delist_anomaly": {"source_id": "pb-test-src", "active_objects": 100,
			"candidates": 40, "share_pct": 40, "max_share_pct": 25},
		"disk_low": {"path": t.TempDir(), "free_pct": 5, "free_gib": 1,
			"total_gib": 20, "critical_pct": 10},
	}
	var ids []int64
	for _, kind := range []string{"delist_anomaly", "disk_low", "liquidity_model"} {
		p := kindToPayload[kind]
		if p == nil {
			p = liqPayload("LL")
		}
		id, err := Enqueue(ctx, pool, kind, p)
		if err != nil {
			t.Fatalf("Enqueue %s: %v", kind, err)
		}
		ids = append(ids, id)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM notifications WHERE id = ANY($1)`, ids)
	})

	srv, calls := mockTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		okResponse(w)
	})
	cl := notifyClientTest(t, srv)
	sent, failed, err := Flush(ctx, pool, cl, 10)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if sent != 3 || failed != 0 {
		t.Fatalf("sent=%d failed=%d, ждали 3/0", sent, failed)
	}
	if len(*calls) != 3 {
		t.Fatalf("mock получил запросов %d, ждали 3", len(*calls))
	}
	for _, c := range *calls {
		if c.ChatID != "42" || c.Text == "" {
			t.Errorf("mock: chat=%q text=%q", c.ChatID, c.Text)
		}
	}
	// Порядок — по id (очередь FIFO), текст соответствует kind.
	wantFragments := []string{"Аномальный delist-прогон", "Мало свободного места", "Модель ликвидности"}
	for i, frag := range wantFragments {
		if !strings.Contains((*calls)[i].Text, frag) {
			t.Errorf("сообщение %d: нет %q:\n%s", i+1, frag, (*calls)[i].Text)
		}
	}

	var pending int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE id = ANY($1) AND status != 'sent'`,
		ids).Scan(&pending); err != nil {
		t.Fatalf("чтение статусов: %v", err)
	}
	if pending != 0 {
		t.Errorf("не-'sent' строк %d, ждали 0", pending)
	}
	var createdAtSet int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE id = ANY($1) AND created_at IS NOT NULL`,
		ids).Scan(&createdAtSet); err != nil {
		t.Fatalf("чтение created_at: %v", err)
	}
	if createdAtSet != 3 {
		t.Errorf("created_at заполнен у %d из 3 (миграция 0021)", createdAtSet)
	}
}

// TestLiveNotifyQueuePermanent — 400 (чат не найден): строка failed с
// текстом ошибки, прогон по очереди ОСТАНАВЛИВАЕТСЯ (дальше — так же).
func TestLiveNotifyQueuePermanent(t *testing.T) {
	pool := notifyPool(t)
	ctx := t.Context()

	id1, err := Enqueue(ctx, pool, "disk_low", map[string]any{
		"path": "/x", "free_pct": 5, "free_gib": 1, "total_gib": 20, "critical_pct": 10})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := Enqueue(ctx, pool, "test", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM notifications WHERE id = ANY($1)`, []int64{id1, id2})
	})

	srv, _ := mockTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"chat not found"}`))
	})
	cl := notifyClientTest(t, srv)
	sent, failed, err := Flush(ctx, pool, cl, 10)
	if err == nil {
		t.Fatal("Flush при 400 должен вернуть ошибку")
	}
	if sent != 0 || failed != 1 {
		t.Fatalf("sent=%d failed=%d, ждали 0/1", sent, failed)
	}

	var st1, st2 string
	var errMsg *string
	if err := pool.QueryRow(ctx,
		`SELECT status, error FROM notifications WHERE id = $1`, id1).Scan(&st1, &errMsg); err != nil {
		t.Fatal(err)
	}
	if st1 != "failed" || errMsg == nil || !strings.Contains(*errMsg, "chat not found") {
		t.Errorf("строка 1: status=%q error=%v, ждали failed + текст ошибки", st1, errMsg)
	}
	if err := pool.QueryRow(ctx,
		`SELECT status FROM notifications WHERE id = $1`, id2).Scan(&st2); err != nil {
		t.Fatal(err)
	}
	if st2 != "pending" {
		t.Errorf("строка 2: status=%q, ждали pending (прогон остановлен)", st2)
	}
}

// TestLiveNotifyQueueRetryable — 500: строка failed, но прогон
// продолжается (следующая строка достаётся и доставляется).
func TestLiveNotifyQueueRetryable(t *testing.T) {
	saved := retryWait
	retryWait = []time.Duration{0, 0}
	t.Cleanup(func() { retryWait = saved })

	pool := notifyPool(t)
	ctx := t.Context()

	id1, err := Enqueue(ctx, pool, "disk_low", map[string]any{
		"path": "/x", "free_pct": 5, "free_gib": 1, "total_gib": 20, "critical_pct": 10})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := Enqueue(ctx, pool, "test", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM notifications WHERE id = ANY($1)`, []int64{id1, id2})
	})

	attempts := 0
	srv, _ := mockTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= maxSendAttempts {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":500,"description":"boom"}`))
			return
		}
		okResponse(w)
	})
	cl := notifyClientTest(t, srv)
	sent, failed, err := Flush(ctx, pool, cl, 10)
	if err != nil {
		t.Fatalf("Flush (retryable): %v", err)
	}
	if sent != 1 || failed != 1 {
		t.Fatalf("sent=%d failed=%d, ждали 1/1", sent, failed)
	}
	var st1, st2 string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM notifications WHERE id = $1`, id1).Scan(&st1); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT status FROM notifications WHERE id = $1`, id2).Scan(&st2); err != nil {
		t.Fatal(err)
	}
	if st1 != "failed" || st2 != "sent" {
		t.Errorf("статусы: %q/%q, ждали failed/sent (500 не останавливает очередь)", st1, st2)
	}
}

// TestLiveCheckDisk — алерт при пороге, ретлимит-окно, повтор при
// realert_minutes=0, тишина при недостижении порога.
func TestLiveCheckDisk(t *testing.T) {
	pool := notifyPool(t)
	ctx := t.Context()

	path := t.TempDir()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM notifications WHERE kind = 'disk_low' AND payload->>'path' = $1`, path)
	})

	// Порог 99.99% — свободное место ниже его всегда: алерт гарантирован.
	info, queued, err := CheckDisk(ctx, pool, path, 99.99, 1440)
	if err != nil {
		t.Fatalf("CheckDisk: %v", err)
	}
	if !queued {
		t.Fatalf("алерт не поставлен при %v%% < 99.99%%", info.FreePct)
	}
	// Внутри окна повтора — тихо.
	if _, queued, err := CheckDisk(ctx, pool, path, 99.99, 1440); err != nil {
		t.Fatal(err)
	} else if queued {
		t.Error("повторный алерт внутри окна realert_minutes — ждали тишины")
	}
	// realert_minutes=0 — окно нет, алерт снова.
	if _, queued, err := CheckDisk(ctx, pool, path, 99.99, 0); err != nil {
		t.Fatal(err)
	} else if !queued {
		t.Error("реалерт с realert_minutes=0 не поставлен")
	}
	// Порог ниже фактического свободного места — тишина.
	if _, queued, err := CheckDisk(ctx, pool, path, 0.001, 0); err != nil {
		t.Fatal(err)
	} else if queued {
		t.Error("алерт при недостижении порога — ждали тишины")
	}

	// В очереди — ровно два disk_low от этого прогона.
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE kind = 'disk_low' AND payload->>'path' = $1`,
		path).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("disk_low в очереди: %d, ждали 2", n)
	}
}

// TestLiveObjectSnapshot — снимок объекта: цены/оценка/вероятность +
// рендер «не голое число» (интервал, n, горизонт, число событий).
func TestLiveObjectSnapshot(t *testing.T) {
	pool := notifyPool(t)
	ctx := t.Context()

	var fullID, bareID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO objects (country, deal_type, address, area_sqm,
			current_price_minor, currency, status, first_seen_at, last_seen_at)
		VALUES ('ZZ', 'sale', 'Тестовая 1', 50, 450000000, 'CZK', 'active',
			now() - interval '12 days', now()) RETURNING id`).Scan(&fullID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO objects (country, deal_type, address, status,
			first_seen_at, last_seen_at)
		VALUES ('ZZ', 'sale', 'Тестовая 2', 'active', now(), now())
		RETURNING id`).Scan(&bareID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM objects WHERE id = ANY($1)`, []int64{fullID, bareID})
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO valuations (object_id, model_version, price_deviation,
			interval_low_minor, interval_high_minor, sample_size, r_squared,
			zone_fallback, computed_at)
		VALUES ($1, 'test-hedonic', -0.05, 420000000, 470000000, 120, 0.8, false, now())`,
		fullID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO liquidity_estimates (object_id, horizon_days, hazard_probability,
			model_version, events_in_training, computed_at)
		VALUES ($1, 30, 0.12, 'test-liq', 105, now())`, fullID); err != nil {
		t.Fatal(err)
	}

	renderSnap := func(id int64) string {
		t.Helper()
		snap, err := ObjectSnapshotFor(ctx, pool, id)
		if err != nil {
			t.Fatalf("ObjectSnapshotFor(%d): %v", id, err)
		}
		raw, err := json.Marshal(snap.Payload())
		if err != nil {
			t.Fatal(err)
		}
		text, err := Render("object_snapshot", raw)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		return text
	}

	text := renderSnap(fullID)
	for _, want := range []string{
		"4 500 000 CZK", "12 дн",
		"-5.0%", "4 200 000 CZK — 4 700 000 CZK", "n=120",
		"30 дн.", "12.0%", "105 событий",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("снимок: нет %q:\n%s", want, text)
		}
	}

	// Объект без оценок — честно «нет», а не выдуманные числа.
	bare := renderSnap(bareID)
	for _, want := range []string{"оценка не выполнялась", "модель ликвидности не запускалась"} {
		if !strings.Contains(bare, want) {
			t.Errorf("пустой снимок: нет %q:\n%s", want, bare)
		}
	}

	// Невыбранный объект — ErrNoRows (не пустой снимок).
	if _, err := ObjectSnapshotFor(ctx, pool, -1); err == nil {
		t.Error("несуществующий объект: ждали ошибку")
	}
}
