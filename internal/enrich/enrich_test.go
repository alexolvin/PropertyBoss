// Тесты enrich: чистая логика (правило типа продавца, названия стран,
// классификация делистинга, вердикт контрольных — C7). DB-часть
// (Run/saveObject) — live-проверкой на реальных страницах;
// TestLiveEnrichDelisting — live-тест пути делистинга (PB_TEST_DSN).
package enrich

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/attrules"
	"propertyboss/internal/config"
	"propertyboss/internal/connectors/bazos"
	"propertyboss/internal/db"
)

// testSellerRules — маркеры из config.example.yaml (то же, что в проде).
func testSellerRules() config.SellerRulesConf {
	return config.SellerRulesConf{
		AgencyMarkers: []string{
			"realitní kancelář", "realitni kancelar", "realitace",
			"s.r.o.", "s. r. o.", "a.s.", "společnost",
		},
		PrivateMarkers: []string{
			"přímý majitel", "prímý majitel", "jsem majitel", "jsem přímý",
			"realitní kanceláře nevolat", "bez realitky", "přímý prodej",
		},
		DeveloperMarkers:    []string{"stavební společnost", "developer"},
		ConfidenceAgency:    0.85,
		ConfidencePrivate:   0.90,
		ConfidenceDeveloper: 0.85,
	}
}

func TestClassifySeller(t *testing.T) {
	sr := testSellerRules()
	cases := []struct {
		name, seller, desc, want string
		conf                     float64
	}{
		{"agency: бренд в имени",
			"Mgr. Veronika Hradilová (Realitní kancelář - pobočka Praha)", "",
			"agency", 0.85},
		{"agency: юридическая форма",
			"ABCD realitace s.r.o.", "",
			"agency", 0.85},
		{"private: заявление в описании, агентство НЕ ищем в описании",
			"Petr Novák", "REALITNÍ kanceláře NEVOLAT!!!!!",
			"private", 0.90},
		{"private: заявление в имени",
			"Jsem majitel, prodávám přímo", "",
			"private", 0.90},
		{"developer: конкретное маркер бьёт общее (společnost → не agency)",
			"Stavební společnost XYZ a.s.", "",
			"developer", 0.85},
		{"developer: в описании",
			"DV s.r.o.", "Prodává developer, nové výstavby",
			"developer", 0.85},
		{"agency: společnost без stavební",
			"Společnost ABC a.s.", "",
			"agency", 0.85},
		{"без маркеров → NULL",
			"Jan Svoboda", "krásný byt s lodžií, sluneční",
			"", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, conf := classifySeller(sr, tc.seller, tc.desc)
			if got != tc.want || (tc.want != "" && conf != tc.conf) {
				t.Errorf("classifySeller(%q, %q) = %q/%.2f, ожидалось %q/%.2f",
					tc.seller, tc.desc, got, conf, tc.want, tc.conf)
			}
		})
	}
}

func TestClassifySellerEmptyMarkers(t *testing.T) {
	// Секция не сконфигурирована — ни один тип не устанавливается
	// (NULL, догадка запрещена).
	if typ, conf := classifySeller(config.SellerRulesConf{}, "Realitní kancelář X", "jsem majitel"); typ != "" || conf != 0 {
		t.Errorf("пустой конфиг: %q/%.2f, ожидалось \"\"/0", typ, conf)
	}
}

func TestCountryName(t *testing.T) {
	cases := map[string]string{
		"CZ": "Czechia", "IT": "Italy", "NL": "Netherlands",
		"DE": "DE", // неизвестный код — как есть
	}
	for code, want := range cases {
		if got := countryName(code); got != want {
			t.Errorf("countryName(%q) = %q, ожидалось %q", code, got, want)
		}
	}
}

func TestExtractConfigOf(t *testing.T) {
	if extractConfigOf(&config.Config{}) != nil {
		t.Fatal("пустой конфиг должен пропускать LLM")
	}
	cfg := &config.Config{}
	cfg.Extract.APIKey = "k"
	cfg.Extract.Model = "qwen3.8-27b"
	cfg.Extract.BaseURL = "http://127.0.0.1:9/v1"
	cfg.Extract.MaxConfidence = 0.9
	cfg.Extract.AttrListVersion = "cz-bazos-v1"
	got := extractConfigOf(cfg)
	if got == nil || got.Model != "qwen3.8-27b" || got.MinedAutoPromote || got.ShadowSamplePct != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestSha256Hex(t *testing.T) {
	// Известное значение (sha256("abc")).
	if got := sha256Hex("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("sha256Hex(abc) = %q", got)
	}
}

// TestDelistReason — классификация ошибки детального fetch'а (ТЗ §8.2,
// та же семантика, что urlCheck delist-пасса): gone → 'unknown',
// relisted → 'relisted', прочие ошибки (429, layout change, сеть) → ""
// (не делистинг, объект повторится).
func TestDelistReason(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"gone прямая", bazos.ErrListingGone, "unknown"},
		{"gone в обёртке", fmt.Errorf("bazos: detail: %w: http://x → http://y", bazos.ErrListingGone), "unknown"},
		{"relisted в обёртке", fmt.Errorf("%w: http://a → http://b", bazos.ErrListingRelisted), "relisted"},
		{"HTTP 429 — не делистинг (D13)", &bazos.RateLimitError{URL: "http://x", RetryAfter: time.Minute}, ""},
		{"layout change — не делистинг", fmt.Errorf("bazos: детальная страница: заголовок (nadpisdetail) не найден — вероятно, изменена верстка (layout change)"), ""},
		{"сеть — не делистинг", fmt.Errorf("bazos: detail: http://x: context deadline exceeded"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := delistReason(tc.err); got != tc.want {
				t.Errorf("delistReason(%v) = %q, ожидалось %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestDelistGuardFires — D18: единое основание гварда (delisted и доля
// — от верифицированных в раунде), проективное +1 только внутри
// сравнения, минимальный знаменатель раунда.
func TestDelistGuardFires(t *testing.T) {
	cases := []struct {
		name     string
		delisted int
		verified int
		max      float64
		min      int
		want     bool
	}{
		{"раунд ниже минимального N — гвард молчит", 0, 1, 25, 30, false},
		{"раунд ниже минимального N при 100% кандидатов", 7, 8, 25, 30, false},
		{"N=1 — гвард жив (100*1/1 = 100 > 25)", 0, 1, 25, 1, true},
		{"ровно минимальный N, порог превышен", 10, 30, 25, 30, true},         // 100*11/30 ≈ 36.7 > 25
		{"ровно на пороге — не срабатывает (строго >)", 2, 30, 10, 30, false}, // 100*3/30 = 10, не > 10
		{"чуть выше порога", 3, 30, 10, 30, true},                             // 100*4/30 ≈ 13.3 > 10
		{"всё исчезло, N выполнен", 29, 30, 25, 30, true},                     // 100*30/30 = 100 > 25
		{"нулевой раунд — деления нет", 0, 0, 25, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := delistGuardFires(tc.delisted, tc.verified, tc.max, tc.min); got != tc.want {
				t.Errorf("delistGuardFires(delisted=%d, verified=%d, max=%v, min=%d) = %v, ожидалось %v",
					tc.delisted, tc.verified, tc.max, tc.min, got, tc.want)
			}
		})
	}
}

// TestProcessObjectRateLimited — 429 (ТЗ §10.4, D13): стоп-сигнал
// прогона, а не ошибка объекта. processObject возвращает его как есть
// (вызывающий Run переводит источник в cooldown и останавливает прогон),
// НЕ инкрементируя rep.Failed.
func TestProcessObjectRateLimited(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()

	det := bazos.NewDetailClient(10 * time.Second)
	c := candidate{id: 1, country: "CZ", sourceID: "reality_bazos", url: ts.URL + "/inzerat/1/x.php"}
	rep := &Report{}

	err := processObject(context.Background(), nil, &config.Config{}, det, nil, map[string]bool{}, nil, c, true, rep)
	if !errors.Is(err, bazos.ErrRateLimited) {
		t.Fatalf("ожидалось распространение ErrRateLimited, получено %v", err)
	}
	if rep.Failed != 0 {
		t.Fatalf("429 не ошибка объекта, Failed=%d", rep.Failed)
	}
}

// TestProcessObjectOtherFetchErr — прочие fetch-ошибки (403/5xx/сеть)
// остаются «повторить позже»: считаются в rep.Failed, прогон не
// прерывается (processObject возвращает nil).
func TestProcessObjectOtherFetchErr(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer ts.Close()

	det := bazos.NewDetailClient(10 * time.Second)
	c := candidate{id: 1, country: "CZ", sourceID: "reality_bazos", url: ts.URL + "/inzerat/1/x.php"}
	rep := &Report{}

	if err := processObject(context.Background(), nil, &config.Config{}, det, nil, map[string]bool{}, nil, c, true, rep); err != nil {
		t.Fatalf("403 не должен прерывать прогон: %v", err)
	}
	if rep.Failed != 1 {
		t.Fatalf("403 считается ошибкой объекта, Failed=%d", rep.Failed)
	}
}

// TestControlsVerdict — C7: вердикт прогона по контрольным. Живы не
// меньше половины (alive*2 >= n) — сбор здоров; n == 0 — верифицировать
// нечем (fallback на гвард доли).
func TestControlsVerdict(t *testing.T) {
	cases := []struct {
		n, alive         int
		ok, unverifiable bool
	}{
		{0, 0, false, true}, // контрольных нет — fallback
		{1, 1, true, false},
		{1, 0, false, false},
		{2, 2, true, false},
		{2, 1, true, false}, // половина живых — прогон
		{2, 0, false, false},
		{3, 2, true, false},
		{3, 1, false, false},
		{5, 5, true, false},
		{5, 3, true, false},
		{5, 2, false, false},
		{5, 0, false, false},
	}
	for _, tc := range cases {
		ok, unv := controlsVerdict(tc.n, tc.alive)
		if ok != tc.ok || unv != tc.unverifiable {
			t.Errorf("controlsVerdict(%d, %d) = %v/%v, ожидалось %v/%v",
				tc.n, tc.alive, ok, unv, tc.ok, tc.unverifiable)
		}
	}
}

// TestClassifyControlFetch — C7: классификация ответа на контрольный
// fetch теми же классами, что и пул (тот же bazos.DetailClient).
func TestClassifyControlFetch(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"живая страница", nil, controlAlive},
		{"gone прямая", bazos.ErrListingGone, controlDead},
		{"gone в обёртке", fmt.Errorf("bazos: detail: %w: http://x → http://y", bazos.ErrListingGone), controlDead},
		{"relisted в обёртке", fmt.Errorf("%w: http://a → http://b", bazos.ErrListingRelisted), controlDead},
		{"HTTP 429", &bazos.RateLimitError{URL: "http://x"}, controlRateLimited},
		{"403 — неконклюзивно", fmt.Errorf("bazos: detail: http://x: 403"), controlRetry},
		{"сеть — неконклюзивно", fmt.Errorf("context deadline exceeded"), controlRetry},
	}
	for _, tc := range cases {
		if got := classifyControlFetch(tc.err); got != tc.want {
			t.Errorf("%s: classifyControlFetch = %d, ожидалось %d", tc.name, got, tc.want)
		}
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

// TestLiveEnrichDelisting — C7+D18: делистинг по прямому свидетельству
// (301 на раздел) в трёх режимах, песочница VX:
//
//   - контрольные прошли (delistUnlimited=true): делистинг применяется
//     при доле 1/1 (100% > 25%) — прямое свидетельство не проходит
//     через гвард доли; Delisted=1, Failed=0 (результат, не ошибка);
//   - контрольной ссылки нет (delistUnlimited=false, fallback),
//     min_objects_for_share=1: гвард доли (ТЗ §8.2, защита №3)
//     срабатывает при 1/1 — объект остаётся active, Failed=1,
//     DelistAnomaly, уведомление в notifications; D18: в пейлоаде
//     единое основание (verified=1) и доля БЕЗ проективного +1
//     (0 delisted из 1 проверенного = 0%, а не 100%);
//   - контрольной ссылки нет (fallback), min_objects_for_share=30
//     (боевое значение): знаменатель раунда 1 < 30 — гвард не
//     вычисляется и не срабатывает, делистинг по прямому сигналу
//     применяется, аномалии и уведомления нет: «0 из 1 (100.0%)»
//     невозможно.
func TestLiveEnrichDelisting(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var notifIDBefore int64
	if err := pool.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM notifications`).Scan(&notifIDBefore); err != nil {
		t.Fatalf("notifications max(id): %v", err)
	}

	insObj := func() int64 {
		var id int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO objects
			    (country, deal_type, address, description_original, first_seen_at, last_seen_at)
			VALUES ('VX', 'sale', 'Test VX street 99', 'live test C7 delisting', now(), now())
			RETURNING id`).Scan(&id); err != nil {
			t.Fatalf("объект: %v", err)
		}
		return id
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cctx, `DELETE FROM objects WHERE country = 'VX'`)
		_, _ = pool.Exec(cctx, `DELETE FROM notifications WHERE id > $1 AND kind IN ('enrich_delist_anomaly', 'enrich_collection_failure')`, notifIDBefore)
	})

	// Удалённое объявление: 301 на раздел (реальный паттерн bazos, D11).
	mux := http.NewServeMux()
	mux.HandleFunc("/inzerat/2/gone.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/inzeraty/prodej-bytu/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/inzeraty/prodej-bytu/", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "<html><body>inzerce</body></html>")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	det := bazos.NewDetailClient(10 * time.Second)
	cfg := &config.Config{}
	cfg.Delist.MaxDelistedSharePct = 25
	cfg.Delist.MinObjectsForShare = 1 // режим 2: гвард на знаменателе 1
	candURL := srv.URL + "/inzerat/2/gone.php"

	// Режим 1: контрольные прошли — без ограничения доли.
	id1 := insObj()
	rep1 := &Report{Objects: 1}
	if err := processObject(ctx, pool, cfg, det, nil, map[string]bool{}, nil,
		candidate{id: id1, country: "VX", sourceID: "vz-test", url: candURL}, true, rep1); err != nil {
		t.Fatalf("unlimited: %v", err)
	}
	if rep1.Delisted != 1 || rep1.Failed != 0 {
		t.Fatalf("unlimited: Delisted=%d Failed=%d, ожидалось 1/0 (прямое свидетельство — результат, не ошибка)", rep1.Delisted, rep1.Failed)
	}
	var status, reason string
	if err := pool.QueryRow(ctx, `SELECT status, coalesce(delisted_reason, '') FROM objects WHERE id = $1`, id1).Scan(&status, &reason); err != nil {
		t.Fatalf("объект %d: %v", id1, err)
	}
	if status != "delisted" || reason != "unknown" {
		t.Fatalf("unlimited: объект %s/%s, ожидалось delisted/unknown", status, reason)
	}

	// Режим 2: контрольной ссылки нет, min_objects_for_share=1 —
	// fallback на гвард доли: срабатывает на 1/1.
	id2 := insObj()
	rep2 := &Report{Objects: 1}
	if err := processObject(ctx, pool, cfg, det, nil, map[string]bool{}, nil,
		candidate{id: id2, country: "VX", sourceID: "vz-test", url: candURL}, false, rep2); err != nil {
		t.Fatalf("fallback: %v", err)
	}
	if rep2.Delisted != 0 || rep2.Failed != 1 || !rep2.DelistAnomaly || rep2.Verified != 1 {
		t.Fatalf("fallback: Delisted=%d Failed=%d Anomaly=%v Verified=%d, ожидалось 0/1/true/1 (гвард 1/1 > 25%%)", rep2.Delisted, rep2.Failed, rep2.DelistAnomaly, rep2.Verified)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM objects WHERE id = $1`, id2).Scan(&status); err != nil {
		t.Fatalf("объект %d: %v", id2, err)
	}
	if status != "active" {
		t.Fatalf("fallback: объект %s, ожидалось active (гвард не применил delisting)", status)
	}
	var notices int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE id > $1 AND kind = 'enrich_delist_anomaly'`, notifIDBefore).Scan(&notices); err != nil {
		t.Fatalf("notifications: %v", err)
	}
	if notices != 1 {
		t.Fatalf("fallback: уведомлений об аномалии %d, ожидалось 1", notices)
	}
	// D18: единое основание в пейлоаде (verified) и доля БЕЗ
	// проективного +1: 0 delisted из 1 проверенного = 0%, а не 100%.
	var (
		pVerified, pDelisted int64
		pShare               float64
	)
	if err := pool.QueryRow(ctx, `
		SELECT (payload->>'verified')::int8, (payload->>'delisted')::int8, (payload->>'share_pct')::float8
		FROM notifications WHERE id > $1 AND kind = 'enrich_delist_anomaly'
		ORDER BY id DESC LIMIT 1`, notifIDBefore).Scan(&pVerified, &pDelisted, &pShare); err != nil {
		t.Fatalf("пейлоад уведомления: %v", err)
	}
	if pVerified != 1 || pDelisted != 0 || pShare != 0 {
		t.Fatalf("пейлоад D18: verified=%d delisted=%d share=%.1f, ожидалось 1/0/0 (без проективного +1)", pVerified, pDelisted, pShare)
	}

	// Режим 3 (D18): боевое min_objects_for_share=30, знаменатель
	// раунда 1 < 30 — гвард доли не вычисляется и не срабатывает:
	// делистинг по прямому сигналу применяется, аномалии и
	// уведомления нет — «0 из 1 (100.0%)» невозможно.
	id3 := insObj()
	cfg3 := *cfg
	cfg3.Delist.MinObjectsForShare = 30
	rep3 := &Report{Objects: 1}
	if err := processObject(ctx, pool, &cfg3, det, nil, map[string]bool{}, nil,
		candidate{id: id3, country: "VX", sourceID: "vz-test", url: candURL}, false, rep3); err != nil {
		t.Fatalf("min-N: %v", err)
	}
	if rep3.Delisted != 1 || rep3.Failed != 0 || rep3.DelistAnomaly {
		t.Fatalf("min-N: Delisted=%d Failed=%d Anomaly=%v, ожидалось 1/0/false (раунд 1 < минимального N 30)", rep3.Delisted, rep3.Failed, rep3.DelistAnomaly)
	}
	if err := pool.QueryRow(ctx, `SELECT status, coalesce(delisted_reason, '') FROM objects WHERE id = $1`, id3).Scan(&status, &reason); err != nil {
		t.Fatalf("объект %d: %v", id3, err)
	}
	if status != "delisted" || reason != "unknown" {
		t.Fatalf("min-N: объект %s/%s, ожидалось delisted/unknown", status, reason)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE id > $1 AND kind = 'enrich_delist_anomaly'`, notifIDBefore).Scan(&notices); err != nil {
		t.Fatalf("notifications: %v", err)
	}
	if notices != 1 {
		t.Fatalf("min-N: уведомлений об аномалии %d, ожидалось 1 (режим 2) — малый раунд уведомления не создаёт", notices)
	}
}

// TestLiveEnrichTemplateArea — C6: enrich-путь — шаблонный слой
// заполняет зеркало area_sqm, когда Derive() площади не дал (нет блока
// «Plocha», и разметочная форма «N m²» ни в заголовке, ни в описании —
// в тексте форма «65 čtverečních metrů», которую reAreaM2 не ловит, а
// шаблон — да). Markup-значение при появлении детали позже перетирает
// (COALESCE) — здесь проверяется первичное заполнение: колонка +
// attributes + provenance rule_text.
func TestLiveEnrichTemplateArea(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// Песочница 'VX': старт с чистого листа (остатки прерванного прогона).
	sweep := func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cctx, `DELETE FROM objects WHERE country = 'VX'`)
		_, _ = pool.Exec(cctx, `DELETE FROM attribute_templates WHERE country = 'VX'`)
		_, _ = pool.Exec(cctx, `DELETE FROM attribute_registry WHERE country = 'VX'`)
	}
	sweep()
	t.Cleanup(sweep)

	if _, err := pool.Exec(ctx, `
INSERT INTO attribute_registry
    (country, key, data_type, used_in_pricing, label_ru, label_en, source_evidence, value_min, value_max)
VALUES ('VX', 'area_sqm', 'float', false, 'vx', 'vx', 'live test', 1, 5000)`); err != nil {
		t.Fatalf("registry: %v", err)
	}
	// Те же паттерны, что seed в миграции 0031 (std + čtvereční).
	for _, pat := range []string{
		`(?P<val>[0-9][0-9\s]*(?:[,.][0-9]+)?)\s*(?:m[²2]|mkv\b|metry kvadratick\p{L}+)`,
		`(?P<val>[0-9][0-9\s]*(?:[,.][0-9]+)?)\s*(?:m\p{L}*\s*)?čtverečn\p{L}+`,
	} {
		if _, err := pool.Exec(ctx, `
INSERT INTO attribute_templates
    (country, attr_key, pattern_type, pattern, value, status, confidence, source)
VALUES ('VX', 'area_sqm', 'regex', $1, NULL, 'active', 0.80, 'seed')`, pat); err != nil {
			t.Fatalf("template: %v", err)
		}
	}

	var id int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO objects
		    (country, deal_type, address, description_original, first_seen_at, last_seen_at)
		VALUES ('VX', 'sale', 'Test VX street 98', 'live test C6 area', now(), now())
		RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("объект: %v", err)
	}

	// Детальная страница: без блока «Plocha», без «N m²» в заголовке;
	// описание — форма, которую разметка не берёт, а шаблон — берёт.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `<html><body>`+
			`<H1 class="nadpisdetail">Prodej bytu, Praha 6</H1>`+
			`<div class="popisdetail">Slunečný byt. Užitná plocha 65 čtverečních metrů, k dispozici sklep.</div>`+
			`</body></html>`)
	}))
	defer srv.Close()

	det := bazos.NewDetailClient(10 * time.Second)
	eng, err := attrules.Load(ctx, pool, "VX")
	if err != nil {
		t.Fatalf("attrules.Load: %v", err)
	}
	if eng.Len() != 2 {
		t.Fatalf("eng.Len() = %d, want 2", eng.Len())
	}

	cfg := &config.Config{}
	rep := &Report{Objects: 1}
	if err := processObject(ctx, pool, cfg, det, nil,
		map[string]bool{"area_sqm": true}, eng,
		candidate{id: id, country: "VX", sourceID: "vz-test", url: srv.URL + "/inzerat/7/c6.php"},
		true, rep); err != nil {
		t.Fatalf("processObject: %v", err)
	}
	if rep.Attrs != 1 {
		t.Fatalf("rep.Attrs = %d, want 1 (только шаблонный хит area_sqm)", rep.Attrs)
	}

	var (
		area *float64
		has  bool
	)
	if err := pool.QueryRow(ctx, `
SELECT o.area_sqm, o.attributes ? 'area_sqm'
FROM objects o WHERE o.id = $1`, id).Scan(&area, &has); err != nil {
		t.Fatalf("объект: %v", err)
	}
	if area == nil || *area != 65 || !has {
		t.Fatalf("area_sqm=%v attrs?area=%v, want 65/true (шаблонное зеркало)", area, has)
	}
	var (
		prov string
		conf float64
	)
	if err := pool.QueryRow(ctx, `
SELECT extracted_by, confidence FROM attribute_values
WHERE object_id = $1 AND attr_key = 'area_sqm'`, id).Scan(&prov, &conf); err != nil {
		t.Fatalf("attribute_values: %v", err)
	}
	if prov != "rule_text" || conf != 0.80 {
		t.Fatalf("provenance = %s/%v, want rule_text/0.80", prov, conf)
	}
}
