// rules.go — извлечение атрибутов правилами из текста (ТЗ v4 §4.2).
//
// Порядок источников (первый найденный выигрывает, догадка запрещена,
// ТЗ v4 §0.4):
//  1. структурированный блок описания (Plocha/Stavba/…) — source_field,
//     уверенность 1.0;
//  2. заголовок объявления — rule, уверенность detail.rule_confidence_title;
//  3. описание — rule, уверенность detail.rule_confidence_description.
//
// Чешские числа: запятая — десятичный разделитель («82,01»), точка —
// разрядный («13.900.000»). Диапазоны записываются нижней границей
// (допускение, зафиксировано в отчёте).
package bazos

import (
	"regexp"
	"strconv"
	"strings"
)

// Attr — извлечённое значение с происхождением (ТЗ v4 §4.2).
type Attr struct {
	Value      any
	Method     string // source_field | rule
	Confidence float64
}

// RuleConfidence — уверенность правил из конфига (detail.*).
type RuleConfidence struct {
	Title       float64 // правила из заголовка
	Description float64 // правила из описания
}

// Derived — атрибуты, извлечённые со страницы объявления.
// Нил — на странице не найдено (NULL в базе, не пустая строка и не 0).
type Derived struct {
	Area        *Attr  // м², float64 — C13: celková > užitná > первое вне контекста части (areaselect.go)
	TotalArea   *Attr  // м², float64 — только выражение «celková …» (areaselect.go)
	Rooms       *Attr  // int — «Nkk»
	Floor       *Attr  // int
	Floors      *Attr  // int — этажей в доме
	Material    *Attr  // string — building_material
	EnergyClass *Attr  // string — energy_class (блок описания только)
	Ownership   *Attr  // string — ownership_type
	Condition   *Attr  // string — condition
	HasElevator *Attr  // bool
	NewBuild    *Attr  // bool
	YearBuilt   *Attr  // int — в CZ-реестре нет (нет доказательств) → attributes_unmapped
	MonthlyCZK  *Attr  // float64, CZK/мес — диапазоны: нижняя граница
	PlotArea    *Attr  // м², float64 — только для land (pozemek)
	Street      string // улица для геокодирования (атрибутом не является)
}

// areaRange — правдоподобная площадь объекта, м² (помещение/дом).
const areaRangeMin, areaRangeMax = 5.0, 5000.0

var (
	// ВАЖНО: Go \w/\b — только ASCII; чешские слова заканчиваются
	// диакритикой (podlaží, dobrém), поэтому в шаблонах на чешский
	// текст используется \p{L}, а \b ставится только после ASCII-букв.
	// reAreaM2 — «80 m²», «82,01 m2», «99 m²», «53 mkv».
	reAreaM2 = regexp.MustCompile(`([\d][\d\s]*(?:[,.]\d+)?)\s*(?:m[²2]|mkv\b|metry kvadratick\w+)`)
	// reRooms — «3kk», «3 kk», «2+kk», «1+KK».
	reRooms = regexp.MustCompile(`(?i)\b(\d{1,2})\s*\+?\s*kk\b`)
	// reRooms1 — «3+1» (чешская запись = 3+kk: комнаты + кухня).
	reRooms1 = regexp.MustCompile(`(?i)\b(\d{1,2})\s*\+\s*1\b`)
	// reFloorNum — «2. NP», «ve 4. patře», «10. podlaží», «4. nadzemním podlaží».
	reFloorNum = regexp.MustCompile(`\b(\d{1,3})\s*[.,]?\s*(?:NP\b|pat[řr][ěe]|patra\b|patro\b|podla[žz][íi]|nadzemn[íi]m\s+podla[žz][íi])`)
	// reFloorWord — «ve druhém podlaží» (порядковые числительные).
	// Без \b: «čtvrt»/«šest» начинаются чешскими знаками — ASCII-\b
	// перед ними никогда не сработает (см. комментарий к var-блоку).
	// [íée]: локатив окончаний — prvním/třetím (í), druhém/čtvrtém/…
	// (é), плюс «pátem» с обычной e (нередкая опечатка на сайте).
	reFloorWord = regexp.MustCompile(`(?i)(prvn|druh|třet|čtvrt|pát|šest|sedm|osm|devát|desát)[íée]\p{L}*\s+(?:podla[žz]\p{L}*|pat[řr]\p{L}*)`)
	// reGround — «v přízemí» → этаж 1.
	reGround = regexp.MustCompile(`(?i)\bp[řr]ízem[íi]\b`)
	// reFloorsNum — «s 11 nadzemními podlažími», «s 8 podlažími».
	reFloorsNum = regexp.MustCompile(`\b(\d{1,2})\s+(?:nadzemn[íi]m\p{L}*\s+podla[žz][íi]\p{L}*|podla[žz][íi]\p{L}*|patr\p{L}*)`)
	// reElev / reNoElev — «s výtahem» / «bez výtahu».
	reElev   = regexp.MustCompile(`(?i)\bv[ýy]tah\p{L}*`)
	reNoElev = regexp.MustCompile(`(?i)bez\s+v[ýy]tah\p{L}*`)
	// reNewBuild — «novostavba», «úplně nový byt».
	reNewBuild = regexp.MustCompile(`(?i)novostavb\p{L}*|úpln[ěe]\s+nov\p{L}*`)
	// reOV — аббревиатура «OV» (osobní vlastnictví) — только верхний
	// регистр: нижний «ov» встречается в словах.
	reOV = regexp.MustCompile(`\bOV\b`)
	// reOvText — устойчивая связка «osobním vlastnictvím».
	reOvText = regexp.MustCompile(`(?i)osobn[íi]m vlastnictv\p{L}*`)
	// reMonthRange — «2000-3000 Kč/měsíc» → нижняя граница 2000.
	reMonthRange = regexp.MustCompile(`(\d[\d\s]*(?:[,.]\d+)?)\s*[-–—]\s*(\d[\d\s]*(?:[,.]\d+)?)\s*Kč\s*/?\s*měs\p{L}*`)
	// reMonth — «5000 Kč/měsíc».
	reMonth = regexp.MustCompile(`(\d[\d\s]*(?:[,.]\d+)?)\s*Kč\s*/?\s*měs\p{L}*`)
	// reYearBuilt — «stavební rok: 1998», «postaven v roce 1995»,
	// «v roce 1998 postaven». (Год РЕКОНСТРУКЦИИ не ловится:
	// «prošel v roce 2020 rekonstrukcí» не содержит слова постройки.)
	reYear1 = regexp.MustCompile(`(?i)stavebn[íi] rok\s*[:\-]?\s*(\d{4})`)
	reYear2 = regexp.MustCompile(`(?i)(?:postaven\p{L}*|vystav[ěe]n\p{L}*|vybudován\p{L}*|kolaudován\p{L}*)\s*(?:byl\p{L}*)?\s*(?:v|ve)\s+roce\s*(\d{4})`)
	reYear3 = regexp.MustCompile(`(?i)(?:v|ve)\s+roce\s*(\d{4})\s*(?:postaven\p{L}*|vystav[ěe]n\p{L}*|vybudován\p{L}*|kolaudován\p{L}*)`)
	// rePlot — «pozemek o ploše 500 m²», «výměra 400 ar».
	rePlot   = regexp.MustCompile(`(?i)pozem\p{L}*\s*(?:o\s*)?(?:ploš\p{L}*|v[ýy]m[ěe][řr]\p{L}*)\s*[:\-]?\s*([\d][\d\s]*(?:[,.]\d+)?)\s*(m[²2]|mkv|ar|ha)`)
	rePlotAr = regexp.MustCompile(`(?i)\b([\d][\d\s]*(?:[,.]\d+)?)\s*ar\b`)
	// reCZThousands — разрядные точки без десятичной запятой: «1.234».
	reCZThousands = regexp.MustCompile(`^\d{1,3}(\.\d{3})+$`)
	// reEnergyText — «energetická třída: C», «zařazen v energetické
	// třídě A». «věž C» не ловится (нет слова «tříd»).
	reEnergyText = regexp.MustCompile(`(?i)t[řr][íi]d[aě]?\s*[:\-]?\s*([A-G])\b`)
	// reStreet — «v ulici Diviškové», «ul. Högerova», «Högerova 633/4».
	reStreet1 = regexp.MustCompile(`(?i)v ulici\s+([A-ZÁ-Ž][\p{L}\-]{2,30})`)
	reStreet2 = regexp.MustCompile(`\bul\.?\s+([A-ZÁ-Ž][\p{L}\-]{2,30})`)
	// Гвард (?:^|[^\p{L}]): буква-предшественник запрещена, но \b
	// перед «Á»/«Č» не работает (ASCII-only).
	reStreet3 = regexp.MustCompile(`(?i)(?:^|[^\p{L}])([A-ZÁ-Ž][\p{L}\-]{2,20})\s+\d{1,4}/\d{1,4}\b`)
)

// floorsWordRules — словесные составные числительные этажности
// (jednopodlažní, dvoupatrový, čtyřpodlažního, šestipodlažního, …).
// Проверяются по порядку; первое совпадение выигрывает.
var floorsWordRules = []struct {
	re *regexp.Regexp
	n  int
}{
	// Без \b: «čtyř»/«šesti» начинаются чешскими знаками (ASCII-\b перед
	// ними не сработает); суффикс podlažn/patrov исключает ложные срабатывания.
	{regexp.MustCompile(`(?i)jedn[oi]?(?:podla[žz]n\p{L}*|patrov\p{L}*)`), 1},
	{regexp.MustCompile(`(?i)dvou[oi]?(?:podla[žz]n\p{L}*|patrov\p{L}*)`), 2},
	{regexp.MustCompile(`(?i)t[řr]i[oi]?(?:podla[žz]n\p{L}*|patrov\p{L}*)`), 3},
	{regexp.MustCompile(`(?i)čtyř[oi]?(?:podla[žz]n\p{L}*|patrov\p{L}*)`), 4},
	{regexp.MustCompile(`(?i)pět[oi]?(?:podla[žz]n\p{L}*|patrov\p{L}*)`), 5},
	{regexp.MustCompile(`(?i)šesti[oi]?(?:podla[žz]n\p{L}*|patrov\p{L}*)`), 6},
	{regexp.MustCompile(`(?i)sedm[ií]?[oi]?(?:podla[žz]n\p{L}*|patrov\p{L}*)`), 7},
	{regexp.MustCompile(`(?i)osmi[oi]?(?:podla[žz]n\p{L}*|patrov\p{L}*)`), 8},
	{regexp.MustCompile(`(?i)dev[ěi]ti[oi]?(?:podla[žz]n\p{L}*|patrov\p{L}*)`), 9},
	{regexp.MustCompile(`(?i)deseti[oi]?(?:podla[žz]n\p{L}*|patrov\p{L}*)`), 10},
}

var floorWordNums = map[string]int{
	"prvn": 1, "druh": 2, "třet": 3, "čtvrt": 4, "pát": 5,
	"šest": 6, "sedm": 7, "osm": 8, "devát": 9, "desát": 10,
}

// materialRules — порядок важен: cihlopanel содержит «panel»,
// поэтому проверяется первым.
var materialRules = []struct {
	re  *regexp.Regexp
	val string
}{
	{regexp.MustCompile(`(?i)cihlopanel`), "cihlopanelova"},
	{regexp.MustCompile(`(?i)cihlov`), "cihlova"},
	{regexp.MustCompile(`(?i)panel`), "panelova"},
	{regexp.MustCompile(`(?i)kamenn`), "kamenna"},
	{regexp.MustCompile(`(?i)zd[ěe]n`), "zdena"},
	{regexp.MustCompile(`(?i)dřev|dreve`), "drevena"},
}

// conditionRules — приоритет = порядок проверки. Состояние важнее
// истории: «zrekonstruovaný, ve velmi dobrém stavu» → very_good.
var conditionRules = []struct {
	re  *regexp.Regexp
	val string
}{
	{regexp.MustCompile(`(?i)velmi dobr\p{L}* stav`), "very_good"},
	{regexp.MustCompile(`(?i)dobr\p{L}* stav`), "good"},
	{regexp.MustCompile(`(?i)středn\p{L}* stav|stredn\p{L}* stav`), "average"},
	{regexp.MustCompile(`(?i)špatn\p{L}* stav|zchátral\p{L}*|s nutností rekonstrukce`), "poor"},
	{regexp.MustCompile(`(?i)zrekonstruovan\p{L}*|po rekonstrukci|kompletn\p{L}* rekonstr`), "renovated"},
}

// Derive извлекает атрибуты из разобранной страницы.
func Derive(d *Detail, rc RuleConfidence) Derived {
	var r Derived
	r.Area = deriveArea(d, rc)
	r.TotalArea = deriveTotalArea(d, rc)
	r.Rooms = deriveRooms(d, rc)
	r.Floor = deriveFloor(d, rc)
	r.Floors = deriveFloors(d, rc)
	r.Material = deriveMaterial(d, rc)
	r.EnergyClass = deriveEnergy(d, rc)
	r.Ownership = deriveOwnership(d, rc)
	r.Condition = deriveCondition(d, rc)
	r.HasElevator = deriveElevator(d, rc)
	r.NewBuild = deriveNewBuild(d, rc)
	r.YearBuilt = deriveYearBuilt(d, rc)
	r.MonthlyCZK = deriveMonthly(d, rc)
	r.PlotArea = derivePlotArea(d, rc)
	r.Street = deriveStreet(d)
	return r
}

// ---- числовые разборы ----------------------------------------------------

// ParseCZNum разбирает чешское число: «82,01» → 82.01, «1.234» → 1234.
// Экспортируется: общий разбор чешских чисел (атрибут-шаблоны attrules).
func ParseCZNum(s string) (float64, bool) {
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return 0, false
	}
	if strings.Contains(s, ",") {
		// Запятая есть → точки только разрядные: «1.234,56» → 1234.56.
		s = strings.ReplaceAll(s, ".", "")
		s = strings.Replace(s, ",", ".", 1)
	} else if reCZThousands.MatchString(s) { // разрядные точки: «1.234»
		s = strings.ReplaceAll(s, ".", "")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// ---- атрибуты -------------------------------------------------------------

// deriveArea — площадь объекта (правило C13, areaselect.go): число — из
// привязки к формулировке общей площади, число в контексте части не
// берётся. Источники по приоритету: структурированный блок (source_field),
// заголовок, описание (rule_markup).
func deriveArea(d *Detail, rc RuleConfidence) *Attr {
	if v, method, conf, ok := SelectAreaFromSources(areaSources(d, rc), reAreaM2, false); ok {
		return &Attr{v, method, conf}
	}
	return nil
}

// deriveTotalArea — заявленная ОБЩАЯ площадь: только выражение
// «celková plocha/rozloha/výměra» (C13, areaselect.go). «užitná» общим не
// является, а не-привязанный фолбэк — домысел.
func deriveTotalArea(d *Detail, rc RuleConfidence) *Attr {
	if v, method, conf, ok := SelectAreaFromSources(areaSources(d, rc), reAreaM2, true); ok {
		return &Attr{v, method, conf}
	}
	return nil
}

// areaSources — источники площади в порядке приоритета: структурированный
// блок «Plocha», заголовок, описание.
func areaSources(d *Detail, rc RuleConfidence) []TextSource {
	var out []TextSource
	if v, ok := d.Block["Plocha"]; ok {
		out = append(out, TextSource{Text: v, Method: "source_field", Conf: 1.0})
	}
	out = append(out,
		TextSource{Text: d.Title, Method: "rule_markup", Conf: rc.Title},
		TextSource{Text: d.Description, Method: "rule_markup", Conf: rc.Description},
	)
	return out
}

func deriveRooms(d *Detail, rc RuleConfidence) *Attr {
	for _, src := range []struct {
		text string
		conf float64
	}{
		{d.Title, rc.Title},
		{d.Description, rc.Description},
	} {
		// «N+kk» — каноническая запись; первое вхождение.
		if m := reRooms.FindStringSubmatch(src.text); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n >= 1 && n <= 8 {
				return &Attr{n, "rule_markup", src.conf}
			}
		}
		// «N+1» (= N+kk): ПОСЛЕДНЕЕ вхождение — в текстах вида
		// «původně 3+1, po rekonstrukci nyní 4+1» актуальное
		// состояние описывается последним.
		if ms := reRooms1.FindAllStringSubmatch(src.text, -1); len(ms) > 0 {
			for i := len(ms) - 1; i >= 0; i-- {
				if n, err := strconv.Atoi(ms[i][1]); err == nil && n >= 1 && n <= 8 {
					return &Attr{n, "rule_markup", src.conf}
				}
			}
		}
	}
	return nil
}

func deriveFloor(d *Detail, rc RuleConfidence) *Attr {
	from := func(s string, method string, conf float64) *Attr {
		if m := reFloorNum.FindStringSubmatch(s); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n >= 0 && n <= 200 {
				return &Attr{n, method, conf}
			}
		}
		if m := reFloorWord.FindStringSubmatch(s); m != nil {
			if n, ok := floorWordNums[m[1]]; ok {
				return &Attr{n, method, conf}
			}
		}
		if reGround.MatchString(s) {
			return &Attr{1, method, conf}
		}
		return nil
	}
	if v, ok := d.Block["Stavba"]; ok {
		if a := from(v, "source_field", 1.0); a != nil {
			return a
		}
	}
	return from(d.Description, "rule_markup", rc.Description)
}

func deriveFloors(d *Detail, rc RuleConfidence) *Attr {
	from := func(s string, method string, conf float64) *Attr {
		// Собираем все кандидатуры (числовые и словесные) и берём
		// первую по положению — в тексте обычно одна относится к дому.
		type cand struct{ pos, n int }
		var best *cand
		consider := func(pos, n int) {
			if n < 1 || n > 200 {
				return
			}
			if best == nil || pos < best.pos {
				best = &cand{pos, n}
			}
		}
		if idx := reFloorsNum.FindStringSubmatchIndex(s); idx != nil {
			if n, err := strconv.Atoi(s[idx[2]:idx[3]]); err == nil {
				consider(idx[0], n)
			}
		}
		for _, r := range floorsWordRules {
			if i := r.re.FindStringIndex(s); i != nil {
				consider(i[0], r.n)
			}
		}
		if best == nil {
			return nil
		}
		return &Attr{best.n, method, conf}
	}
	if v, ok := d.Block["Stavba"]; ok {
		if a := from(v, "source_field", 1.0); a != nil {
			return a
		}
	}
	return from(d.Description, "rule_markup", rc.Description)
}

func deriveMaterial(d *Detail, rc RuleConfidence) *Attr {
	from := func(s string, method string, conf float64) *Attr {
		for _, r := range materialRules {
			if r.re.MatchString(s) {
				return &Attr{r.val, method, conf}
			}
		}
		return nil
	}
	if v, ok := d.Block["Stavba"]; ok {
		if a := from(v, "source_field", 1.0); a != nil {
			return a
		}
	}
	return from(d.Description, "rule_markup", rc.Description)
}

// deriveEnergy — сначала структурированный блок (source_field, 1.0);
// свободный текст — только устойчивая конструкция «třída/třídě A–G»
// (буква сама по себе не разбирается: ложное срабатывание =
// изобретённое значение, ТЗ v4 §0.4).
func deriveEnergy(d *Detail, rc RuleConfidence) *Attr {
	if v, ok := d.Block["Energetická náročnost"]; ok {
		t := strings.ToLower(strings.TrimSpace(v))
		// «neúsporn» содержит «úsporn» — отрицание проверяется первым.
		switch {
		case strings.Contains(t, "neúsporn") || strings.Contains(t, "neusporn"):
			return &Attr{"nesporne", "source_field", 1.0}
		case strings.Contains(t, "úsporn") || strings.Contains(t, "usporn"):
			return &Attr{"usporne", "source_field", 1.0}
		}
		if m := reEnergyText.FindStringSubmatch(v); m != nil {
			return &Attr{strings.ToUpper(m[1]), "source_field", 1.0}
		}
		if len(t) == 1 && t[0] >= 'a' && t[0] <= 'g' {
			return &Attr{strings.ToUpper(t), "source_field", 1.0}
		}
	}
	for _, src := range []struct {
		text string
		conf float64
	}{
		{d.Title, rc.Title},
		{d.Description, rc.Description},
	} {
		if m := reEnergyText.FindStringSubmatch(src.text); m != nil {
			return &Attr{strings.ToUpper(m[1]), "rule_markup", src.conf}
		}
	}
	return nil
}

func deriveOwnership(d *Detail, rc RuleConfidence) *Attr {
	// Блок Vlastnictví: «Osobní»/«Družstevní» — значение поля, ложное
	// срабатывание невозможно (контекст метки).
	if v, ok := d.Block["Vlastnictví"]; ok {
		t := strings.ToLower(v)
		if strings.Contains(t, "družstevn") || strings.Contains(t, "druzstevn") {
			return &Attr{"druzstevni", "source_field", 1.0}
		}
		if strings.Contains(t, "osobn") {
			return &Attr{"osobni", "source_field", 1.0}
		}
	}
	// Свободный текст: «osobní» без «vlastnictví» ложное (например,
	// «osobní výtah») — только устойчивые связки + аббревиатура OV.
	text := func(s string, method string, conf float64) *Attr {
		t := strings.ToLower(s)
		switch {
		case strings.Contains(t, "družstevn") || strings.Contains(t, "druzstevn"):
			return &Attr{"druzstevni", method, conf}
		case reOvText.MatchString(s), reOV.MatchString(s):
			return &Attr{"osobni", method, conf}
		}
		return nil
	}
	if a := text(d.Title, "rule_markup", rc.Title); a != nil {
		return a
	}
	return text(d.Description, "rule_markup", rc.Description)
}

func deriveCondition(d *Detail, rc RuleConfidence) *Attr {
	from := func(s string, method string, conf float64) *Attr {
		for _, r := range conditionRules {
			if r.re.MatchString(s) {
				return &Attr{r.val, method, conf}
			}
		}
		return nil
	}
	if v, ok := d.Block["Stavba"]; ok {
		if a := from(v, "source_field", 1.0); a != nil {
			return a
		}
	}
	return from(d.Description, "rule_markup", rc.Description)
}

func deriveElevator(d *Detail, rc RuleConfidence) *Attr {
	from := func(s string, method string, conf float64) *Attr {
		if reNoElev.MatchString(s) {
			return &Attr{false, method, conf}
		}
		if reElev.MatchString(s) {
			return &Attr{true, method, conf}
		}
		return nil
	}
	if v, ok := d.Block["Infrastruktura"]; ok {
		if a := from(v, "source_field", 1.0); a != nil {
			return a
		}
	}
	return from(d.Description, "rule_markup", rc.Description)
}

func deriveNewBuild(d *Detail, rc RuleConfidence) *Attr {
	for _, src := range []struct {
		text string
		conf float64
	}{
		{d.Title, rc.Title},
		{d.Description, rc.Description},
	} {
		if reNewBuild.MatchString(src.text) {
			return &Attr{true, "rule_markup", src.conf}
		}
	}
	return nil
}

func deriveYearBuilt(d *Detail, rc RuleConfidence) *Attr {
	for _, re := range []*regexp.Regexp{reYear1, reYear2, reYear3} {
		if m := re.FindStringSubmatch(d.Description); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n >= 1800 && n <= 2026 {
				return &Attr{n, "rule_markup", rc.Description}
			}
		}
	}
	return nil
}

func deriveMonthly(d *Detail, rc RuleConfidence) *Attr {
	if m := reMonthRange.FindStringSubmatch(d.Description); m != nil {
		if n, ok := ParseCZNum(m[1]); ok && n > 0 { // диапазон → нижняя граница
			return &Attr{n, "rule_markup", rc.Description}
		}
	}
	if m := reMonth.FindStringSubmatch(d.Description); m != nil {
		if n, ok := ParseCZNum(m[1]); ok && n > 0 {
			return &Attr{n, "rule_markup", rc.Description}
		}
	}
	return nil
}

func derivePlotArea(d *Detail, rc RuleConfidence) *Attr {
	if m := rePlot.FindStringSubmatch(d.Description); m != nil {
		n, ok := ParseCZNum(m[1])
		if !ok {
			return nil
		}
		switch strings.ToLower(m[2]) {
		case "ha":
			n *= 10000
		case "ar":
			n *= 100
		}
		if n >= 5 && n <= 1000000 {
			return &Attr{n, "rule_markup", rc.Description}
		}
	}
	if m := rePlotAr.FindStringSubmatch(d.Description); m != nil {
		if n, ok := ParseCZNum(m[1]); ok {
			n *= 100
			if n >= 5 && n <= 1000000 {
				return &Attr{n, "rule_markup", rc.Description}
			}
		}
	}
	return nil
}

// deriveStreet — улица для геокодирования (координат площадки нет →
// Nominatim). Не атрибут: в реестре нет, в модель не попадает.
func deriveStreet(d *Detail) string {
	cand := func(s string) string {
		for _, r := range []*regexp.Regexp{reStreet1, reStreet2, reStreet3} {
			if m := r.FindStringSubmatch(s); m != nil {
				c := strings.TrimSpace(m[1])
				// Город не улица: «Praha» в «Praha 5» не подходит.
				if c != "" && !strings.EqualFold(c, d.City) {
					return c
				}
			}
		}
		return ""
	}
	if s := cand(d.Description); s != "" {
		return s
	}
	return cand(d.Title)
}
