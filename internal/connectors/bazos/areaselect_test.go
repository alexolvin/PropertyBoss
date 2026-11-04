// areaselect_test.go — unit-тесты правила выбора числа площади (C13):
// число — из привязки к формулировке общей площади; число в контексте
// части объекта площадью объекта не становится никогда. Правило общее
// для обоих компонентов (Derive и attrules).
package bazos

import "testing"

// sel — AreaNumbers + SelectAreaNumber (правило area_sqm).
func sel(s string) (float64, bool) {
	nums := AreaNumbers(s, reAreaM2)
	i, ok := SelectAreaNumber(nums)
	if !ok {
		return 0, false
	}
	return nums[i].Value, true
}

// selTotal — AreaNumbers + SelectTotalAreaNumber (правило total_area).
func selTotal(s string) (float64, bool) {
	nums := AreaNumbers(s, reAreaM2)
	i, ok := SelectTotalAreaNumber(nums)
	if !ok {
		return 0, false
	}
	return nums[i].Value, true
}

func TestSelectAreaNumber(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want float64
		ok   bool
	}{
		// Регрессия бага корпуса 7167: общая привязана к «celkové
		// rozloze», число погреба — не площадь объекта.
		{"7167", "bytová jednotka o celkové rozloze 109 m² (z čehož 9 m² tvoří sklep)", 109, true},
		// «celková» побеждает «užitnou»; форма «celkovou plochu».
		{"celková wins", "s celkovou plochu 35 m² a užitnou plochu 32 m²", 35, true},
		{"rozlož+užitná", "celková plocha 63 m², z toho užitná 32 m²", 63, true},
		{"výměra", "dům o celkové výměře 142 m², z toho čistá obytná plocha 100 m²", 142, true},
		// «užitná» — самостоятельный источник (достаточно прилагательного).
		{"užitná", "byt, užitná plocha 45 m²", 45, true},
		{"užitná adj", "z toho užitná 32 m²", 32, true},
		// Фолбэк: первое число вне контекста части.
		{"fallback", "krásný 3+kk byt, 68 m²", 68, true},
		// Часть до числа — пропускается, берётся чистое.
		{"part before", "byt 65 m², sklep 9 m²", 65, true},
		{"only part", "sklep 9 m²", 0, false},
		{"pozemek", "pozemek 1500 m², byt 65 m²", 65, true},
		{"only pozemek", "pozemek 1500 m²", 0, false},
		// Часть после единицы («tvoří» / прямое приложение).
		{"part after tvoří", "z čehož 9 m² tvoří sklep, byt 65 m²", 65, true},
		{"part after bare", "5,5 m2 sklep, celková plocha 63 m²", 63, true},
		// Запятая между единицей и токеном — контекст части не делает.
		{"comma", "by 65 m², sklep 5 m²", 65, true},
		// «včetně X» — часть включена в общее (корпус id 10178).
		{"včetně binding", "celkové ploše včetně balkónu 45,9 m²", 45.9, true},
		{"včetně fallback", "45,9 m² včetně balkonu", 45.9, true},
		// Токен после единицы без «tvoří» управляет следующим числом —
		// не наш контекст.
		{"part after next num", "by 65 m² balkón 12 m²", 65, true},
		// «tvoří» — безусловно к этому числу, даже со следующим числом.
		{"part after tvoří + num", "9 m² tvoří sklep 5 m²", 0, false},
		// Граница предложения: чужая часть в предыдущем предложении не
		// наследуется.
		{"sentence", "sklep 3 m². Byt 65 m²", 65, true},
		// Склеивание dispozice «N+M» с площадью: «5+1 110 m²» —
		// площадь 110, не 1110 (корпус id 8621/10157).
		{"room glue 5+1", "velký byt v Ledči nad Sázavou 5+1 110 m²", 110, true},
		{"room glue 1+1", "Prodej bytu 1+1 40 m² + řadová garáž", 40, true},
		{"room glue 3+1", "byt 3+1 65 m2", 65, true},
		// Законный тысячный пробел (пробел перед числом, не «+») — не
		// трогается: «1 110 m²» = 1110.
		{"thousand space", "rodinný dům 1 110 m²", 1110, true},
		// «s terasou» (с террасой) — описательное, не площадь части.
		{"s terasou", "dom 70 m² s terasou", 70, true},
		// Аббревиатура «vč.» не режет предложение.
		{"vč. binding", "celková plocha vč. balkonu 45,9 m²", 45.9, true},
	}
	for _, c := range cases {
		got, ok := sel(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%s: sel(%q) = %v, %v; want %v, %v", c.name, c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestSelectTotalAreaNumber(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want float64
		ok   bool
	}{
		// Форма «rozloze» (старый reTotalArea её не ловил — 7167).
		{"rozloze", "o celkové rozloze 109 m² (z čehož 9 m² tvoří sklep)", 109, true},
		{"celková", "celková plocha 99 m²", 99, true},
		{"vyměře", "o celkové výměře 82,01 m²", 82.01, true},
		// «užitná» — не общее; не-привязанный фолбэк запрещён (домысел).
		{"užitná no", "užitná plocha 45 m²", 0, false},
		{"bare no", "krásný byt, 68 m²", 0, false},
		// «+ 5,5 m2 sklep» — часть после: не адресат выражения.
		{"part after", "celková plocha 63 m² + 5,5 m2 sklep", 63, true},
	}
	for _, c := range cases {
		got, ok := selTotal(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%s: selTotal(%q) = %v, %v; want %v, %v", c.name, c.in, got, ok, c.want, c.ok)
		}
	}
}

// --- Derive: правило на компоненте Go-правил --------------------------------

func TestDeriveArea7167Regression(t *testing.T) {
	d := &Detail{
		Title:       "Bytová jednotka k prodeji",
		Description: "Prodávám bytovou jednotku o celkové rozloze 109 m², z čehož 9 m² tvoří sklep.",
	}
	rc := RuleConfidence{Title: 0.9, Description: 0.8}
	r := Derive(d, rc)
	if r.Area == nil {
		t.Fatal("Area = nil, want 109 (celková; регрессия 7167)")
	}
	if v := r.Area.Value.(float64); v != 109 {
		t.Errorf("Area = %v, want 109 (celková; регрессия 7167)", v)
	}
	if r.Area.Method != "rule_markup" || r.Area.Confidence != 0.8 {
		t.Errorf("Area provenance = %s/%v, want rule_markup/0.8", r.Area.Method, r.Area.Confidence)
	}
	if r.TotalArea == nil {
		t.Fatal("TotalArea = nil, want 109")
	}
	if v := r.TotalArea.Value.(float64); v != 109 {
		t.Errorf("TotalArea = %v, want 109", v)
	}
}

func TestDeriveAreaPartFirst(t *testing.T) {
	d := &Detail{
		Title:       "Prodej bytu 65 m²",
		Description: "Byt s balkonem. Balkón 6 m², sklep 5 m².",
	}
	rc := RuleConfidence{Title: 0.9, Description: 0.8}
	r := Derive(d, rc)
	if r.Area == nil {
		t.Fatal("Area = nil, want 65 (заголовок, фолбэк вне контекста части)")
	}
	if v := r.Area.Value.(float64); v != 65 {
		t.Errorf("Area = %v, want 65", v)
	}
	if r.Area.Method != "rule_markup" || r.Area.Confidence != 0.9 {
		t.Errorf("Area provenance = %s/%v, want rule_markup/0.9", r.Area.Method, r.Area.Confidence)
	}
	// «celková» нет — TotalArea не домысливается.
	if r.TotalArea != nil {
		t.Errorf("TotalArea = %v, want nil", r.TotalArea.Value)
	}
}

func TestDeriveAreaOnlyPart(t *testing.T) {
	d := &Detail{
		Title:       "Sklep",
		Description: "Prodej sklepu 9 m².",
	}
	r := Derive(d, RuleConfidence{Title: 0.9, Description: 0.8})
	if r.Area != nil {
		t.Errorf("Area = %v, want nil (только число в контексте части)", r.Area.Value)
	}
	if r.TotalArea != nil {
		t.Errorf("TotalArea = %v, want nil", r.TotalArea.Value)
	}
}

func TestSelectAreaFromSourcesPriority(t *testing.T) {
	rc := RuleConfidence{Title: 0.9, Description: 0.8}
	d := &Detail{
		Block:       map[string]string{"Plocha": "Užitná plocha 80 m²"},
		Title:       "Byt 65 m²",
		Description: "Celková plocha 70 m².",
	}
	// Блок — приоритетный источник: его užitná-привязка выигрывает,
	// описательная celková дальше не смотрится.
	if v, method, conf, ok := SelectAreaFromSources(areaSources(d, rc), reAreaM2, false); !ok || v != 80 || method != "source_field" || conf != 1.0 {
		t.Errorf("got %v %s/%v %v, want 80 source_field/1.0", v, method, conf, ok)
	}
}
