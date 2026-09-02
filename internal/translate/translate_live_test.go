package translate

// Live-тесты переводчика: PB_TEST_DSN + live-лок, песочница — страна QQ.
// LLM подменяется mock-сервером (httptest): live-прогон проверяется на
// настоящую БД (хеши, идемпотентность, CASCADE), но реальный API в тестах
// быть не должен (ключи/затраты). Mock детерминирован: «<lang>» +
// оригинал — видно, какой текст ушёл в API и для какого языка.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/db"
)

const (
	// Образцы в стиле реальных объявлений (с маркерами языка — детектор
	// должен подтвердить язык, а не угадать).
	qqDescCS    = "Prodám světlý byt s prostornou kuchyní a balkonem, klidná ulice, blízko metra."
	qqDescCSnew = "Pronajmeme nově zrekonstruovaný byt s plnou vybaveností, včetně pračky a sporáku, blízkost metra."
	qqDescRU    = "Продаётся светлая двухкомнатная квартира с кухней и балконом, тихий двор, рядом метро."
)

func translatePool(t *testing.T) *pgxpool.Pool {
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

// insertQQObject — объект-песочница (QQ): чистка через ON DELETE CASCADE
// у object_translations.
func insertQQObject(t *testing.T, pool *pgxpool.Pool, desc, lang string) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO objects (country, deal_type, description_original, language_original,
			first_seen_at, last_seen_at)
		VALUES ('QQ', 'sale', $1, $2, now(), now()) RETURNING id`, desc, lang).Scan(&id); err != nil {
		t.Fatalf("вставка QQ-объекта: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM objects WHERE id = $1`, id)
	})
	return id
}

// mockLLM — OpenAI-совместимый mock: перевод = «<язык>» + оригинал.
// Возвращает сервер и счётчик реальных HTTP-запросов (идемпотентность
// проверяется именно им: второй прогон = 0 запросов).
func mockLLM(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, i int)) (*httptest.Server, *int32) {
	t.Helper()
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(atomic.AddInt32(&n, 1))
		handler(w, r, i)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// echoLLM — стандартный ответ mock: «<lang>» + user-текст, модель
// mock-llm-1, 15 токенов.
func echoLLM(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "тело: "+err.Error(), http.StatusBadRequest)
		return
	}
	target := "ru"
	if len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, "(en)") {
		target = "en"
	}
	var orig string
	if len(req.Messages) > 1 {
		orig = req.Messages[1].Content
	}
	body, _ := json.Marshal(map[string]any{
		"model":   "mock-llm-1",
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": target + "> " + orig}}},
		"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func llmClient(srvURL string, sleep func(time.Duration)) *Client {
	return &Client{
		BaseURL: srvURL,
		APIKey:  "test-key",
		Model:   "test-model",
		Sleep:   sleep,
	}
}

// TestLiveTranslateFlow — полный проход: чешское описание → строки ru+en
// с хешом, моделью и стоимостью; язык не меняется (cs подтверждён);
// статус: pending −1, строк +2, переведённых объектов +1.
func TestLiveTranslateFlow(t *testing.T) {
	zeroLLMRetries(t)
	pool := translatePool(t)
	ctx := context.Background()

	id := insertQQObject(t, pool, qqDescCS, "cs")

	before, err := Status(ctx, pool, 4000)
	if err != nil {
		t.Fatalf("Status (до): %v", err)
	}

	srv, n := mockLLM(t, func(w http.ResponseWriter, r *http.Request, i int) { echoLLM(w, r) })
	rep, err := Run(ctx, pool, llmClient(srv.URL, nil), NewDetector(), 4000, 0, "QQ")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Objects != 1 || rep.Translated != 2 || rep.Reused != 0 || rep.Skipped != 0 || rep.Failed != 0 {
		t.Fatalf("отчёт %+v, ждали objects=1 translated=2", rep)
	}
	if rep.LanguageUpdated != 0 {
		t.Errorf("language_updated=%d, ждали 0 (cs подтверждён детектором)", rep.LanguageUpdated)
	}
	if *n != 2 {
		t.Errorf("LLM-запросов %d, ждали 2 (ru+en)", *n)
	}

	var lang *string
	var nRows int
	hash := HashDescription(qqDescCS)
	for _, wantLang := range []string{"ru", "en"} {
		var (
			text  string
			model string
			h     string
		)
		var tokens *int
		if err := pool.QueryRow(ctx, `
			SELECT text, model, token_cost, source_hash FROM object_translations
			WHERE object_id = $1 AND lang = $2`, id, wantLang).
			Scan(&text, &model, &tokens, &h); err != nil {
			t.Fatalf("строка %s: %v", wantLang, err)
		}
		if text != wantLang+"> "+qqDescCS {
			t.Errorf("%s: текст %q, ждали %q (оригинал дошёл до API)", wantLang, text, wantLang+"> "+qqDescCS)
		}
		if model != "mock-llm-1" || tokens == nil || *tokens != 15 {
			t.Errorf("%s: model=%q tokens=%v, ждали mock-llm-1/15 (ТЗ §11: model и token_cost хранятся)", wantLang, model, tokens)
		}
		if h != hash {
			t.Errorf("%s: source_hash %s, ждали sha256 оригинала %s", wantLang, h, hash)
		}
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM object_translations WHERE object_id = $1`, id).Scan(&nRows); err != nil {
		t.Fatal(err)
	}
	if nRows != 2 {
		t.Errorf("строк перевода %d, ждали 2", nRows)
	}
	if err := pool.QueryRow(ctx, `SELECT language_original FROM objects WHERE id = $1`, id).Scan(&lang); err != nil {
		t.Fatal(err)
	}
	if lang == nil || *lang != "cs" {
		t.Errorf("language_original = %v, ждали cs", lang)
	}

	after, err := Status(ctx, pool, 4000)
	if err != nil {
		t.Fatalf("Status (после): %v", err)
	}
	if after.Rows-before.Rows != 2 {
		t.Errorf("строк + %d, ждали +2", after.Rows-before.Rows)
	}
	if after.Pending-before.Pending != -1 {
		t.Errorf("pending дельта %d, ждали -1", after.Pending-before.Pending)
	}
	if after.Translated-before.Translated != 1 {
		t.Errorf("переведённых объектов дельта %d, ждали +1", after.Translated-before.Translated)
	}
}

// TestLiveTranslateIdempotent — второй прогон того же состояния: 0
// объектов в кандидатах, 0 обращений к LLM (ТЗ §11: повторный перевод
// того же текста не выполняется).
func TestLiveTranslateIdempotent(t *testing.T) {
	zeroLLMRetries(t)
	pool := translatePool(t)
	ctx := context.Background()

	id := insertQQObject(t, pool, qqDescCS, "cs")
	srv, n := mockLLM(t, func(w http.ResponseWriter, r *http.Request, i int) { echoLLM(w, r) })
	if _, err := Run(ctx, pool, llmClient(srv.URL, nil), NewDetector(), 4000, 0, "QQ"); err != nil {
		t.Fatalf("первый прогон: %v", err)
	}
	if *n != 2 {
		t.Fatalf("первый прогон: запросов %d, ждали 2", *n)
	}

	rep, err := Run(ctx, pool, llmClient(srv.URL, nil), NewDetector(), 4000, 0, "QQ")
	if err != nil {
		t.Fatalf("второй прогон: %v", err)
	}
	if rep.Objects != 0 || rep.Translated != 0 || *n != 2 {
		t.Fatalf("второй прогон: отчёт %+v, запросов всего %d — ждали 0 объектов и 0 новых запросов", rep, *n)
	}
	_ = id
}

// TestLiveTranslateReuse — тот же текст на другом объекте: перевод
// переиспользуется, без обращения к LLM (ТЗ §11; индекс
// translations_hash_idx).
func TestLiveTranslateReuse(t *testing.T) {
	zeroLLMRetries(t)
	pool := translatePool(t)
	ctx := context.Background()

	idA := insertQQObject(t, pool, qqDescCS, "cs")
	idB := insertQQObject(t, pool, qqDescCS, "cs")
	srv, n := mockLLM(t, func(w http.ResponseWriter, r *http.Request, i int) { echoLLM(w, r) })
	if _, err := Run(ctx, pool, llmClient(srv.URL, nil), NewDetector(), 4000, 0, "QQ"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if *n != 2 {
		t.Fatalf("LLM-запросов %d, ждали 2 (переводится один текст, не два)", *n)
	}
	var textA, modelA, textB, modelB string
	var tokensB *int
	if err := pool.QueryRow(ctx, `SELECT text, model FROM object_translations WHERE object_id = $1 AND lang = 'ru'`,
		idA).Scan(&textA, &modelA); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT text, model, token_cost FROM object_translations WHERE object_id = $1 AND lang = 'ru'`,
		idB).Scan(&textB, &modelB, &tokensB); err != nil {
		t.Fatal(err)
	}
	if textA != textB || modelA != modelB || modelB != "mock-llm-1" || tokensB == nil || *tokensB != 15 {
		t.Errorf("переиспользование: A=%q/%q B=%q/%q/%v, ждали идентичные строки (mock-llm-1/15)", textA, modelA, textB, modelB, tokensB)
	}
}

// TestLiveTranslateStale — описание изменилось: старые переводы
// удаляются, текст переводится заново (хеш другой), новые строки — по
// новому хешу.
func TestLiveTranslateStale(t *testing.T) {
	zeroLLMRetries(t)
	pool := translatePool(t)
	ctx := context.Background()

	id := insertQQObject(t, pool, qqDescCS, "cs")
	srv, n := mockLLM(t, func(w http.ResponseWriter, r *http.Request, i int) { echoLLM(w, r) })
	cl := llmClient(srv.URL, nil)
	if _, err := Run(ctx, pool, cl, NewDetector(), 4000, 0, "QQ"); err != nil {
		t.Fatalf("первый прогон: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE objects SET description_original = $2 WHERE id = $1`, id, qqDescCSnew); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(ctx, pool, cl, NewDetector(), 4000, 0, "QQ")
	if err != nil {
		t.Fatalf("второй прогон: %v", err)
	}
	if rep.Translated != 2 || rep.Skipped != 0 {
		t.Fatalf("отчёт %+v, ждали translated=2 (старые ушли, новые переведены)", rep)
	}
	if *n != 4 {
		t.Errorf("LLM-запросов %d, ждали 4 (2+2)", *n)
	}
	var text, h string
	if err := pool.QueryRow(ctx, `
		SELECT text, source_hash FROM object_translations WHERE object_id = $1 AND lang = 'ru'`,
		id).Scan(&text, &h); err != nil {
		t.Fatal(err)
	}
	if text != "ru> "+qqDescCSnew {
		t.Errorf("текст %q, ждали перевод НОВОГО описания", text)
	}
	if h != HashDescription(qqDescCSnew) {
		t.Errorf("source_hash %s, ждали хеш нового описания", h)
	}
	var nRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM object_translations WHERE object_id = $1`, id).Scan(&nRows); err != nil {
		t.Fatal(err)
	}
	if nRows != 2 {
		t.Errorf("строк %d, ждали 2 (старые удалены)", nRows)
	}
}

// TestLiveTranslateLanguageDetection — оригинал на русском на чешском
// сайте: language_original уточняется детектором («cs» → «ru»),
// переводится только en (ru не переводится в ru).
func TestLiveTranslateLanguageDetection(t *testing.T) {
	zeroLLMRetries(t)
	pool := translatePool(t)
	ctx := context.Background()

	id := insertQQObject(t, pool, qqDescRU, "cs")
	srv, n := mockLLM(t, func(w http.ResponseWriter, r *http.Request, i int) { echoLLM(w, r) })
	rep, err := Run(ctx, pool, llmClient(srv.URL, nil), NewDetector(), 4000, 0, "QQ")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.LanguageUpdated != 1 || rep.Translated != 1 || rep.Reused != 0 {
		t.Fatalf("отчёт %+v, ждали language_updated=1 translated=1", rep)
	}
	if *n != 1 {
		t.Errorf("LLM-запросов %d, ждали 1 (только en — оригинал уже ru)", *n)
	}
	var lang *string
	if err := pool.QueryRow(ctx, `SELECT language_original FROM objects WHERE id = $1`, id).Scan(&lang); err != nil {
		t.Fatal(err)
	}
	if lang == nil || *lang != "ru" {
		t.Errorf("language_original = %v, ждали ru (детектор, не страна)", lang)
	}
	var nRu, nEn int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM object_translations WHERE object_id = $1 AND lang = 'ru'`, id).Scan(&nRu); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM object_translations WHERE object_id = $1 AND lang = 'en'`, id).Scan(&nEn); err != nil {
		t.Fatal(err)
	}
	if nRu != 0 || nEn != 1 {
		t.Errorf("строк ru/en = %d/%d, ждали 0/1", nRu, nEn)
	}
}

// TestLiveTranslateTooLong — описание длиннее max_chars: не переводится
// (перевод остаётся NULL), в кандидатах не висит, в статусе видно.
func TestLiveTranslateTooLong(t *testing.T) {
	zeroLLMRetries(t)
	pool := translatePool(t)
	ctx := context.Background()

	long := strings.Repeat("x", 120) // 120 > 100 (наименьший допустимый лимит)
	id := insertQQObject(t, pool, long, "cs")
	srv, n := mockLLM(t, func(w http.ResponseWriter, r *http.Request, i int) { echoLLM(w, r) })
	rep, err := Run(ctx, pool, llmClient(srv.URL, nil), NewDetector(), 100, 0, "QQ")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Objects != 0 || *n != 0 {
		t.Fatalf("отчёт %+v, запросов %d — слишком длинное описание не должно быть кандидатом", rep, *n)
	}
	var nRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM object_translations WHERE object_id = $1`, id).Scan(&nRows); err != nil {
		t.Fatal(err)
	}
	if nRows != 0 {
		t.Errorf("строк перевода %d, ждали 0 (NULL — перевод недоступен, не псевдоперевод)", nRows)
	}
	st, err := Status(ctx, pool, 100)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.TooLong < 1 {
		t.Errorf("статус too_long=%d, ждали >= 1 (наш QQ-объект)", st.TooLong)
	}
}

// TestLiveTranslateLLM500 — 5xx: объект не переведён (NULL, строки нет),
// прогон не останавливается, ошибки прогона нет — повторит следующий cron.
func TestLiveTranslateLLM500(t *testing.T) {
	zeroLLMRetries(t)
	pool := translatePool(t)
	ctx := context.Background()

	id := insertQQObject(t, pool, qqDescCS, "cs")
	srv, n := mockLLM(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"down"}}`))
	})
	rep, err := Run(ctx, pool, llmClient(srv.URL, nil), NewDetector(), 4000, 0, "QQ")
	if err != nil {
		t.Fatalf("Run при 5xx не должен возвращать ошибку: %v", err)
	}
	if rep.Translated != 0 || rep.Failed != 2 {
		t.Fatalf("отчёт %+v, ждали translated=0 failed=2 (ru и en)", rep)
	}
	if *n != 2*maxTranslateAttempts {
		t.Errorf("LLM-запросов %d, ждали %d (по 3 на язык)", *n, 2*maxTranslateAttempts)
	}
	var nRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM object_translations WHERE object_id = $1`, id).Scan(&nRows); err != nil {
		t.Fatal(err)
	}
	if nRows != 0 {
		t.Errorf("строк перевода %d при ошибке API, ждали 0 (NULL)", nRows)
	}
}

// TestLiveTranslateLLM400 — прочие 4xx (неверный ключ): прогон
// ОСТАНАВЛИВАЕТСЯ (дальше — та же ошибка), ошибка *ErrPermanent,
// переводы не пишутся.
func TestLiveTranslateLLM400(t *testing.T) {
	zeroLLMRetries(t)
	pool := translatePool(t)
	ctx := context.Background()

	id1 := insertQQObject(t, pool, qqDescCS, "cs")
	id2 := insertQQObject(t, pool, qqDescCSnew, "cs")
	srv, _ := mockLLM(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	})
	rep, err := Run(ctx, pool, llmClient(srv.URL, nil), NewDetector(), 4000, 0, "QQ")
	if err == nil {
		t.Fatal("400: прогон должен остановиться с ошибкой")
	}
	var perm *ErrPermanent
	if !errors.As(err, &perm) {
		t.Fatalf("ошибка %T %v, ждали *ErrPermanent", err, err)
	}
	if !strings.Contains(perm.Msg, "invalid api key") {
		t.Errorf("причина API потеряна: %q", perm.Msg)
	}
	if rep.Translated != 0 {
		t.Errorf("переведено %d при 400, ждали 0", rep.Translated)
	}
	var n1, n2 int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM object_translations WHERE object_id = $1`, id1).Scan(&n1); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM object_translations WHERE object_id = $1`, id2).Scan(&n2); err != nil {
		t.Fatal(err)
	}
	if n1 != 0 || n2 != 0 {
		t.Errorf("строк %d/%d при 400, ждали 0/0 (прогон остановлен)", n1, n2)
	}
}

// TestLiveTranslateRateLimit — 429: пауза retry_after, повтор, прогон
// завершается УСПЕШНО (мягкий останов срабатывает только при исчерпании).
func TestLiveTranslateRateLimit(t *testing.T) {
	zeroLLMRetries(t)
	pool := translatePool(t)
	ctx := context.Background()

	id := insertQQObject(t, pool, qqDescCS, "cs")
	var slept []time.Duration
	srv, n := mockLLM(t, func(w http.ResponseWriter, r *http.Request, i int) {
		if i == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		echoLLM(w, r)
	})
	cl := llmClient(srv.URL, func(d time.Duration) { slept = append(slept, d) })
	rep, err := Run(ctx, pool, cl, NewDetector(), 4000, 0, "QQ")
	if err != nil {
		t.Fatalf("Run после 429+успех: %v", err)
	}
	if rep.Translated != 2 || *n != 3 {
		t.Fatalf("отчёт %+v, запросов %d, ждали translated=2 и 3 запроса (429 + 2)", rep, *n)
	}
	if len(slept) != 1 || slept[0] != time.Second {
		t.Errorf("паузы %v, ждали [1s] (Retry-After: 1)", slept)
	}
	var nRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM object_translations WHERE object_id = $1`, id).Scan(&nRows); err != nil {
		t.Fatal(err)
	}
	if nRows != 2 {
		t.Errorf("строк %d, ждали 2", nRows)
	}
}

// TestLiveTranslateLimit — limit=1: за прогон один объект (по id),
// остальные остаются кандидатами для следующего прогона.
func TestLiveTranslateLimit(t *testing.T) {
	zeroLLMRetries(t)
	pool := translatePool(t)
	ctx := context.Background()

	id1 := insertQQObject(t, pool, qqDescCS, "cs")
	id2 := insertQQObject(t, pool, qqDescCSnew, "cs")
	if id1 > id2 {
		id1, id2 = id2, id1
	}
	srv, n := mockLLM(t, func(w http.ResponseWriter, r *http.Request, i int) { echoLLM(w, r) })
	rep, err := Run(ctx, pool, llmClient(srv.URL, nil), NewDetector(), 4000, 1, "QQ")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Objects != 1 || rep.Translated != 2 || *n != 2 {
		t.Fatalf("отчёт %+v, запросов %d, ждали objects=1 translated=2", rep, *n)
	}
	var n1, n2 int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM object_translations WHERE object_id = $1`, id1).Scan(&n1); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM object_translations WHERE object_id = $1`, id2).Scan(&n2); err != nil {
		t.Fatal(err)
	}
	if n1 != 2 || n2 != 0 {
		t.Errorf("строк %d/%d, ждали 2/0 (первый по id за лимит, второй — следующий прогон)", n1, n2)
	}
}
