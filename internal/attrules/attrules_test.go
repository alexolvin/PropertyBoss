// attrules_test.go — unit-тесты движка шаблонов (без БД).
//
// Покрытие (ТЗ v4 §4.2, гибридное извлечение):
//   - Normalize: регистр + схлопывание пробелов, БЕЗ фолда диакритики;
//   - literal: границы по обе стороны («2kk» не внутри «12kk»), регистр;
//   - regex: статическое и динамическое (числовой capture + ParseCZNum),
//     границы реестра value_min/max;
//   - Apply: заголовок раньше описания, первый хит на атрибут выигрывает;
//   - CheckMine: ⊆-гарды, запрет цифр, длина;
//   - Promote: порог повторений + нулевые конфликты.
package attrules

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
)

// --- тестовые конструкторы (не трогая Load/БД) ---------------------------

func litT(key, pat string, val any) Template {
	return Template{ID: 1, AttrKey: key, PatternType: "literal", Pattern: pat, Value: val}
}

func regT(id int64, key, pat string, val any) Template {
	re, err := regexp.Compile(pat)
	if err != nil {
		panic(err)
	}
	return Template{ID: id, AttrKey: key, PatternType: "regex", Pattern: pat, Value: val, re: re}
}

func f64(v float64) *float64 { return &v }

// --- Normalize ------------------------------------------------------------

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  Bez   VÝTAHU  ", "bez výtahu"},
		{"Plyn\nje přiveden", "plyn je přiveden"},
		{"bez plynů", "bez plynů"},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// Без фолда диакритики: варианты — отдельные (консервативно).
	if Normalize("vytah") == Normalize("výtah") {
		t.Error("Normalize схлопывает диакритику — запрещено")
	}
}

// --- literal: границы -----------------------------------------------------

func TestLiteralBoundary(t *testing.T) {
	e := &Engine{tpls: []Template{litT("rooms", "2kk", "2kk")}}
	// «2kk» внутри «12kk» — не срабатывает (слева цифра, граница нет).
	if h := e.Apply("prodej 12kk bytu", "", map[string]bool{"rooms": true}); h != nil {
		t.Fatalf("literal «2kk» сработал внутри «12kk»: %+v", h)
	}
	// Отдельное «2kk» (края строки) — срабатывает.
	if h := e.Apply("prodej 2kk bytu", "", map[string]bool{"rooms": true}); h == nil {
		t.Fatal("literal «2kk» не сработал на «2kk»")
	}
	// «bez» внутри «bezva» — не срабатывает (справа буква).
	e2 := &Engine{tpls: []Template{litT("has_gas", "bez", "bez")}}
	if h := e2.Apply("to je bezva", "", map[string]bool{"has_gas": true}); h != nil {
		t.Fatalf("literal «bez» сработал внутри «bezva»: %+v", h)
	}
	// Символ-разделитель (запятая/точка) — граница есть.
	if h := e2.Apply("byt, bez plynu.", "", map[string]bool{"has_gas": true}); h == nil {
		t.Fatal("literal «bez» не сработал с границей по пунктуации")
	}
}

// --- regex: статическое + динамическое -----------------------------------

func TestRegexStatic(t *testing.T) {
	e := &Engine{tpls: []Template{regT(1, "has_gas", `(?i)bez\s+plyn\p{L}*`, false)}}
	if h := e.Apply("Byt bez plynu", "", map[string]bool{"has_gas": true}); h == nil || h["has_gas"].Value != false {
		t.Fatalf("regex «bez plyn*» не дал false: %+v", h)
	}
}

func TestRegexNumericCapture(t *testing.T) {
	e := &Engine{tpls: []Template{regT(1, "floor", `(\d{1,3})\s*[.,]?\s*pat[řr][ěe]`, nil)}}
	e.tpls[0].Min, e.tpls[0].Max = f64(1), f64(300)
	h := e.Apply("Nabízíme byt ve 4. patře", "", map[string]bool{"floor": true})
	if h == nil {
		t.Fatal("числовой capture не сработал")
	}
	if v, ok := h["floor"].Value.(float64); !ok || v != 4 {
		t.Fatalf("floor = %v (%T), want float64(4)", h["floor"].Value, h["floor"].Value)
	}
	// Чешское число «12,5» → 12.5 (ParseCZNum).
	e2 := &Engine{tpls: []Template{regT(1, "area_x", `([\d][\d\s]*(?:[,.]\d+)?)\s*m[²2]`, nil)}}
	e2.tpls[0].Min, e2.tpls[0].Max = f64(5), f64(5000)
	h2 := e2.Apply("celkem 82,5 m2", "", map[string]bool{"area_x": true})
	if h2 == nil {
		t.Fatal("числовой capture «82,5» не сработал")
	}
	if v := h2["area_x"].Value.(float64); v != 82.5 {
		t.Fatalf("area = %v, want 82.5", v)
	}
}

func TestRegexNumericBounds(t *testing.T) {
	e := &Engine{tpls: []Template{regT(1, "floor", `(\d{1,3})\s+pat[řr][ěe]`, nil)}}
	e.tpls[0].Min, e.tpls[0].Max = f64(1), f64(300)
	// 0 < min=1 → граница реестра не пройдена, хита нет (не домысливаем).
	if h := e.Apply("byt v 0 patře", "", map[string]bool{"floor": true}); h != nil {
		t.Fatalf("floor=0 прошёл границы (min=1): %+v", h)
	}
	// 400 > max=300 → не прошёл.
	if h := e.Apply("byt v 400 patře", "", map[string]bool{"floor": true}); h != nil {
		t.Fatalf("floor=400 прошёл границы (max=300): %+v", h)
	}
	// Границы не заданы (nil) → ограничивать нечем, хит проходит.
	e2 := &Engine{tpls: []Template{regT(1, "floor", `(\d{1,3})\s+pat[řr][ěe]`, nil)}}
	if h := e2.Apply("byt v 5 patře", "", map[string]bool{"floor": true}); h == nil {
		t.Fatal("floor=5 без границ не прошёл (должен — границ не задано)")
	}
}

// --- regex: динамические шаблоны площади (C6) -----------------------------
// именованная группа «val», контекстный гвард, границы как у Go-правил.

func TestAreaNamedGroupAndGuard(t *testing.T) {
	std := `(?P<val>[0-9][0-9\s]*(?:[,.][0-9]+)?)\s*(?:m[²2]|mkv\b|metry kvadratick\p{L}+)`
	e := &Engine{tpls: []Template{areaTpl(std)}}

	// Чистое совпадение.
	h := e.Apply("Prodej bytu 65 m², Praha", "", map[string]bool{"area_sqm": true})
	if h == nil || h["area_sqm"].Value.(float64) != 65 {
		t.Fatalf("«65 m²» → %+v, want 65", h)
	}
	// Чешское число «82,01» → 82.01 (ParseCZNum).
	h = e.Apply("byt 82,01 m2", "", map[string]bool{"area_sqm": true})
	if h == nil || h["area_sqm"].Value.(float64) != 82.01 {
		t.Fatalf("«82,01 m2» → %+v, want 82.01", h)
	}
	// Числовой контекст участка: первое совпадение под гвардом —
	// пропускается, берётся следующее чистое.
	h = e.Apply("pozemek 1500 m², byt 65 m²", "", map[string]bool{"area_sqm": true})
	if h == nil || h["area_sqm"].Value.(float64) != 65 {
		t.Fatalf("«pozemek 1500 m², byt 65 m²» → %+v, want 65 (гвард)", h)
	}
	// Только защищённое число → хита нет (честно: это площадь участка).
	if h = e.Apply("pozemek 1500 m²", "", map[string]bool{"area_sqm": true}); h != nil {
		t.Fatalf("«pozemek 1500 m²» → %+v, want нет хита (гвард)", h)
	}
	// «celková rozloha» — основная заявленная площадь (как у Derive:
	// первое число), гварда на «celkov» нет — регрессия корпуса id 7167:
	// раньше гвард отбрасывал 109, движок брал «9 m² tvoří sklep».
	h = e.Apply("Bytová jednotka o celkové rozloze 109 m² (z čehož 9 m² tvoří sklep)", "", map[string]bool{"area_sqm": true})
	if h == nil || h["area_sqm"].Value.(float64) != 109 {
		t.Fatalf("«celkové rozloze 109 m² (z čehož 9 m² tvoří sklep)» → %+v, want 109", h)
	}
	// Гвард не наследуется из предыдущего предложения (граница «. »).
	h = e.Apply("sklep 3 m². Byt 65 m²", "", map[string]bool{"area_sqm": true})
	if h == nil || h["area_sqm"].Value.(float64) != 65 {
		t.Fatalf("«sklep 3 m². Byt 65 m²» → %+v, want 65 (граница предложения)", h)
	}
	// Диакритическое «balkón»: гвард ловит и акцентированный вариант
	// (12 под гвардом, 65 — чистое, гвард дальше 20 символов).
	h = e.Apply("Slunečný byt s balkónem 12 m², užitná plocha 65 m²", "", map[string]bool{"area_sqm": true})
	if h == nil || h["area_sqm"].Value.(float64) != 65 {
		t.Fatalf("«s balkónem 12 m², užitná plocha 65 m²» → %+v, want 65 (гвард balkón)", h)
	}
	// Число без capture в динамическом шаблоне — хита нет по построению.
	e2 := &Engine{tpls: []Template{areaTpl("m[²2]")}}
	if h := e2.Apply("byt 65 m²", "", map[string]bool{"area_sqm": true}); h != nil {
		t.Fatalf("динамический шаблон без capture дал хит: %+v", h)
	}
}

func TestAreaBoundsOverrideRegistry(t *testing.T) {
	// Границы реестра (value_min=1) мягче — для LLM-валидации; числовой
	// шаблон area_* — как Go-правила (5–5000): «3 m²» в чистом контексте —
	// пристройка, не жильё.
	e := &Engine{tpls: []Template{areaTpl(`(?P<val>[0-9]+)\s*m[²2]`)}}
	if h := e.Apply("příliš malá 3 m²", "", map[string]bool{"area_sqm": true}); h != nil {
		t.Fatalf("3 m² прошло границы area (min=5): %+v", h)
	}
	// Прочие ключи — границы реестра действуют (оверрайда нет).
	e2 := &Engine{tpls: []Template{regT(1, "floor", `(?P<val>\d{1,3})\s+pat[řr][ěe]`, nil)}}
	e2.tpls[0].Min, e2.tpls[0].Max = f64(1), f64(300)
	h2 := e2.Apply("byt v 4 patře", "", map[string]bool{"floor": true})
	if h2 == nil || h2["floor"].Value.(float64) != 4 {
		t.Fatalf("floor по границам реестра: %+v, want 4", h2)
	}
}

// areaTpl — динамический шаблон area_sqm с мягкими реестровыми границами
// (1–5000): в тестах видно, что действует оверрайд (5–5000), а не они.
func areaTpl(pat string) Template {
	tpl := regT(1, "area_sqm", pat, nil)
	tpl.Min, tpl.Max = f64(1), f64(5000)
	return tpl
}

// --- Apply: приоритеты ----------------------------------------------------

func TestApplyTitleBeforeDescAndFirstHitWins(t *testing.T) {
	e := &Engine{tpls: []Template{
		// Гвард-отрицание (id=1) раньше утверждения (id=2).
		regT(1, "has_gas", `(?i)bez\s+plyn\p{L}*`, false),
		regT(2, "has_gas", `(?i)plynov\p{L}*`, true),
	}}
	// В заголовке отрицание, в описании утверждение → выигрывает заголовок (false).
	h := e.Apply("Prodej bez plynu", "plynové připojení", map[string]bool{"has_gas": true})
	if h == nil || h["has_gas"].Value != false {
		t.Fatalf("заголовок (отрицание) должен выиграть: %+v", h)
	}
	// Только описание с утверждением → true.
	h2 := e.Apply("Prodej", "plynové připojení", map[string]bool{"has_gas": true})
	if h2 == nil || h2["has_gas"].Value != true {
		t.Fatalf("описание с утверждением → true: %+v", h2)
	}
	// Ключ не в разрешённом наборе → не заполняем.
	if h3 := e.Apply("bez plynu", "", map[string]bool{"rooms": true}); h3 != nil {
		t.Fatalf("заполнили незапрошенный ключ: %+v", h3)
	}
}

// --- CheckMine: гарды -----------------------------------------------------

func TestCheckMine(t *testing.T) {
	text := "Byt má bez plynu, bez výtahu a bez revitalizace paneláru."
	norm := Normalize(text)
	cases := []struct {
		name    string
		pattern string
		quote   string
		text    string
		wantErr bool
	}{
		{"ok", "bez plynu", "Byt má bez plynu", text, false},
		{"pattern<2", "b", "b", norm, true},
		{"цифры", "bez 2", "bez 2 x", "x bez 2 x", true},
		{"pattern⊄quote", "výtahu", "bez plynu", text, true},
		{"quote⊄text", "bez plynu", "bez plynu", "НЕдословный текст", true},
	}
	for _, c := range cases {
		err := CheckMine(c.pattern, c.quote, c.text)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: CheckMine err=%v, wantErr=%v", c.name, err, c.wantErr)
		}
	}
}

// --- Mine: гард уверенности ------------------------------------------------

// TestMineConfidenceGuard — значение с неположительной уверенностью
// шаблона не порождает: модель заявила, что не доверяет извлечению,
// а constraint confidence > 0 отклонил бы строку и уронил прогон.
// Гард срабатывает до обращения к БД — pool nil намеренно.
// Регрессия к срабатыванию полного LLM-прогона 2026-09-07: модель
// вернула floor с confidence 0 → CHECK-нарушение на вставке шаблона.
func TestMineConfidenceGuard(t *testing.T) {
	text := "Přízemní byt, plná výška, bez plynu."
	for _, conf := range []float64{0, -0.1} {
		mined, err := Mine(context.Background(), nil, "CZ", "has_gas",
			false, conf, text, "bez plynu", "bez plynu", 1, 5)
		if err != nil {
			t.Fatalf("conf=%v: err = %v, want nil (гард, не сбой БД)", conf, err)
		}
		if mined {
			t.Errorf("conf=%v: mined=true, want false", conf)
		}
	}
}

// --- Правило выбора числа площади (C13) ------------------------------------
// Число — из привязки к формулировке общей площади (celková plocha/rozloha/
// výměra, užitná plocha); число в контексте части объекта (sklep, balkón,
// terasa, pozemek, …) площадью не становится никогда. Правило — общее с
// Go-правилами Derive (bazos.SelectAreaNumber).

func TestAreaSelectionRule(t *testing.T) {
	std := `(?P<val>[0-9][0-9\s]*(?:[,.][0-9]+)?)\s*(?:m[²2]|mkv\b|metry kvadratick\p{L}+)`
	e := &Engine{tpls: []Template{areaTpl(std)}}
	get := func(desc string) (float64, bool) {
		h := e.Apply("Nadpis", desc, map[string]bool{"area_sqm": true})
		if h == nil {
			return 0, false
		}
		return h["area_sqm"].Value.(float64), true
	}
	// Регрессия бага корпуса 7167: общая привязана к «celkové rozloze»,
	// «9 m² tvoří sklep» — не площадь объекта.
	if v, ok := get("Bytová jednotka o celkové rozloze 109 m² (z čehož 9 m² tvoří sklep)"); !ok || v != 109 {
		t.Fatalf("celkové rozloze: got %v, %v; want 109", v, ok)
	}
	// «celková» побеждает «užitnou».
	if v, ok := get("s celkovou plochu 35 m² a užitnou plochu 32 m²"); !ok || v != 35 {
		t.Fatalf("celková > užitná: got %v, %v; want 35", v, ok)
	}
	// «včetně X» — часть включена в общее (корпус id 10178).
	if v, ok := get("Byt o celkové ploše včetně balkónu 45,9 m²"); !ok || v != 45.9 {
		t.Fatalf("včetně: got %v, %v; want 45.9", v, ok)
	}
	// «užitná» — самостоятельный источник.
	if v, ok := get("užitná plocha 48,5 m2"); !ok || v != 48.5 {
		t.Fatalf("užitná: got %v, %v; want 48.5", v, ok)
	}
	// Часть до числа — пропускается, берётся чистое.
	if v, ok := get("sklep 9 m², byt 65 m²"); !ok || v != 65 {
		t.Fatalf("part before: got %v, %v; want 65", v, ok)
	}
	// Только часть — честный no-hit.
	if v, ok := get("sklep 9 m²"); ok {
		t.Fatalf("only part: got %v, want no hit", v)
	}
	// Часть после единицы («tvoří») в первой части; запятая начинает
	// новую часть — «sklep» не контекст 65.
	if v, ok := get("z čehož 9 m² tvoří sklep, byt 65 m²"); !ok || v != 65 {
		t.Fatalf("part after + comma: got %v, %v; want 65", v, ok)
	}
}

func TestAreaSelectionTotalKey(t *testing.T) {
	std := `(?P<val>[0-9][0-9\s]*(?:[,.][0-9]+)?)\s*(?:m[²2]|mkv\b|metry kvadratick\p{L}+)`
	e := &Engine{tpls: []Template{areaTplTotal(std)}}
	// total_area — только выражение «celková …»: «užitná» и голое число
	// общим не являются (не-привязанный фолбэк — домысел).
	if h := e.Apply("N", "užitná plocha 45 m²", map[string]bool{"total_area": true}); h != nil {
		t.Fatalf("užitná: got %+v, want nil", h)
	}
	if h := e.Apply("N", "o celkové rozloze 109 m² (z čehož 9 m² tvoří sklep)", map[string]bool{"total_area": true}); h == nil || h["total_area"].Value.(float64) != 109 {
		t.Fatalf("celková rozloze: got %+v, want 109", h)
	}
}

func areaTplTotal(pat string) Template {
	tpl := regT(1, "total_area", pat, nil)
	tpl.Min, tpl.Max = f64(1), f64(5000)
	return tpl
}

func TestAreaSelectionCrossTemplate(t *testing.T) {
	// Привязка побеждает порядок шаблонов и приоритет заголовка: голое
	// число заголовка (шаблон 1, m²) слабее «celková plocha» описания
	// (шаблон 2, sq m).
	e := &Engine{tpls: []Template{
		areaTplX(1, `(?P<val>[0-9][0-9\s]*(?:[,.][0-9]+)?)\s*m[²2]`),
		areaTplX(2, `(?P<val>[0-9][0-9\s]*(?:[,.][0-9]+)?)\s*sq\.?\s*m\b`),
	}}
	h := e.Apply("Byt 65 m²", "Celková plocha 70 sq. m", map[string]bool{"area_sqm": true})
	if h == nil || h["area_sqm"].Value.(float64) != 70 {
		t.Fatalf("cross-template: got %+v, want 70 (привязка описания)", h)
	}
	if h["area_sqm"].TemplateID != 2 {
		t.Errorf("TemplateID = %d, want 2 (шаблон выигравшего числа)", h["area_sqm"].TemplateID)
	}
}

func areaTplX(id int64, pat string) Template {
	tpl := regT(id, "area_sqm", pat, nil)
	tpl.Min, tpl.Max = f64(1), f64(5000)
	return tpl
}

// TestValuesEqual — контракт равенства JSON-значений (C8: холдаут и
// тень сравнивают шаблонное значение с LLM-значением). Равенство —
// сравнение канонических JSON-кодировок (jsonEqual), без приведения
// типов внутри функции: «1 и 1.0 — одно» работает, потому что оба
// значения на практике приходят результатом json.Unmarshal (числа —
// float64(1)), а не потому что функция различает 1 от 1.0.
func TestValuesEqual(t *testing.T) {
	un := func(s string) any {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			panic(err)
		}
		return v
	}
	cases := []struct {
		name string
		a, b any
		want bool
	}{
		{"число 1 и 1.0", un("1"), un("1.0"), true},
		{"true и true", un("true"), un("true"), true},
		{"объекты с 1 и 1.0", un(`{"a":1}`), un(`{"a":1.0}`), true},
		{"true и «true»", un("true"), un(`"true"`), false},
		{"число 65 и «65»", un("65"), un(`"65"`), false},
		{"null и 0", un("null"), un("0"), false},
		{"true и 1", un("true"), un("1"), false},
		{"без приведения типов: 1.5 ≠ 1", float64(1.5), int64(1), false},
		{"каноническая кодировка: int64(1) и float64(1.0) — «1» = «1»", int64(1), float64(1.0), true},
	}
	for _, c := range cases {
		if got := ValuesEqual(c.a, c.b); got != c.want {
			t.Errorf("%s: ValuesEqual(%v, %v) = %v, want %v", c.name, c.a, c.b, got, c.want)
		}
	}
}
