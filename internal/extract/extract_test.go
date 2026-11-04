package extract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/db"
)

func pf(v float64) *float64 { return &v }

// --- parseResponse ---

func TestParseResponsePlain(t *testing.T) {
	got, err := parseResponse(`{"rooms":{"value":2,"confidence":0.9}}`)
	if err != nil {
		t.Fatalf("plain: %v", err)
	}
	e := got["rooms"]
	if e == nil || e.Value == nil || e.Confidence == nil || *e.Confidence != 0.9 {
		t.Fatalf("rooms: %+v", e)
	}
}

func TestParseResponseFenced(t *testing.T) {
	// markdown-ограды: модели оборачивают JSON в fence — сам JSON валиден.
	got, err := parseResponse("```json\n{\"rooms\":{\"value\":3,\"confidence\":0.8}}\n```")
	if err != nil {
		t.Fatalf("fenced: %v", err)
	}
	if len(got) != 1 || got["rooms"] == nil {
		t.Fatalf("fenced: %+v", got)
	}
}

func TestParseResponseBad(t *testing.T) {
	if _, err := parseResponse("not a json"); err == nil {
		t.Fatal("bad: ожидалась ошибка")
	}
}

func TestParseResponseThinkPrefix(t *testing.T) {
	got, err := parseResponse("<think>reason</think>\n{\"rooms\":{\"value\":2,\"confidence\":0.9}}")
	if err != nil {
		t.Fatalf("think: %v", err)
	}
	if got["rooms"] == nil {
		t.Fatalf("think: %+v", got)
	}
}

// --- validateAttrs ---

func testSpecs() map[string]attrSpec {
	return map[string]attrSpec{
		"rooms":      {Key: "rooms", DataType: "int", Min: pf(0), Max: pf(30)},
		"area_sqm":   {Key: "area_sqm", DataType: "float", Min: pf(1), Max: pf(5000)},
		"total_area": {Key: "total_area", DataType: "float", Min: pf(1), Max: pf(5000)},
		"condition":  {Key: "condition", DataType: "enum", Values: []string{"old", "good"}},
		"has_garden": {Key: "has_garden", DataType: "bool"},
		"year_built": {Key: "year_built", DataType: "int", Min: pf(1800), Max: pf(2026)},
	}
}

func val(key string, v any, conf float64) *extractedAttr {
	c := conf
	return &extractedAttr{Value: v, Confidence: &c}
}

func TestValidateAttrs(t *testing.T) {
	cases := []struct {
		name        string
		got         map[string]*extractedAttr
		known       map[string]any
		maxConf     float64
		wantKeys    []string // ключи, которые должны остаться
		wantDropped int
	}{
		{
			name:     "int valid",
			got:      map[string]*extractedAttr{"rooms": val("rooms", float64(2), 0.9)},
			wantKeys: []string{"rooms"},
		},
		{
			name:     "int string number",
			got:      map[string]*extractedAttr{"rooms": val("rooms", "12", 0.8)},
			wantKeys: []string{"rooms"},
		},
		{
			name:        "int out of range",
			got:         map[string]*extractedAttr{"rooms": val("rooms", float64(40), 0.9)},
			wantDropped: 1,
		},
		{
			name:        "int non-integer",
			got:         map[string]*extractedAttr{"rooms": val("rooms", 2.5, 0.9)},
			wantDropped: 1,
		},
		{
			name:     "enum case normalized",
			got:      map[string]*extractedAttr{"condition": val("condition", "GOOD", 0.7)},
			wantKeys: []string{"condition"},
		},
		{
			name:        "enum not allowed",
			got:         map[string]*extractedAttr{"condition": val("condition", "brand_new", 0.7)},
			wantDropped: 1,
		},
		{
			name:     "bool true",
			got:      map[string]*extractedAttr{"has_garden": val("has_garden", true, 0.8)},
			wantKeys: []string{"has_garden"},
		},
		{
			name:     "bool string false",
			got:      map[string]*extractedAttr{"has_garden": val("has_garden", "false", 0.8)},
			wantKeys: []string{"has_garden"},
		},
		{
			name:        "bool string yes",
			got:         map[string]*extractedAttr{"has_garden": val("has_garden", "yes", 0.8)},
			wantDropped: 1,
		},
		{
			name:        "no confidence",
			got:         map[string]*extractedAttr{"rooms": {Value: float64(2)}},
			wantDropped: 1,
		},
		{
			name:        "confidence > 1",
			got:         map[string]*extractedAttr{"rooms": val("rooms", 2, 1.5)},
			wantDropped: 1,
		},
		{
			name:        "unknown key",
			got:         map[string]*extractedAttr{"plot_area": val("plot_area", float64(100), 0.9)},
			wantDropped: 1,
		},
		{
			name:        "nil value",
			got:         map[string]*extractedAttr{"rooms": val("rooms", nil, 0.9)},
			wantDropped: 1,
		},
		{
			name: "area pair inconsistent in response",
			got: map[string]*extractedAttr{
				"area_sqm":   val("area_sqm", float64(100), 0.9),
				"total_area": val("total_area", float64(80), 0.9),
			},
			wantDropped: 2,
		},
		{
			name:     "area consistent with known",
			got:      map[string]*extractedAttr{"area_sqm": val("area_sqm", float64(100), 0.9)},
			known:    map[string]any{"total_area": float64(200)},
			wantKeys: []string{"area_sqm"},
		},
		{
			name:        "area inconsistent with known",
			got:         map[string]*extractedAttr{"area_sqm": val("area_sqm", float64(100), 0.9)},
			known:       map[string]any{"total_area": float64(80)},
			wantDropped: 1,
		},
		{
			name: "total_area from obytná quote is living area",
			got: map[string]*extractedAttr{
				"total_area": func() *extractedAttr {
					a := val("total_area", float64(57), 0.9)
					a.Quote = "má obytnou plochu 57 m2"
					a.Pattern = "obytnou plochu"
					return a
				}(),
			},
			known:       map[string]any{"area_sqm": float64(57)},
			wantDropped: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, dropped := validateAttrs(tc.got, testSpecs(), 0.9, tc.known)
			if len(out) != len(tc.wantKeys) {
				t.Fatalf("ключей: %d, ожидалось %d: %+v", len(out), len(tc.wantKeys), out)
			}
			if dropped != tc.wantDropped {
				t.Fatalf("отброшено: %d, ожидалось %d", dropped, tc.wantDropped)
			}
			for _, v := range out {
				found := false
				for _, k := range tc.wantKeys {
					if v.Key == k {
						found = true
					}
				}
				if !found {
					t.Fatalf("неожиданный ключ %q", v.Key)
				}
			}
		})
	}
}

func TestValidateConfidenceCapped(t *testing.T) {
	out, _ := validateAttrs(
		map[string]*extractedAttr{"rooms": val("rooms", float64(2), 0.95)},
		testSpecs(), 0.5, nil)
	if len(out) != 1 || out[0].Confidence != 0.5 {
		t.Fatalf("потолок не применён: %+v", out)
	}
}

func TestNormValueIntType(t *testing.T) {
	v, ok := normValue(float64(1965), attrSpec{DataType: "int", Min: pf(1800), Max: pf(2026)})
	if !ok {
		t.Fatal("1965 провалило")
	}
	if _, isInt64 := v.(int64); !isInt64 {
		t.Fatalf("тип: %T", v)
	}
}

// --- promptAttrs ---

func TestPromptAttrs(t *testing.T) {
	s := promptAttrs([]attrSpec{
		{Key: "rooms", DataType: "int", Min: pf(0), Max: pf(30)},
		{Key: "condition", DataType: "enum", Values: []string{"old", "good"}},
	})
	for _, want := range []string{
		"- rooms (int); min: 0; max: 30",
		"- condition (enum); allowed values: old, good",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("промпт без %q:\n%s", want, s)
		}
	}
	s2 := promptAttrs([]attrSpec{
		{Key: "total_area", DataType: "float", Label: "Total area, m²", Min: pf(1), Max: pf(5000)},
	})
	if !strings.Contains(s2, "- total_area (float): Total area, m²; min: 1; max: 5000") {
		t.Fatalf("label: %s", s2)
	}
}

func TestPromptKnownAreas(t *testing.T) {
	got := promptKnownAreas(map[string]any{"area_sqm": 57.0, "plot_area": 674, "rooms": 3})
	if !strings.Contains(got, "area_sqm = 57") || !strings.Contains(got, "plot_area = 674") {
		t.Fatalf("known areas: %s", got)
	}
	if strings.Contains(got, "rooms") {
		t.Fatalf("rooms не площадь: %s", got)
	}
	p := buildUserPrompt("má obytnou plochu 57 m2", []attrSpec{{Key: "total_area", DataType: "float", Label: "Total area, m²"}}, map[string]any{"area_sqm": 57.0})
	if !strings.Contains(p, "Already known area values") || !strings.Contains(p, "area_sqm = 57") {
		t.Fatalf("user prompt: %s", p)
	}
	if !strings.Contains(systemPrompt, "Never copy obytná/užitná into total_area") {
		t.Fatal("systemPrompt без различия площадей")
	}
}

// --- Client ---

func TestClientPermanent(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid key"}}`)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, APIKey: "bad", Model: "m", Sleep: func(time.Duration) {}}
	_, _, _, err := c.Complete(context.Background(), "s", "u")
	var perm *ErrPermanent
	if !errors.As(err, &perm) {
		t.Fatalf("ожидался ErrPermanent, получено %v", err)
	}
	if n != 1 {
		t.Fatalf("4xx не должен ретраиться: запросов %d", n)
	}
}

func TestClientRetriesThenSuccess(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n < 3 {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, `{"error":{"message":"upstream"}}`)
			return
		}
		fmt.Fprint(w, `{"model":"m-2","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"total_tokens":42}}`)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, APIKey: "k", Model: "m", Sleep: func(time.Duration) {}}
	text, model, tokens, err := c.Complete(context.Background(), "s", "u")
	if err != nil {
		t.Fatalf("после ретраев: %v", err)
	}
	if text != "ok" || model != "m-2" || tokens != 42 {
		t.Fatalf("результат: %q %q %d", text, model, tokens)
	}
	if n != 3 {
		t.Fatalf("запросов: %d, ожидалось 3", n)
	}
}

func TestClient429(t *testing.T) {
	var n int
	var slept time.Duration
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"total_tokens":7}}`)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, APIKey: "k", Model: "m", Sleep: func(d time.Duration) { slept = d }}
	_, _, tokens, err := c.Complete(context.Background(), "s", "u")
	if err != nil || tokens != 7 {
		t.Fatalf("429: %v tokens=%d", err, tokens)
	}
	if n != 2 || slept != time.Second {
		t.Fatalf("retry-after: запросов %d, пауза %v", n, slept)
	}
}

func TestClientRequestShape(t *testing.T) {
	var req chatReq
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&req)
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"total_tokens":1}}`)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, APIKey: "secret", Model: "m", Sleep: func(time.Duration) {}}
	if _, _, _, err := c.Complete(context.Background(), "sys-prompt", "user-prompt"); err != nil {
		t.Fatal(err)
	}
	if req.Model != "m" || req.Temperature != 0 {
		t.Fatalf("тело: model=%q temp=%v", req.Model, req.Temperature)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[0].Content != "sys-prompt" ||
		req.Messages[1].Role != "user" || req.Messages[1].Content != "user-prompt" {
		t.Fatalf("сообщения: %+v", req.Messages)
	}
	if auth != "Bearer secret" {
		t.Fatalf("Authorization: %q", auth)
	}
}

// --- Live-тест (PB_TEST_DSN) ---

func testPool(t *testing.T) *pgxpool.Pool {
	dsn := os.Getenv("PB_TEST_DSN")
	if dsn == "" {
		t.Skip("PB_TEST_DSN не задан — live-тест пропускается")
	}
	t.Helper()
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	unlock, err := db.LiveTestLock(context.Background(), pool)
	if err != nil {
		t.Fatalf("live lock: %v", err)
	}
	t.Cleanup(pool.Close)
	t.Cleanup(unlock)
	return pool
}

// TestLiveRun — полный прогон по песочной стране VX: кандидаты,
// промпт, вызов mock-LLM, валидация по реестру, запись, идемпотентность,
// «слишком длинное», бюджет.
func TestLiveRun(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// Реестр страны-песочницы VX (не пересекаться с другими пакетами).
	regRows := []struct {
		key, dt, allowed string
		min, max         *float64
	}{
		{"condition", "enum", `["old","good"]`, nil, nil},
		{"has_garden", "bool", "null", nil, nil},
		{"rooms", "int", "null", pf(0), pf(30)},
	}
	for _, r := range regRows {
		if _, err := pool.Exec(ctx, `
			INSERT INTO attribute_registry
			    (country, key, data_type, allowed_values, label_ru, label_en, source_evidence, value_min, value_max)
			VALUES ('VX', $1, $2, $3::jsonb, 'тест', 'test', 'test', $4, $5)`,
			r.key, r.dt, r.allowed, r.min, r.max); err != nil {
			t.Fatalf("реестр %s: %v", r.key, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM attribute_registry WHERE country = 'VX'`)
	})

	insObj := func(desc string) int64 {
		var id int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO objects
			    (country, deal_type, address, description_original, detail_fetched_at, first_seen_at, last_seen_at)
			VALUES ('VX', 'sale', 'Test VX street 1', $1, now(), now(), now())
			RETURNING id`, desc).Scan(&id); err != nil {
			t.Fatalf("объект: %v", err)
		}
		return id
	}
	objA := insObj("Test listing: 2 rooms, garden, good condition.")
	objB := insObj("BADJSON listing text")
	objC := insObj(strings.Repeat("x", 5000))
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM objects WHERE id = ANY($1)`, []int64{objA, objB, objC})
	})

	// Mock-LLM: нормальный JSON-ответ; на описании с BADJSON — не JSON.
	modelResp := `{"rooms":{"value":2,"confidence":0.9},"has_garden":{"value":true,"confidence":0.8},"condition":{"value":"GOOD","confidence":0.7},"plot_area":{"value":1,"confidence":0.9}}`
	respBody, _ := json.Marshal(modelResp)
	okBody := `{"model":"mock-model","choices":[{"message":{"role":"assistant","content":` + string(respBody) + `}}],"usage":{"total_tokens":100}}`
	badContent, _ := json.Marshal("not a json")
	badBody := `{"model":"mock-model","choices":[{"message":{"role":"assistant","content":` + string(badContent) + `}}],"usage":{"total_tokens":100}}`

	var (
		mu       sync.Mutex
		calls    int
		lastUser string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		var req chatReq
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			for _, m := range req.Messages {
				if m.Role == "user" {
					lastUser = m.Content
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(lastUser, "BADJSON") {
			fmt.Fprint(w, badBody)
			return
		}
		fmt.Fprint(w, okBody)
	}))
	defer srv.Close()

	cfg := &Config{
		BaseURL:         srv.URL,
		APIKey:          "test-key",
		Model:           "mock-model",
		TimeoutSec:      5,
		MaxConfidence:   0.9,
		AttrListVersion: "v1",
		MaxChars:        4000,
	}

	// Фаза 1: A — значения записаны; B — ответ не JSON; C — слишком длинное.
	rep, err := Run(ctx, pool, cfg, 0, "VX")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.Candidates != 3 || rep.Extracted != 2 || rep.TooLong != 1 ||
		rep.ParseFailed != 1 || rep.Valid != 3 || rep.Dropped != 1 || rep.Tokens != 200 ||
		rep.BudgetHit {
		t.Fatalf("фаза 1: %+v", rep)
	}
	mu.Lock()
	if calls != 2 {
		t.Fatalf("вызовов к mock: %d, ожидалось 2", calls)
	}
	for _, want := range []string{
		"Listing text (original)",
		"- condition (enum): test; allowed values: old, good",
		"- has_garden (bool): test",
		"- rooms (int): test; min: 0; max: 30",
	} {
		if !strings.Contains(lastUser, want) {
			t.Fatalf("промпт без %q", want)
		}
	}
	mu.Unlock()

	// attributes объекта A: типизированные значения, plot_area (не в
	// реестре VX) отброшен.
	var attrsRaw []byte
	if err := pool.QueryRow(ctx, `SELECT attributes FROM objects WHERE id = $1`, objA).Scan(&attrsRaw); err != nil {
		t.Fatal(err)
	}
	var attrs map[string]any
	if err := json.Unmarshal(attrsRaw, &attrs); err != nil {
		t.Fatal(err)
	}
	if attrs["rooms"] != float64(2) || attrs["has_garden"] != true || attrs["condition"] != "good" {
		t.Fatalf("attributes: %v", attrs)
	}
	if _, has := attrs["plot_area"]; has {
		t.Fatalf("plot_area записан, хотя его нет в реестре VX: %v", attrs)
	}

	// Provenance: 3 строки, extracted_by=llm_extraction, модель и версия.
	rows, err := pool.Query(ctx, `
		SELECT attr_key, confidence, model, attr_list_version, extracted_by
		FROM attribute_values WHERE object_id = $1 ORDER BY attr_key`, objA)
	if err != nil {
		t.Fatal(err)
	}
	var prov []struct {
		key            string
		conf           float64
		model, ver, by string
	}
	for rows.Next() {
		var p struct {
			key            string
			conf           float64
			model, ver, by string
		}
		if err := rows.Scan(&p.key, &p.conf, &p.model, &p.ver, &p.by); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		prov = append(prov, p)
	}
	rows.Close()
	if len(prov) != 3 {
		t.Fatalf("провенанса: %d строк, ожидалось 3: %+v", len(prov), prov)
	}
	wantProv := map[string]float64{"condition": 0.7, "has_garden": 0.8, "rooms": 0.9}
	for _, p := range prov {
		if p.conf != wantProv[p.key] || p.model != "mock-model" || p.ver != "v1" || p.by != "llm_extraction" {
			t.Fatalf("провенанс: %+v", p)
		}
	}

	// Учёт вызовов: A и B (B — несмотря на не JSON-ответ: повтор
	// бесполезен, ядовитость исключена).
	var nExt int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM llm_extractions
		WHERE object_id IN ($1, $2) AND model = 'mock-model' AND attr_list_version = 'v1'`,
		objA, objB).Scan(&nExt); err != nil {
		t.Fatal(err)
	}
	if nExt != 2 {
		t.Fatalf("llm_extractions: %d строк, ожидалось 2", nExt)
	}

	// Фаза 2: A перестал кандидатом (атрибуты полны), B — идемпотентно
	// пропущен, C — снова слишком длинное (остаётся в очереди).
	rep2, err := Run(ctx, pool, cfg, 0, "VX")
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if rep2.Candidates != 2 || rep2.Skipped != 1 || rep2.TooLong != 1 ||
		rep2.Extracted != 0 || rep2.Tokens != 0 {
		t.Fatalf("фаза 2: %+v", rep2)
	}
	mu.Lock()
	if calls != 2 {
		t.Fatalf("фаза 2: новых вызовов к mock (всего %d)", calls)
	}
	mu.Unlock()

	// Фаза 3: бюджет: расход 200 ≥ 50 — прогон остановлен до кандидатов.
	cfg.BudgetTokens = 50
	rep3, err := Run(ctx, pool, cfg, 0, "VX")
	if err != nil {
		t.Fatalf("run 3: %v", err)
	}
	if !rep3.BudgetHit || rep3.Candidates != 0 || rep3.Extracted != 0 {
		t.Fatalf("фаза 3: %+v", rep3)
	}
}

// TestLiveProcessObject — шаг 3 на одном id (путь ulov-импорта).
func TestLiveProcessObject(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		INSERT INTO attribute_registry
		    (country, key, data_type, allowed_values, label_ru, label_en, source_evidence)
		VALUES ('VX', 'has_garden', 'bool', 'null'::jsonb, 'сад', 'garden', 'test')
		ON CONFLICT (country, key) DO NOTHING`); err != nil {
		t.Fatalf("реестр: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM attribute_registry WHERE country = 'VX' AND key = 'has_garden'`)
	})

	var id int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO objects
		    (country, deal_type, address, description_original, detail_fetched_at, first_seen_at, last_seen_at)
		VALUES ('VX', 'sale', 'ProcessObject street', 'Listing with a garden.', now(), now(), now())
		RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("объект: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM objects WHERE id = $1`, id)
	})

	body := `{"model":"mock-one","choices":[{"message":{"role":"assistant","content":"{\"has_garden\":{\"value\":true,\"confidence\":0.8,\"quote\":\"garden\",\"pattern\":\"garden\"}}"}}],"usage":{"total_tokens":12}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	cfg := &Config{
		BaseURL:         srv.URL,
		APIKey:          "test-key",
		Model:           "mock-one",
		TimeoutSec:      5,
		MaxConfidence:   0.9,
		AttrListVersion: "v-one",
		MaxChars:        4000,
	}
	rep, err := ProcessObject(ctx, pool, cfg, id)
	if err != nil {
		t.Fatalf("ProcessObject: %v", err)
	}
	if rep.Candidates != 1 || rep.Extracted != 1 || rep.Valid != 1 {
		t.Fatalf("отчёт: %+v", rep)
	}
	var has bool
	if err := pool.QueryRow(ctx, `SELECT (attributes->>'has_garden')::boolean FROM objects WHERE id = $1`, id).Scan(&has); err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("has_garden не записан")
	}
	rep2, err := ProcessObject(ctx, pool, cfg, id)
	if err != nil {
		t.Fatalf("ProcessObject 2: %v", err)
	}
	if rep2.Skipped != 1 || rep2.Extracted != 0 {
		t.Fatalf("идемпотентность: %+v", rep2)
	}
}
