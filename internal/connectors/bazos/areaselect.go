// areaselect.go — правило выбора числа, которое является площадью объекта
// (C13): число берётся из привязки к формулировке общей площади, а число в
// контексте части объекта площадью объекта никогда не становится.
//
// Единое правило для обоих компонентов (ТЗ v4-2 §4.2):
//   - Go-правила Derive (deriveArea/deriveTotalArea — ежедневный enrich);
//   - шаблонный слой attrules (pb rules run / enrich — движок шаблонов).
//
// Приоритеты чисел (выигрывает первое из класса выше; внутри класса —
// порядок срез: порядок шаблонов, заголовок раньше описания, порядок
// вхождений):
//  1. число, привязанное к выражению «общего» — celková plocha / celková
//     rozloha / celková výměra (все падежные формы): выражение в пределах
//     bindWindow до числа, не раньше границы предложения, и между выражением
//     и числом нет ни другого правдоподобного числа, ни токена части;
//  2. число, привязанное к выражению «užitná plocha» / «užitná» (те же
//     условия);
//  3. первое число вне контекста части (фолбэк — «первое правдоподобное»
//     без частей).
//
// Часть объекта — pozemek, parcela, zahrada, terasa, balkon/balkón,
// lodžie, sklep, garáž, komora, příslušenství: число в её контексте —
// площадь части, не объекта. Контекст: токен до числа (partBeforeWindow,
// не раньше границы предложения и последней запятой — «z čehož 9 m²
// tvoří sklep, byt 65 m²» — «sklep» не контекст 65) либо токен сразу
// после единицы с необязательным «tvoří» между ними («9 m² tvoří
// sklep», «5,5 m2 sklep»); запятая между единицей и токеном контекст не
// делает («65 m², sklep 5 m²» — 65 число объекта). Токен без «tvoří»,
// за которым стоит следующее правдоподобное число, управляет им, а не
// нашим числом («65 m² balkón 12 m²» — «balkón» принадлежит 12); «tvoří»
// — безусловно к этому числу.
//
// Документированное исключение «včetně X» / «vč. X»: часть, включённая в
// общее («celkové ploše včetně balkónu 45,9 m²», корпус id 10178) — токен
// и невалидизирует привязку, и не срабатывает как контекст: число — общее
// с учётом части.
package bazos

import (
	"regexp"
	"strings"
)

const (
	// bindWindow — глубина привязочного контекста до числа.
	bindWindow = 40
	// partBeforeWindow — глубина контекста части до числа.
	partBeforeWindow = 20
	// partAfterCap — глубина контекста части после единицы.
	partAfterCap = 16
)

// AreaNumber — правдоподобное число площади в тексте.
type AreaNumber struct {
	Value    float64
	NumStart int // начало числа (числовой capture) в Text
	NumEnd   int // конец числа
	MatchEnd int // конец всего совпадения (с единицей измерения)
	Text     string
}

// AreaNumbers — все правдоподобные числа площади строки s: regex unitRe с
// числовым capture (группа 1 — число), границы
// [areaRangeMin, areaRangeMax]. Порядок = порядок вхождений.
//
// Склеивание «N+M NNN m²»: числовой capture [0-9][0-9\s]* склеивает
// комнату dispozice с площадью («5+1 110 m²» → «1 110», что ParseCZNum
// читает как 1110 — тысячный пробел). Если перед числом «+», числом
// считается часть после последнего пробела («110»); законный «1 110 m²»
// (пробел перед числом) не трогается.
func AreaNumbers(s string, unitRe *regexp.Regexp) []AreaNumber {
	var out []AreaNumber
	for _, m := range unitRe.FindAllStringSubmatchIndex(s, -1) {
		if len(m) < 4 || m[2] < 0 || m[3] < 0 {
			continue
		}
		lo, hi := m[2], m[3]
		if lo > 0 && s[lo-1] == '+' {
			// Числовой capture захватывает и хвостовой пробел
			// («1 65 ») — отсечь: ищем последний ВНУТРЕННИЙ пробел.
			for hi > lo && s[hi-1] == ' ' {
				hi--
			}
			if j := strings.LastIndexByte(s[lo:hi], ' '); j >= 0 {
				lo += j + 1
			}
		}
		v, ok := ParseCZNum(s[lo:hi])
		if !ok || v < areaRangeMin || v > areaRangeMax {
			continue
		}
		out = append(out, AreaNumber{Value: v, NumStart: lo, NumEnd: hi, MatchEnd: m[1], Text: s})
	}
	return out
}

// TextSource — источник текста для выбора площади (порядок среза =
// приоритет источника: структурированный блок, заголовок, описание).
type TextSource struct {
	Text   string
	Method string // source_field | rule_markup
	Conf   float64
}

// SelectAreaNumber — правило C13 по срезу чисел (см. пакетный комментарий).
// Возвращает индекс выбранного числа; число в контексте части не
// возвращается никогда.
func SelectAreaNumber(nums []AreaNumber) (int, bool) {
	if i, ok := firstBound(nums, reAreaTotalExpr); ok {
		return i, true
	}
	if i, ok := firstBound(nums, reAreaUzitnaExpr); ok {
		return i, true
	}
	for i := range nums {
		if !inPartContext(nums[i]) {
			return i, true
		}
	}
	return 0, false
}

// SelectTotalAreaNumber — только выражение «общего» (celková …): «užitná»
// общим не является, а не-привязанный фолбэк для total_area — домысел
// (атрибут определён как заявленная ОБЩАЯ площадь).
func SelectTotalAreaNumber(nums []AreaNumber) (int, bool) {
	return firstBound(nums, reAreaTotalExpr)
}

// SelectAreaFromSources — правило C13 по источникам в порядке приоритета:
// внутри источника — правило по его числам; выигрывает первый источник с
// выбранным числом (старый порядок источников: блок → заголовок →
// описание).
func SelectAreaFromSources(srcs []TextSource, unitRe *regexp.Regexp, totalOnly bool) (float64, string, float64, bool) {
	for _, src := range srcs {
		nums := AreaNumbers(src.Text, unitRe)
		var idx int
		var ok bool
		if totalOnly {
			idx, ok = SelectTotalAreaNumber(nums)
		} else {
			idx, ok = SelectAreaNumber(nums)
		}
		if ok {
			return nums[idx].Value, src.Method, src.Conf, true
		}
	}
	return 0, "", 0, false
}

// firstBound — первое число, привязанное к выражению expr.
func firstBound(nums []AreaNumber, expr *regexp.Regexp) (int, bool) {
	for i := range nums {
		if boundTo(nums[i], expr) {
			return i, true
		}
	}
	return 0, false
}

// reAreaTotalExpr — выражение «общего»: celková plocha / celková rozloha /
// celková výměra, все падежные формы. Диакритика не фолдится — варианты
// явными: plocha/plochu/ploše (и plose), rozloha/rozlohu/rozloze (простая
// z), výměra/výměře/vyměře.
var reAreaTotalExpr = regexp.MustCompile(
	`(?i)celkov\p{L}*\s+(?:plo[čc]h\p{L}*|plo[šs]\p{L}*|rozlo[zžh]\p{L}*|v[ýy]m[ěe][řr]\p{L}*)`)

// reAreaUzitnaExpr — выражение «užitná plocha»; достаточно и прилагательного
// одного («z toho užitná 32 m²»).
var reAreaUzitnaExpr = regexp.MustCompile(
	`(?i)u[žz]itn\p{L}*(?:\s+(?:plo[čc]h\p{L}*|plo[šs]\p{L}*|rozlo[zžh]\p{L}*|v[ýy]m[ěe][řr]\p{L}*))?`)

// areaPartTokensPattern — токены частей объекта (общие для контекста до
// числа и контекста после единицы).
const areaPartTokensPattern = `pozem\p{L}*|parcel\p{L}*|zahr[áa]d\p{L}*|teras\p{L}*|(?:balkon|balkón)\p{L}*|lod[žz]i\p{L}*|sklep\p{L}*|gar[áa][žz]\p{L}*|komor\p{L}*|p[řr]íslu[štš]enstv\p{L}*`

var (
	// areaPartTokens — токены части: контекст до числа и сегмент привязки.
	areaPartTokens = []*regexp.Regexp{
		regexp.MustCompile(`(?i)pozem\p{L}*`),
		regexp.MustCompile(`(?i)parcel\p{L}*`),
		regexp.MustCompile(`(?i)zahr[áa]d\p{L}*`),
		regexp.MustCompile(`(?i)teras\p{L}*`),
		regexp.MustCompile(`(?i)(?:balkon|balkón)\p{L}*`),
		regexp.MustCompile(`(?i)lod[žz]i\p{L}*`),
		regexp.MustCompile(`(?i)sklep\p{L}*`),
		regexp.MustCompile(`(?i)gar[áa][žz]\p{L}*`),
		regexp.MustCompile(`(?i)komor\p{L}*`),
		regexp.MustCompile(`(?i)p[řr]íslu[štš]enstv\p{L}*`),
	}
	// rePartAfterTvorii — токен части после единицы через «tvoří»:
	// «m² tvoří sklep» — токен относится к этому числу безусловно.
	rePartAfterTvorii = regexp.MustCompile(`(?i)^\s+tvo[řr][íi]\s+(?:` + areaPartTokensPattern + `)`)
	// rePartAfterBare — токен части сразу после единицы (приложение):
	// «m² sklep». Запятая между единицей и токеном контекст не делает.
	rePartAfterBare = regexp.MustCompile(`(?i)^\s+(?:` + areaPartTokensPattern + `)`)
	// reInclPrefix — конец сегмента: перед токеном части «včetně» / «vč.»
	// (часть включена в число).
	reInclPrefix = regexp.MustCompile(`(?i)(?:^|\s)(?:v[čc]etn[ěe]|v[čc]\.)\s+$`)
	// reAnyNum — голое число в сегменте привязки (другое правдоподобное
	// число там — адресат выражения, не наше число).
	reAnyNum = regexp.MustCompile(`[\d][\d\s]*(?:[,.]\d+)?`)
)

// boundTo — число привязано к выражению: самое правое вхождение выражения в
// окне до числа (bindWindow, не раньше границы предложения) «чистое» —
// между его концом и числом нет ни другого правдоподобного числа, ни токена
// части («včetně X» — исключение). Достаточно правейшего: более далёкое
// вхождение унаследовало бы его «грязь» (число или токен между ним и
// нашим числом остались бы в сегменте).
func boundTo(n AreaNumber, expr *regexp.Regexp) bool {
	from := n.NumStart - bindWindow
	if from < 0 {
		from = 0
	}
	win := sentenceCut(n.Text[from:n.NumStart])
	ms := expr.FindAllStringIndex(win, -1)
	if len(ms) == 0 {
		return false
	}
	return segmentClean(win[ms[len(ms)-1][1]:])
}

// segmentClean — в сегменте между выражением привязки и числом нет ни
// другого правдоподобного числа (выражение тогда привязано к НЕМУ, не к
// нашему числу), ни токена части («včetně X» — исключение: часть включена
// в общее).
func segmentClean(seg string) bool {
	for _, m := range reAnyNum.FindAllStringIndex(seg, -1) {
		if v, ok := ParseCZNum(seg[m[0]:m[1]]); ok && v >= areaRangeMin && v <= areaRangeMax {
			return false
		}
	}
	for _, g := range areaPartTokens {
		if m := g.FindStringIndex(seg); m != nil && !inclBefore(seg, m[0]) {
			return false
		}
	}
	return true
}

// inPartContext — число в контексте части объекта: токен до числа
// (partBeforeWindow, не раньше границы предложения и последней запятой)
// либо сразу после единицы, с опциональным «tvoří» («9 m² tvoří sklep»).
// «včetně X» — исключение: часть включена в число.
func inPartContext(n AreaNumber) bool {
	s := n.Text
	from := n.NumStart - partBeforeWindow
	if from < 0 {
		from = 0
	}
	win := clauseCut(s[from:n.NumStart])
	for _, g := range areaPartTokens {
		if m := g.FindStringIndex(win); m != nil && !inclBefore(win, m[0]) {
			return true
		}
	}
	hi := n.MatchEnd + partAfterCap
	if hi > len(s) {
		hi = len(s)
	}
	tail := s[n.MatchEnd:hi]
	if i := strings.IndexAny(tail, ".!?;"); i >= 0 {
		tail = tail[:i]
	}
	if rePartAfterTvorii.MatchString(tail) {
		return true
	}
	// Приложение: токен — контекст числа, только если не управляет
	// следующим числом («65 m² balkón 12 m²» — «balkón» принадлежит 12).
	if m := rePartAfterBare.FindStringIndex(tail); m != nil {
		for _, nn := range reAnyNum.FindAllStringIndex(tail[m[1]:], -1) {
			if v, ok := ParseCZNum(tail[m[1]:][nn[0]:nn[1]]); ok && v >= areaRangeMin && v <= areaRangeMax {
				return false
			}
		}
		return true
	}
	return false
}

// inclBefore — слово непосредственно перед токеном части в seg[:tokenStart]
// — «včetně» / «vč.»: часть включена в число (документированное исключение
// правила части).
func inclBefore(seg string, tokenStart int) bool {
	return reInclPrefix.MatchString(seg[:tokenStart])
}

// clauseCut — обрезка окна по последней границе предложения и, ближе к
// числу, по последней запятой: «z čehož 9 m² tvoří sklep, byt 65 m²» —
// «sklep» первой части не является контекстом 65. Десятичная
// запятая числа в окне может срезать токен, стоящий ПЕРЕД числом — это
// корректно: то число — значение токена, а следующее число — уже другая
// часть предложения.
func clauseCut(win string) string {
	win = sentenceCut(win)
	if i := strings.LastIndexByte(win, ','); i >= 0 {
		win = win[i+1:]
	}
	return win
}

// sentenceCut — обрезка окна по последней границе предложения («!?”, “;”
// либо «.», не входящая в аббревиатуру «vč.»).
func sentenceCut(win string) string {
	i := strings.LastIndexAny(win, ".!?;")
	for i >= 0 {
		// «vč.» — точка аббревиатуры не граница предложения.
		if !(win[i] == '.' &&
			(strings.HasSuffix(win[:i+1], "vč.") ||
				strings.HasSuffix(win[:i+1], "vČ.") ||
				strings.HasSuffix(win[:i+1], "Vč.") ||
				strings.HasSuffix(win[:i+1], "VČ.") ||
				strings.HasSuffix(win[:i+1], "vc."))) {
			return win[i+1:]
		}
		i = strings.LastIndexAny(win[:i], ".!?;")
	}
	return win
}
