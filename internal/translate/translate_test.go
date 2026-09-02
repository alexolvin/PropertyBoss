package translate

// Юнит-тесты переводчика без БД: детектор языка, выбор целевых языков,
// хеш идемпотентности и LLM-клиент (OpenAI-совместимый, mock через
// httptest). Live-тесты полного прогона — в translate_live_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- Детектор языка -------------------------------------------------------

// TestDetect — реальные тексты объявлений четырёх латинских языков и
// кириллица. Для языка, не входящего в целевой набор (немецкий),
// детектор обязан дать НИЗКУЮ уверенность (language_original не
// переопределяется), а не угадать.
func TestDetect(t *testing.T) {
	d := NewDetector()
	cases := []struct {
		name    string
		text    string
		tag     string  // ожидаемый язык; "" — не должно быть чёткого ответа
		minConf float64 // для tag != "" уверенность должна быть не ниже
	}{
		{"czech", "Prodám světlý byt s prostornou kuchyní a balkonem. Vytápění plynové, nová podlahová krytina, klidná ulice, blízko metra.", "cs", 0.5},
		{"italian", "Vendiamo appartamento con soggiorno ampio, cucina abitabile e balcone. Riscaldamento a metano, pavimentazione nuova, zona tranquilla, vicino alla fermata del metrò.", "it", 0.5},
		// conf ниже, чем у других: английскому «ing» подстрокой достаётся
		// нидерландское «verwarming» — честная погрешность подстрочного
		// маркера; победитель (nl) всё равно верный, conf > порога 0.35.
		{"dutch", "Verkoop licht appartement met ruime keuken en balkon. Gasverwarming, nieuwe vloer, rustige straat, dicht bij de metro.", "nl", 0.4},
		{"english", "Bright two bedroom flat for sale in a quiet street. New kitchen, modern bathroom, close to the metro station. Gas heating included.", "en", 0.5},
		{"russian", "Продаётся светлая двухкомнатная квартира с кухней и балконом. Новое отопление, тихий двор, рядом метро. Тёплые полы, новая проводка.", "ru", 0.9},
		// Нецелевой язык: честная неопределённость, а не угадывание.
		{"german", "Helle Wohnung mit Küche und Balkon in ruhiger Lage, nahe der U-Bahn, neuer Boden, Gasheizung.", "", 0},
		// Короткий текст: по чему судить (м. константа minDetectLetters).
		{"short", "Byt na prodej", "", 0},
		// Пустой текст.
		{"empty", "", "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tag, conf := d.Detect(c.text)
			if c.tag == "" {
				if conf >= detectConfThreshold {
					t.Fatalf("Detect(%q) = (%q, %v): уверенность %v выше порога %v — для нецелевого/короткого текста неопределённость честнее угадывания",
						c.text, tag, conf, conf, detectConfThreshold)
				}
				return
			}
			if tag != c.tag {
				t.Fatalf("Detect = %q, ждали %q (conf %v)", tag, c.tag, conf)
			}
			if conf < c.minConf {
				t.Fatalf("conf = %v, ждали >= %v", conf, c.minConf)
			}
		})
	}
}

// --- Целевые языки и хеш --------------------------------------------------

func TestTargets(t *testing.T) {
	cases := []struct {
		orig string
		want []string
	}{
		{"ru", []string{"en"}},
		{"en", []string{"ru"}},
		{"cs", []string{"ru", "en"}},
		{"it", []string{"ru", "en"}},
		{"nl", []string{"ru", "en"}},
		{"", []string{"ru", "en"}},   // язык не определён — оба
		{"de", []string{"ru", "en"}}, // нецелевой — оба
		{"RU", []string{"ru", "en"}}, // регистр не варварский: коннектор/БД дают нижний, но не ломаемся
	}
	for _, c := range cases {
		got := Targets(c.orig)
		if len(got) != len(c.want) {
			t.Errorf("Targets(%q) = %v, ждали %v", c.orig, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("Targets(%q) = %v, ждали %v", c.orig, got, c.want)
				break
			}
		}
	}
}

func TestHashDescription(t *testing.T) {
	// Известный sha256('test') — хеш в Go обязан совпасть с SQL
	// encode(digest(..., 'sha256'), 'hex') (проверено в БД).
	if got := HashDescription("test"); got != "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08" {
		t.Fatalf("HashDescription(test) = %q — не совпадает со стандартным sha256", got)
	}
	if HashDescription("a") == HashDescription("b") {
		t.Fatal("разные тексты дали одинаковый хеш")
	}
	if HashDescription("a") != HashDescription("a") {
		t.Fatal("хеш нестабилен")
	}
}

// --- LLM-клиент -----------------------------------------------------------

// zeroLLMRetries — паузы ретрая в ноль: тесты не ждут секунды.
func zeroLLMRetries(t *testing.T) {
	t.Helper()
	saved := llmRetryWait
	llmRetryWait = []time.Duration{0, 0}
	t.Cleanup(func() { llmRetryWait = saved })
}

func mockLLMServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, i int)) (*httptest.Server, *int32) {
	t.Helper()
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(atomic.AddInt32(&n, 1))
		handler(w, r, i)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			t.Errorf("запись ответа: %v", err)
		}
	}
}

func llmOKBody(text, model string, tokens int) map[string]any {
	usage := map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": tokens}
	if tokens == 0 {
		usage = map[string]any{"prompt_tokens": 10, "completion_tokens": 5} // total_tokens нет — путь fallback
	}
	return map[string]any{
		"model":   model,
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": text}}},
		"usage":   usage,
	}
}

func TestLLMTranslateSuccess(t *testing.T) {
	zeroLLMRetries(t)
	var gotPath, gotAuth string
	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	srv, n := mockLLMServer(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("тело запроса не JSON: %v", err)
		}
		writeJSON(t, w, http.StatusOK, llmOKBody("  Светлая квартира ", "model-from-server", 15))
	})
	cl := &Client{BaseURL: srv.URL, APIKey: "secret-key", Model: "model-requested"}
	res, err := cl.Translate(context.Background(), "Světlý byt", "ru")
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if want := "Светлая квартира"; res.Text != want {
		t.Errorf("Text = %q, ждали %q (TrimSpace)", res.Text, want)
	}
	// Имя модели — реально обслуживавшей запрос (ответ API), не запрошенная.
	if res.Model != "model-from-server" {
		t.Errorf("Model = %q, ждали модель из ответа API", res.Model)
	}
	if res.Tokens != 15 {
		t.Errorf("Tokens = %d, ждали 15", res.Tokens)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("путь %q, ждали /chat/completions", gotPath)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if req.Model != "model-requested" {
		t.Errorf("model в запросе = %q", req.Model)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
		t.Errorf("messages = %+v, ждали system+user", req.Messages)
	}
	if !strings.Contains(req.Messages[0].Content, "Russian") || !strings.Contains(req.Messages[0].Content, "(ru)") {
		t.Errorf("системный промпт не описывает цель: %q", req.Messages[0].Content)
	}
	if req.Messages[1].Content != "Světlý byt" {
		t.Errorf("user-сообщение = %q, ждали оригинал", req.Messages[1].Content)
	}
	if *n != 1 {
		t.Errorf("запросов %d, ждали 1 (успех с первого)", *n)
	}
}

func TestLLMTranslateUsageFallback(t *testing.T) {
	zeroLLMRetries(t)
	srv, _ := mockLLMServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(t, w, http.StatusOK, llmOKBody("text", "m", 0))
	})
	cl := &Client{BaseURL: srv.URL, APIKey: "k", Model: "m"}
	res, err := cl.Translate(context.Background(), "x", "ru")
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if res.Tokens != 15 {
		t.Errorf("Tokens = %d, ждали 15 (prompt+completion при отсутствии total)", res.Tokens)
	}
}

func TestLLMTranslateModelFallback(t *testing.T) {
	zeroLLMRetries(t)
	srv, _ := mockLLMServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "text"}}},
			"usage":   map[string]any{"total_tokens": 7},
		})
	})
	cl := &Client{BaseURL: srv.URL, APIKey: "k", Model: "requested-model"}
	res, err := cl.Translate(context.Background(), "x", "ru")
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if res.Model != "requested-model" {
		t.Errorf("Model = %q, ждали запрошенную (в ответе её нет)", res.Model)
	}
}

// TestLLMTranslatePermanent — прочие 4xx: ретраем не лечится, один
// запрос, тип *ErrPermanent (прогон останавливается).
func TestLLMTranslatePermanent(t *testing.T) {
	zeroLLMRetries(t)
	srv, n := mockLLMServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(t, w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": "invalid api key", "type": "auth"}})
	})
	cl := &Client{BaseURL: srv.URL, APIKey: "wrong", Model: "m"}
	_, err := cl.Translate(context.Background(), "x", "ru")
	if err == nil {
		t.Fatal("400: ждали ошибку")
	}
	var perm *ErrPermanent
	if !errors.As(err, &perm) {
		t.Fatalf("ошибка %T %v, ждали *ErrPermanent", err, err)
	}
	if !strings.Contains(perm.Msg, "invalid api key") {
		t.Errorf("сообщение %q без причины API", perm.Msg)
	}
	if *n != 1 {
		t.Errorf("запросов %d, ждали 1 (4xx не ретраится)", *n)
	}
}

// TestLLMTranslateRateLimit — 429: пауза retry_after, повтор, успех.
func TestLLMTranslateRateLimit(t *testing.T) {
	zeroLLMRetries(t)
	var slept []time.Duration
	srv, n := mockLLMServer(t, func(w http.ResponseWriter, _ *http.Request, i int) {
		if i == 1 {
			w.Header().Set("Retry-After", "1")
			writeJSON(t, w, http.StatusTooManyRequests, map[string]any{
				"error": map[string]any{"message": "rate limited"}})
			return
		}
		writeJSON(t, w, http.StatusOK, llmOKBody("ok", "m", 12))
	})
	cl := &Client{BaseURL: srv.URL, APIKey: "k", Model: "m", Sleep: func(d time.Duration) { slept = append(slept, d) }}
	res, err := cl.Translate(context.Background(), "x", "ru")
	if err != nil {
		t.Fatalf("Translate после 429: %v", err)
	}
	if res.Text != "ok" || *n != 2 {
		t.Errorf("Text=%q запросов=%d, ждали ok/2", res.Text, *n)
	}
	if len(slept) != 1 || slept[0] != time.Second {
		t.Errorf("паузы %v, ждали [1s] (Retry-After: 1)", slept)
	}
}

// TestLLMTranslateRateLimitExhausted — 429 до конца попыток: мягкая
// ошибка *ErrRateLimit (прогон остановится до следующего cron).
func TestLLMTranslateRateLimitExhausted(t *testing.T) {
	zeroLLMRetries(t)
	srv, n := mockLLMServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.Header().Set("Retry-After", "0")
		writeJSON(t, w, http.StatusTooManyRequests, nil)
	})
	cl := &Client{BaseURL: srv.URL, APIKey: "k", Model: "m", Sleep: func(time.Duration) {}}
	_, err := cl.Translate(context.Background(), "x", "ru")
	if err == nil {
		t.Fatal("ждали ошибку после исчерпания 429")
	}
	var rl *ErrRateLimit
	if !errors.As(err, &rl) {
		t.Fatalf("ошибка %T %v, ждали *ErrRateLimit", err, err)
	}
	if *n != maxTranslateAttempts {
		t.Errorf("запросов %d, ждали %d", *n, maxTranslateAttempts)
	}
}

// TestLLMTranslateRetryable — 5xx: ретраится; 500/200 — успех на второй.
func TestLLMTranslateRetryable(t *testing.T) {
	zeroLLMRetries(t)
	srv, n := mockLLMServer(t, func(w http.ResponseWriter, _ *http.Request, i int) {
		if i == 1 {
			writeJSON(t, w, http.StatusInternalServerError, map[string]any{
				"error": map[string]any{"message": "boom"}})
			return
		}
		writeJSON(t, w, http.StatusOK, llmOKBody("ok", "m", 9))
	})
	cl := &Client{BaseURL: srv.URL, APIKey: "k", Model: "m"}
	res, err := cl.Translate(context.Background(), "x", "ru")
	if err != nil {
		t.Fatalf("Translate (500→200): %v", err)
	}
	if res.Text != "ok" || *n != 2 {
		t.Errorf("Text=%q запросов=%d, ждали ok/2", res.Text, *n)
	}
}

// TestLLMTranslate5xxExhausted — 500 трижды: ошибка (не перманентная),
// запросов ровно maxTranslateAttempts.
func TestLLMTranslate5xxExhausted(t *testing.T) {
	zeroLLMRetries(t)
	srv, n := mockLLMServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(t, w, http.StatusInternalServerError, map[string]any{
			"error": map[string]any{"message": "down"}})
	})
	cl := &Client{BaseURL: srv.URL, APIKey: "k", Model: "m"}
	_, err := cl.Translate(context.Background(), "x", "ru")
	if err == nil {
		t.Fatal("ждали ошибку после исчерпания 5xx")
	}
	var perm *ErrPermanent
	if errors.As(err, &perm) {
		t.Fatalf("5xx не перманентная ошибка: %v", err)
	}
	if *n != maxTranslateAttempts {
		t.Errorf("запросов %d, ждали %d", *n, maxTranslateAttempts)
	}
}

// TestLLMTranslateEmpty — пустой перевод не перевод: ошибка, ретраем
// лечится (API мог сбойнуть), но не сохраняется (ТЗ §11: псевдоперевод
// запрещён).
func TestLLMTranslateEmpty(t *testing.T) {
	zeroLLMRetries(t)
	srv, n := mockLLMServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(t, w, http.StatusOK, llmOKBody("   ", "m", 5))
	})
	cl := &Client{BaseURL: srv.URL, APIKey: "k", Model: "m"}
	_, err := cl.Translate(context.Background(), "x", "ru")
	if err == nil {
		t.Fatal("пустой перевод: ждали ошибку")
	}
	if *n != maxTranslateAttempts {
		t.Errorf("запросов %d, ждали %d (пустой ответ ретраится)", *n, maxTranslateAttempts)
	}
}

func TestLLMTranslateConfig(t *testing.T) {
	zeroLLMRetries(t)
	cl := &Client{BaseURL: "http://127.0.0.1:1", Model: "m"}
	if _, err := cl.Translate(context.Background(), "x", "ru"); err == nil || !strings.Contains(err.Error(), "API-ключ") {
		t.Errorf("без API-ключа: err=%v, ждали явную про api_key", err)
	}
	// Язык проверяется вторым, поэтому у этого клиента ключ задан — иначе
	// сработала бы проверка API-ключа.
	clLang := &Client{BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m"}
	if _, err := clLang.Translate(context.Background(), "x", "de"); err == nil || !strings.Contains(err.Error(), "не поддерживается") {
		t.Errorf("целевой язык de: err=%v, ждали явную (ТЗ §11: ru/en)", err)
	}
}

func TestLLMTranslateCancel(t *testing.T) {
	zeroLLMRetries(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	srv, n := mockLLMServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(t, w, http.StatusOK, llmOKBody("ok", "m", 1))
	})
	cl := &Client{BaseURL: srv.URL, APIKey: "k", Model: "m"}
	_, err := cl.Translate(ctx, "x", "ru")
	if err == nil {
		t.Fatal("отменённый контекст: ждали ошибку")
	}
	if *n != 0 {
		t.Errorf("запросов %d, ждали 0 (ctx отменён до запроса)", *n)
	}
}
