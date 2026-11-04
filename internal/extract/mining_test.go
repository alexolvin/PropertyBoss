package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// TestLiveMining — мининг (ТЗ v4 §4.3, гибридное извлечение): LLM-успех с
// дословным доказательством (quote/pattern) становится mined-шаблоном в
// attribute_templates. Проверяется полный цикл на стране-песочнице VX:
//
//   - 3 разных объекта с фразой «s plynem» (каждый даёт одно значение
//     has_garden=true с цитатой) → шаблон «s plynem» набирает
//     occurrences=3 и на пороге (min=3) с нулевыми конфликтами
//     авто-промоутится candidate → active в конце прогона;
//   - 2 объекта с фразой «bez zahrady», но с РАЗНЫМИ значениями
//     (одна модель говорит true, другая false — маркеры ZTRUE/ZFALSE в
//     тексте переключают mock) → конфликт: другое значение при том же
//     паттерне → conflicts=1, status=conflict, авто-промоушн невозможен.
//
// Промоушн идёт по стране прогона (VX) — чужие страны не трогаются (иначе
// live-тест промоутил бы реальные mined-кандидаты). Песочница VX чистится до
// прогона, поэтому промоутится ровно «s plynem» (rep.Promoted=1).
// Доказательство промоушна — конкретный шаблон стал active (Mine создаёт
// только candidate/conflict, active ставит только Promote).
func TestLiveMining(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// До прогона чистим ВЕСЬ песочный контур страны VX (чтобы предыдущий
	// неудачный прогон не исказил счётчики) — чужие страны не трогаем.
	_, _ = pool.Exec(ctx, `DELETE FROM attribute_templates WHERE country = 'VX'`)
	_, _ = pool.Exec(ctx, `DELETE FROM attribute_values WHERE object_id IN (SELECT id FROM objects WHERE country = 'VX')`)
	_, _ = pool.Exec(ctx, `DELETE FROM llm_extractions WHERE object_id IN (SELECT id FROM objects WHERE country = 'VX')`)
	_, _ = pool.Exec(ctx, `DELETE FROM objects WHERE country = 'VX'`)
	_, _ = pool.Exec(ctx, `DELETE FROM attribute_registry WHERE country = 'VX'`)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM attribute_templates WHERE country = 'VX'`)
	})

	// Реестр VX: один bool-атрибут — его и извлекаем.
	if _, err := pool.Exec(ctx, `
		INSERT INTO attribute_registry
		    (country, key, data_type, allowed_values, label_ru, label_en, source_evidence, value_min, value_max)
		VALUES ('VX', 'has_garden', 'bool', 'null', 'тест', 'test', 'test', NULL, NULL)`); err != nil {
		t.Fatalf("реестр: %v", err)
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
	// 3 объекта с «s plynem» (без маркеров → mock вернёт s plynem=true).
	obj1 := insObj("byt s plynem, 2kk")
	obj2 := insObj("byt s plynem, 3kk")
	obj3 := insObj("byt s plynem, 1kk")
	// 2 объекта с «bez zahrady», но разные значения (маркеры ZTRUE/ZFALSE).
	obj4 := insObj("ZTRUE domek bez zahrady")
	obj5 := insObj("ZFALSE domek bez zahrady")
	objIDs := []int64{obj1, obj2, obj3, obj4, obj5}
	t.Cleanup(func() {
		ids := objIDs
		_, _ = pool.Exec(ctx, `DELETE FROM attribute_values WHERE object_id = ANY($1)`, ids)
		_, _ = pool.Exec(ctx, `DELETE FROM llm_extractions WHERE object_id = ANY($1)`, ids)
		_, _ = pool.Exec(ctx, `DELETE FROM objects WHERE id = ANY($1)`, ids)
	})

	// Mock-LLM: по маркеру в тексте возвращает has_garden с цитатой.
	//   ZFALSE  → false, quote «bez zahrady»
	//   ZTRUE   → true,  quote «bez zahrady»
	//   иначе   → true,  quote «s plynem»
	mkBody := func(inner string) string {
		innerJSON, _ := json.Marshal(inner)
		return `{"model":"mock-model","choices":[{"message":{"role":"assistant","content":` +
			string(innerJSON) + `}}],"usage":{"total_tokens":100}}`
	}
	plynBody := mkBody(`{"has_garden":{"value":true,"confidence":0.85,"quote":"s plynem","pattern":"s plynem"}}`)
	zTrueBody := mkBody(`{"has_garden":{"value":true,"confidence":0.85,"quote":"bez zahrady","pattern":"bez zahrady"}}`)
	zFalseBody := mkBody(`{"has_garden":{"value":false,"confidence":0.85,"quote":"bez zahrady","pattern":"bez zahrady"}}`)

	var (
		mu    sync.Mutex
		calls int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		var req chatReq
		user := ""
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			for _, m := range req.Messages {
				if m.Role == "user" {
					user = m.Content
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(user, "ZFALSE"):
			fmt.Fprint(w, zFalseBody)
		case strings.Contains(user, "ZTRUE"):
			fmt.Fprint(w, zTrueBody)
		default:
			fmt.Fprint(w, plynBody)
		}
	}))
	defer srv.Close()

	// Порог повторений 3 (свой шаблон «s plynem» его наберёт); потолок
	// mined-уверенности 0.7 — проверит срез conf=min(conf, 0.7) < 0.85.
	// MinedAutoPromote: true — тест проверяет именно путь авто-промоушна
	// (по умолчанию выключен: держать до ревизии, см. rules.auto_promote).
	cfg := &Config{
		BaseURL:             srv.URL,
		APIKey:              "test-key",
		Model:               "mock-model",
		TimeoutSec:          5,
		MaxConfidence:       0.9,
		AttrListVersion:     "v1",
		MaxChars:            4000,
		MinedMinOccurrences: 3,
		MinedConfidenceMax:  0.7,
		MinedExampleObjects: 5,
		MinedAutoPromote:    true,
	}

	rep, err := Run(ctx, pool, cfg, 0, "VX")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	mu.Lock()
	ncalls := calls
	mu.Unlock()
	if ncalls != 5 {
		t.Fatalf("вызовов к mock: %d, ожидалось 5", ncalls)
	}
	if rep.Candidates != 5 || rep.Extracted != 5 || rep.Valid != 5 ||
		rep.Dropped != 0 || rep.NoQuote != 0 || rep.Mined != 5 ||
		rep.Tokens != 500 || rep.Promoted != 1 {
		t.Fatalf("отчёт: %+v", rep)
	}

	// Шаблон «s plynem»: 3 согласованных значения → occurrences=3,
	// конфликтов нет → на пороге промоутится в active.
	check := func(pattern string, wantStatus string, wantOcc, wantConf int, wantConfFloat float64) {
		var (
			status, source, evidence string
			occ, conf                int
			confNum                  float64
			promoted                 *bool
			examples                 []int64
		)
		if err := pool.QueryRow(ctx, `
			SELECT status, source, evidence, occurrences, conflicts, confidence,
			       promoted_at IS NOT NULL, example_object_ids
			FROM attribute_templates
			WHERE country = 'VX' AND attr_key = 'has_garden' AND pattern = $1`, pattern).
			Scan(&status, &source, &evidence, &occ, &conf, &confNum, &promoted, &examples); err != nil {
			t.Fatalf("шаблон %q: %v", pattern, err)
		}
		if status != wantStatus {
			t.Fatalf("шаблон %q: status=%s, ожидалось %s", pattern, status, wantStatus)
		}
		if source != "llm_mined" {
			t.Fatalf("шаблон %q: source=%s, ожидалось llm_mined", pattern, source)
		}
		if evidence != pattern {
			t.Fatalf("шаблон %q: evidence=%q, ожидалась цитата-паттерн", pattern, evidence)
		}
		if occ != wantOcc || conf != wantConf {
			t.Fatalf("шаблон %q: occurrences=%d conflicts=%d, ожидалось %d/%d",
				pattern, occ, conf, wantOcc, wantConf)
		}
		if confNum != wantConfFloat {
			t.Fatalf("шаблон %q: confidence=%v, ожидалось %v (срез mined_ceiling)",
				pattern, confNum, wantConfFloat)
		}
		if pattern == "s plynem" {
			if promoted == nil || !*promoted {
				t.Fatalf("шаблон %q: не промоутится (promoted_at NULL)", pattern)
			}
			// Примеры — все три объекта-доказательства (distinct-сложение).
			sort.Slice(examples, func(i, j int) bool { return examples[i] < examples[j] })
			want := []int64{obj1, obj2, obj3}
			if len(examples) != 3 {
				t.Fatalf("шаблон %q: примеров %d (%v), ожидалось 3", pattern, len(examples), examples)
			}
			for i := range want {
				if examples[i] != want[i] {
					t.Fatalf("шаблон %q: примеры %v, ожидалось %v", pattern, examples, want)
				}
			}
		}
	}
	check("s plynem", "active", 3, 0, 0.7)
	check("bez zahrady", "conflict", 1, 1, 0.7)

	// Значения записаны объектам (проверим один: obj1 → has_garden=true).
	var attrsRaw []byte
	if err := pool.QueryRow(ctx, `SELECT attributes FROM objects WHERE id = $1`, obj1).Scan(&attrsRaw); err != nil {
		t.Fatal(err)
	}
	var attrs map[string]any
	if err := json.Unmarshal(attrsRaw, &attrs); err != nil {
		t.Fatal(err)
	}
	if attrs["has_garden"] != true {
		t.Fatalf("attributes obj1: has_garden=%v, ожидалось true", attrs["has_garden"])
	}
}

// TestLiveMiningHeld — путь удержания (rules.auto_promote=false, дефолт):
// mined-кандидат, набравший порог повторений, остаётся 'candidate' — его НЕ
// промоутят, и он не применяется к корпусу (Load загружает только active).
// Это и есть защита от регресса «перегретых однословников» (gate-отчёт
// 2026-09-06): шаблон попадает в арсенал, но в работу — только после
// ручной ревизии (pb rules set <id> --status active).
func TestLiveMiningHeld(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// Чистим только песочную страну VX — чужие страны не трогаем.
	_, _ = pool.Exec(ctx, `DELETE FROM attribute_templates WHERE country = 'VX'`)
	_, _ = pool.Exec(ctx, `DELETE FROM attribute_values WHERE object_id IN (SELECT id FROM objects WHERE country = 'VX')`)
	_, _ = pool.Exec(ctx, `DELETE FROM llm_extractions WHERE object_id IN (SELECT id FROM objects WHERE country = 'VX')`)
	_, _ = pool.Exec(ctx, `DELETE FROM objects WHERE country = 'VX'`)
	_, _ = pool.Exec(ctx, `DELETE FROM attribute_registry WHERE country = 'VX'`)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM attribute_templates WHERE country = 'VX'`)
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO attribute_registry
		    (country, key, data_type, allowed_values, label_ru, label_en, source_evidence, value_min, value_max)
		VALUES ('VX', 'has_garden', 'bool', 'null', 'тест', 'test', 'test', NULL, NULL)`); err != nil {
		t.Fatalf("реестр: %v", err)
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
	// 3 объекта с «s plynem» → шаблон occurrences=3; порог 2 — кандидат
	// «готов к промоушну», но при удержании должен остаться candidate.
	var ids []int64
	for _, d := range []string{"byt s plynem, 2kk", "byt s plynem, 3kk", "byt s plynem, 1kk"} {
		ids = append(ids, insObj(d))
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM attribute_values WHERE object_id = ANY($1)`, ids)
		_, _ = pool.Exec(ctx, `DELETE FROM llm_extractions WHERE object_id = ANY($1)`, ids)
		_, _ = pool.Exec(ctx, `DELETE FROM objects WHERE id = ANY($1)`, ids)
	})

	// Mock-LLM: всегда has_garden=true, цитата «s plynem».
	inner, _ := json.Marshal(`{"has_garden":{"value":true,"confidence":0.85,"quote":"s plynem","pattern":"s plynem"}}`)
	plynBody := `{"model":"mock-model","choices":[{"message":{"role":"assistant","content":` +
		string(inner) + `}}],"usage":{"total_tokens":100}}`
	var (
		mu    sync.Mutex
		calls int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, plynBody)
	}))
	defer srv.Close()

	// MinedAutoPromote: false — путь удержания. Порог 2: шаблон «s plynem»
	// (occurrences=3) готов к промоушну, но применяться не должен.
	cfg := &Config{
		BaseURL:             srv.URL,
		APIKey:              "test-key",
		Model:               "mock-model",
		TimeoutSec:          5,
		MaxConfidence:       0.9,
		AttrListVersion:     "v1",
		MaxChars:            4000,
		MinedMinOccurrences: 2,
		MinedConfidenceMax:  0.9,
		MinedExampleObjects: 5,
		MinedAutoPromote:    false,
	}

	rep, err := Run(ctx, pool, cfg, 0, "VX")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	mu.Lock()
	ncalls := calls
	mu.Unlock()
	if ncalls != 3 {
		t.Fatalf("вызовов к mock: %d, ожидалось 3", ncalls)
	}
	// Промоушн/Ready считаются по стране прогона (VX, очищена) → Ready=1
	// ровно (шаблон «s plynem»), Promoted=0 (удержание).
	if rep.Extracted != 3 || rep.Mined != 3 || rep.Promoted != 0 || rep.Ready != 1 {
		t.Fatalf("отчёт: %+v", rep)
	}

	// Шаблон «s plynem» остаётся candidate, promoted_at не выставлен.
	var (
		status   string
		occ      int
		promoted *bool
	)
	if err := pool.QueryRow(ctx, `
		SELECT status, occurrences, promoted_at IS NOT NULL
		FROM attribute_templates
		WHERE country = 'VX' AND attr_key = 'has_garden' AND pattern = 's plynem'`).
		Scan(&status, &occ, &promoted); err != nil {
		t.Fatalf("шаблон: %v", err)
	}
	if status != "candidate" {
		t.Fatalf("шаблон «s plynem»: status=%s, ожидалось candidate (удержание)", status)
	}
	if occ != 3 {
		t.Fatalf("шаблон «s plynem»: occurrences=%d, ожидалось 3", occ)
	}
	if promoted != nil && *promoted {
		t.Fatalf("шаблон «s plynem»: promoted_at выставлен, хотя удержание включено")
	}
}
