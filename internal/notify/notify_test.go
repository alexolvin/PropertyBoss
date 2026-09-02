package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- Render ---------------------------------------------------------

func TestRenderDelistAnomaly(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"source_id":      "bazos-reality",
		"active_objects": 8221,
		"candidates":     3000,
		"share_pct":      36.5,
		"max_share_pct":  25,
	})
	text, err := Render("delist_anomaly", payload)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{"bazos-reality", "3000", "8221", "36.5%", "25%"} {
		if !strings.Contains(text, want) {
			t.Errorf("текст не содержит %q:\n%s", want, text)
		}
	}

	// Обязательное поле отсутствует — ошибка, а не умолчание.
	bad, _ := json.Marshal(map[string]any{"source_id": "x"})
	if _, err := Render("delist_anomaly", bad); err == nil {
		t.Error("payload без обязательных полей должен быть ошибкой")
	}
}

func TestRenderDiskLow(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"path":         "/data",
		"free_pct":     8.4,
		"free_gib":     12.7,
		"total_gib":    150,
		"critical_pct": 10,
	})
	text, err := Render("disk_low", payload)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{"/data", "8.4%", "12.7", "150", "10%"} {
		if !strings.Contains(text, want) {
			t.Errorf("текст не содержит %q:\n%s", want, text)
		}
	}
}

func TestRenderLiquidityModel(t *testing.T) {
	ci, br, dev := 0.721, 0.081, 0.062
	payload, _ := json.Marshal(map[string]any{
		"country":            "CZ",
		"deal_type":          "sale",
		"model_version":      "liq-discrete-v1-20260831-0200",
		"horizon_days":       30,
		"n_completed_events": 105,
		"min_events":         100,
		"n_person_periods":   1234,
		"n_params":           31,
		"train_cutoff_at":    "2026-07-01T00:00:00Z",
		"n_train":            890,
		"n_test":             344,
		"c_index":            ci,
		"brier_score":        br,
		"max_calib_dev":      dev,
		"previous_status":    "insufficient_history",
	})
	text, err := Render("liquidity_model", payload)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// Критерий этапа 8: выборка + разрез, а не голое число.
	for _, want := range []string{
		"CZ/sale", "liq-discrete-v1-20260831-0200", "30 дн",
		"105", "100", "1234", "31",
		"890", "2026-07-01 00:00 UTC", "344",
		"0.721", "0.081", "6.2 п.п.",
		"insufficient_history",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("текст не содержит %q:\n%s", want, text)
		}
	}
}

func TestRenderLiquidityModelNilMetrics(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"country":            "LL",
		"deal_type":          "sale",
		"model_version":      "v",
		"horizon_days":       30,
		"n_completed_events": 12,
		"min_events":         10,
		"n_person_periods":   100,
		"train_cutoff_at":    "2026-01-01T00:00:00Z",
		"n_train":            80,
		"n_test":             20,
		"c_index":            nil, // сравнимых пар не сложилось
		"brier_score":        nil,
		"max_calib_dev":      nil,
		"previous_status":    "первый прогон",
	})
	text, err := Render("liquidity_model", payload)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(text, "н/д") {
		t.Errorf("nil-метрики должны показываться как н/д:\n%s", text)
	}
}

func TestRenderObjectSnapshotFull(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"object_id":         123,
		"country":           "CZ",
		"deal_type":         "sale",
		"status":            "active",
		"price_minor":       450000000, // 4 500 000 CZK
		"currency":          "CZK",
		"currency_exponent": 2,
		"days_on_market":    12,
		"valuation": map[string]any{
			"deviation_pct":       -0.052,
			"deviation_reason":    "",
			"interval_low_minor":  420000000,
			"interval_high_minor": 470000000,
			"sample_size":         120,
			"r_squared":           0.8,
			"zone_fallback":       false,
			"model_version":       "hedonic-r2-v1-20260801",
		},
		"hazard": map[string]any{
			"probability":        0.1234,
			"null_reason":        "",
			"horizon_days":       30,
			"model_version":      "liq-discrete-v1-20260815",
			"events_in_training": 105,
		},
	})
	text, err := Render("object_snapshot", payload)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// Цена с группировкой, интервал + размер выборки (критерий этапа 8),
	// вероятность — с горизонтом и числом событий.
	for _, want := range []string{
		"Объект 123 (CZ/sale, active)",
		"4 500 000 CZK", "12 дн",
		"-5.2%", "4 200 000 CZK — 4 700 000 CZK", "n=120", "hedonic-r2-v1-20260801",
		"30 дн.", "12.3%", "liq-discrete-v1-20260815", "105 событий",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("текст не содержит %q:\n%s", want, text)
		}
	}
}

func TestRenderObjectSnapshotNulls(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"object_id":         1,
		"country":           "CZ",
		"deal_type":         "sale",
		"status":            "active",
		"price_minor":       nil,
		"currency":          nil,
		"currency_exponent": nil,
		"days_on_market":    nil,
		"valuation": map[string]any{
			"deviation_pct":    nil,
			"deviation_reason": "insufficient_observations_in_zone",
			"sample_size":      0,
			"model_version":    "hedonic-r2-v1",
		},
		"hazard": map[string]any{
			"probability":        nil,
			"null_reason":        "insufficient_history",
			"horizon_days":       30,
			"model_version":      "liq-v1",
			"events_in_training": 0,
		},
	})
	text, err := Render("object_snapshot", payload)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		"цена: нет в объявлении",
		"insufficient_observations_in_zone",
		"insufficient_history",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("текст не содержит %q:\n%s", want, text)
		}
	}
}

func TestRenderUnknownKind(t *testing.T) {
	if _, err := Render("unknown_kind", []byte("{}")); err == nil {
		t.Error("неизвестный kind должен быть ошибкой")
	}
}

func TestGroupNum(t *testing.T) {
	cases := map[string]string{
		"4500000":   "4 500 000",
		"100":       "100",
		"1234567.5": "1 234 567.5",
		"-42":       "-42",
		"123456":    "123 456",
	}
	for in, want := range cases {
		if got := groupNum(in); got != want {
			t.Errorf("groupNum(%q) = %q, ждали %q", in, got, want)
		}
	}
}

// --- Telegram-клиент (mock Bot API) ---------------------------------

type tgCall struct {
	Path   string
	ChatID string
	Text   string
}

// mockTelegram — Bot API в httptest; записи запросов + handler ответа.
func mockTelegram(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *[]tgCall) {
	t.Helper()
	calls := &[]tgCall{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		*calls = append(*calls, tgCall{Path: r.URL.Path, ChatID: body["chat_id"], Text: body["text"]})
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}

func okResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func TestTelegramClientSuccess(t *testing.T) {
	srv, calls := mockTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		okResponse(w)
	})
	cl := notifyClientTest(t, srv)
	if err := cl.Send(context.Background(), "привет"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("запросов %d, ждали 1", len(*calls))
	}
	c := (*calls)[0]
	if c.Path != "/bot/tok123/sendMessage" {
		t.Errorf("path = %q", c.Path)
	}
	if c.ChatID != "42" || c.Text != "привет" {
		t.Errorf("body: chat=%q text=%q", c.ChatID, c.Text)
	}
}

// notifyClientTest — Client с mock-сервером и no-op sleep.
func notifyClientTest(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return &Client{
		BaseURL: srv.URL,
		Token:   "tok123",
		ChatID:  "42",
		HTTP:    srv.Client(),
		Sleep:   func(time.Duration) {},
	}
}

func TestTelegramClientPermanent400(t *testing.T) {
	srv, calls := mockTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"chat not found"}`))
	})
	cl := notifyClientTest(t, srv)
	err := cl.Send(context.Background(), "тест")
	if err == nil {
		t.Fatal("400 должен быть ошибкой")
	}
	var perm *ErrPermanent
	if !errors.As(err, &perm) {
		t.Fatalf("ошибка %v не ErrPermanent", err)
	}
	if len(*calls) != 1 {
		t.Errorf("запросов %d, ждали 1 (повтор 400 бессмыслен)", len(*calls))
	}
}

func TestTelegramClientRetryAfter429(t *testing.T) {
	tSaved := retryWait
	retryWait = []time.Duration{0, 0}
	t.Cleanup(func() { retryWait = tSaved })

	// Свой сервер (не mockTelegram): handler ссылается на calls.
	calls := &[]tgCall{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		*calls = append(*calls, tgCall{Path: r.URL.Path, ChatID: body["chat_id"], Text: body["text"]})
		if len(*calls) <= 1 {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"retry later"}`))
			return
		}
		okResponse(w)
	}))
	t.Cleanup(srv.Close)
	cl := notifyClientTest(t, srv)
	if err := cl.Send(context.Background(), "тест"); err != nil {
		t.Fatalf("Send после 429: %v", err)
	}
	if len(*calls) != 2 {
		t.Errorf("запросов %d, ждали 2 (429 → повтор)", len(*calls))
	}
}

func TestTelegramClient5xxExhausted(t *testing.T) {
	tSaved := retryWait
	retryWait = []time.Duration{0, 0}
	t.Cleanup(func() { retryWait = tSaved })

	srv, calls := mockTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":500,"description":"internal"}`))
	})
	cl := notifyClientTest(t, srv)
	err := cl.Send(context.Background(), "тест")
	if err == nil {
		t.Fatal("3×500 должно закончиться ошибкой")
	}
	if len(*calls) != maxSendAttempts {
		t.Errorf("запросов %d, ждали %d", len(*calls), maxSendAttempts)
	}
}

// --- диск -------------------------------------------------------------

func TestMeasureDisk(t *testing.T) {
	info, err := MeasureDisk(t.TempDir())
	if err != nil {
		t.Fatalf("MeasureDisk: %v", err)
	}
	if info.TotalGiB <= 0 {
		t.Errorf("TotalGiB = %v, ждали > 0", info.TotalGiB)
	}
	if info.FreePct <= 0 || info.FreePct > 100 {
		t.Errorf("FreePct = %v, ждали в (0, 100]", info.FreePct)
	}
	if info.FreeGiB > info.TotalGiB {
		t.Errorf("FreeGiB %v > TotalGiB %v", info.FreeGiB, info.TotalGiB)
	}
}
