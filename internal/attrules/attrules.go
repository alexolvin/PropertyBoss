// attrules — арсенал программных шаблонов атрибутов (ТЗ v4 §4.2, шаг 2
// цепочки значений; гибридное извлечение по директиве: стандартные фразы —
// быстрым парсингом без LLM, арсенал — данные в БД и растёт без пересборки).
//
// Шаблон (таблица attribute_templates, миграция 0027):
//   - pattern_type='regex'  — RE2 в стиле bazos/rules.go; числовой capture
//     (именованная группа «val» — (?P<val>...) — либо первая позиционная
//     в наследованных шаблонах) → динамическое значение (bazos.ParseCZNum),
//     проверяемое по границам (area_* — как у Go-правил Derive, остальное —
//     границы реестра). Для area_sqm/total_area — правило выбора числа C13
//     (bazos.SelectAreaNumber/SelectTotalAreaNumber, единое с Go-правилами
//     Derive): число — из привязки к формулировке общей площади (celková
//     plocha/rozloha/výměra, užitná plocha), число в контексте части объекта
//     (sklep, balkón, terasa, garáž, pozemek, …) площадью не становится
//     никогда; без capture — value строки;
//   - pattern_type='literal' — дословная фраза (создаётся ТОЛЬКО минингом из
//     LLM-успехов с дословным доказательством, mine.go): case-insensitive
//     совпадение с границами по обе стороны (символ-сосед не буква/цифра).
//
// Применение: порядок строк = порядок проверки (гварды-отрицания ставятся
// раньше утверждений), первый хит на атрибут выигрывает — та же семантика
// приоритетов, что в materialRules/conditionRules (bazos/rules.go). Числовые
// area-шаблоны — исключение: числа всех их шаблонов собираются вместе, а
// выбор идёт по правилу C13 (привязка побеждает и порядок шаблонов, и
// приоритет заголовка).
//
// Нормализация текста: нижний регистр + схлопывание пробелов. БЕЗ фолда
// диакритики — консервативно: «vytah» и «výtah» — отдельные шаблоны
// (ложное срабатывание = изобретённое значение, ТЗ v4 §0.4).
package attrules

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/connectors/bazos"
)

// MirrorKeys — атрибуты с зеркальными колонками objects (area_sqm/rooms).
// Зеркало допускает заполнение шаблонным слоем — но только в ПУСТУЮ
// колонку: заполненная — уже полученное значение, перезапись запрещена
// (enrich при появлении markup-значения перетирает: COALESCE в saveObject;
// provenance — ON CONFLICT DO UPDATE). rooms остаётся исключённым: чисел
// комнат из текста не берём. Вызывающий включает зеркальный ключ в набор
// «пропущенных» только при пустой колонке.
var MirrorKeys = map[string]bool{"area_sqm": true, "rooms": true}

// Template — строка attribute_templates (active) с готовым регулярным
// выражением и границами реестра (для динамических значений).
type Template struct {
	ID          int64
	AttrKey     string
	PatternType string // "regex" | "literal"
	Pattern     string
	Value       any // nil — динамическое (числовой capture)
	Confidence  float64
	Min         *float64 // attribute_registry.value_min (NULL — не задана)
	Max         *float64
	// Promoted — шаблон прошёл авто-промоушн (promoted_at задан): на нём
	// работает теневой контроль (C8). Seed-шаблоны созданы active сразу —
	// promoted_at NULL и в тень не идут.
	Promoted bool
	re       *regexp.Regexp
}

// Hit — результат срабатывания шаблона.
type Hit struct {
	Key        string
	Value      any
	Confidence float64
	TemplateID int64
	// Promoted — сработал авто-промоутнутый шаблон (теневой контроль, C8).
	Promoted bool
}

// Engine — загруженные активные шаблоны страны; порядок = порядок id.
type Engine struct {
	country string
	tpls    []Template
}

// Load — активные шаблоны страны (порядок id = приоритет). Пустая страна
// — пустой движок (nil не возвращаем: вызывающему проще).
func Load(ctx context.Context, pool *pgxpool.Pool, country string) (*Engine, error) {
	rows, err := pool.Query(ctx, `
SELECT t.id, t.attr_key, t.pattern_type, t.pattern, t.value, t.confidence,
       t.promoted_at IS NOT NULL,
       ar.value_min, ar.value_max
FROM attribute_templates t
LEFT JOIN attribute_registry ar
       ON ar.country = t.country AND ar.key = t.attr_key
WHERE t.country = $1 AND t.status = 'active'
ORDER BY t.id`, country)
	if err != nil {
		return nil, fmt.Errorf("attrules: загрузка %s: %w", country, err)
	}
	defer rows.Close()
	e := &Engine{country: country}
	for rows.Next() {
		var (
			tpl   Template
			raw   []byte
			min   *float64
			max   *float64
		)
		if err := rows.Scan(&tpl.ID, &tpl.AttrKey, &tpl.PatternType, &tpl.Pattern,
			&raw, &tpl.Confidence, &tpl.Promoted, &min, &max); err != nil {
			return nil, fmt.Errorf("attrules: чтение строки: %w", err)
		}
		tpl.Min, tpl.Max = min, max
		if len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &tpl.Value); err != nil {
				return nil, fmt.Errorf("attrules: value шаблона %d: %w", tpl.ID, err)
			}
		}
		if tpl.PatternType == "regex" {
			re, err := regexp.Compile(tpl.Pattern)
			if err != nil {
				return nil, fmt.Errorf("attrules: pattern шаблона %d: %w", tpl.ID, err)
			}
			// Динамический шаблон обязан иметь числовой capture — иначе
			// значения по построению нет; честный срыв при загрузке,
			// а не молчаливый no-op.
			if tpl.Value == nil && re.NumSubexp() == 0 {
				return nil, fmt.Errorf("attrules: динамический шаблон %d без числового capture", tpl.ID)
			}
			tpl.re = re
		}
		e.tpls = append(e.tpls, tpl)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("attrules: %w", err)
	}
	return e, nil
}

// Len — число загруженных шаблонов (статусы/отчёты).
func (e *Engine) Len() int { return len(e.tpls) }

// reWS — последовательности пробельных.
var reWS = regexp.MustCompile(`\s+`)

// Normalize — нормализация для сопоставления: нижний регистр + схлопывание
// пробельных + обрезка краёв. Без фолда диакритики (см. пакетный
// комментарий).
func Normalize(s string) string {
	return strings.TrimSpace(reWS.ReplaceAllString(strings.ToLower(s), " "))
}

// Apply — применить движок к заголовку и описанию. keys — разрешённый
// набор ключей реестра (вызывающий уже исключает покрытые шагами 1–2
// атрибуты и MirrorKeys). Заголовок проверяется раньше описания; первый
// хит на атрибут выигрывает (порядок шаблонов = id).
//
// Числовые шаблоны area_sqm/total_area — правило C13: числа ВСЕХ их
// шаблонов собираются вместе (порядок шаблонов, заголовок раньше
// описания, порядок вхождений), и выигрывает не «первое», а выбранное по
// привязке к формулировке общей площади (bazos.SelectAreaNumber).
func (e *Engine) Apply(title, desc string, keys map[string]bool) map[string]Hit {
	if len(e.tpls) == 0 {
		return nil
	}
	nt, nd := Normalize(title), Normalize(desc)
	var hits map[string]Hit
	set := func(key string, v any, conf float64, tplID int64, promoted bool) {
		if hits == nil {
			hits = map[string]Hit{}
		}
		hits[key] = Hit{Key: key, Value: v, Confidence: conf, TemplateID: tplID, Promoted: promoted}
	}
	for _, t := range e.tpls {
		if !keys[t.AttrKey] {
			continue
		}
		if _, done := hits[t.AttrKey]; done {
			continue
		}
		// Динамические шаблоны: area-ключи — отдельным проходом (C13),
		// остальные — первое в-границах число.
		if t.PatternType == "regex" && t.Value == nil {
			if areaSelKeys[t.AttrKey] {
				continue
			}
			for _, s := range []string{nt, nd} {
				if v, ok := dynamicFirst(t, s); ok {
					set(t.AttrKey, v, t.Confidence, t.ID, t.Promoted)
					break
				}
			}
			continue
		}
		// Неподвижные (literal / regex с фиксированным value): первый
		// хит выигрывает.
		v, ok := matchStatic(t, nt, nd)
		if ok {
			set(t.AttrKey, v, t.Confidence, t.ID, t.Promoted)
		}
	}
	// C13: area-ключи — правило выбора числа по числам всех их шаблонов.
	for _, key := range []string{"area_sqm", "total_area"} {
		if !keys[key] {
			continue
		}
		if _, done := hits[key]; done {
			continue
		}
		nums, tpls := e.areaCands(key, nt, nd)
		if len(nums) == 0 {
			continue
		}
		var idx int
		var ok bool
		if key == "total_area" {
			idx, ok = bazos.SelectTotalAreaNumber(nums)
		} else {
			idx, ok = bazos.SelectAreaNumber(nums)
		}
		if !ok {
			continue
		}
		set(key, nums[idx].Value, tpls[idx].Confidence, tpls[idx].ID, tpls[idx].Promoted)
	}
	return hits
}

// areaSelKeys — ключи, к которым применяется правило выбора числа (C13).
var areaSelKeys = map[string]bool{"area_sqm": true, "total_area": true}

// matchStatic — неподвижный шаблон (literal / regex с фиксированным value)
// против нормализованного текста: заголовок раньше описания.
func matchStatic(t Template, nt, nd string) (any, bool) {
	if t.PatternType == "literal" {
		p := Normalize(t.Pattern)
		if nt != "" && literalHit(nt, p) {
			return t.Value, true
		}
		if nd != "" && literalHit(nd, p) {
			return t.Value, true
		}
		return nil, false
	}
	if nt != "" && t.re.MatchString(nt) {
		return t.Value, true
	}
	if nd != "" && t.re.MatchString(nd) {
		return t.Value, true
	}
	return nil, false
}

// dynamicFirst — первое в-границах число шаблона в тексте (не-area ключи;
// area-ключи — правило C13 в Apply, а не «первое число»).
func dynamicFirst(t Template, normText string) (any, bool) {
	if normText == "" {
		return nil, false
	}
	min, max := numericBounds(t)
	gi := numericGroup(t)
	for _, m := range t.re.FindAllStringSubmatch(normText, -1) {
		if gi >= len(m) || m[gi] == "" {
			continue
		}
		n, ok := bazos.ParseCZNum(m[gi])
		if !ok {
			continue
		}
		if min != nil && n < *min {
			continue
		}
		if max != nil && n > *max {
			continue
		}
		return n, true
	}
	return nil, false
}

// areaCands — числа в границах динамических шаблонов ключа (порядок
// шаблонов, заголовок раньше описания, порядок вхождений); tpls — шаблон
// каждого числа (уверенность и id хита берутся у выигравшего шаблона).
func (e *Engine) areaCands(key, nt, nd string) (nums []bazos.AreaNumber, tpls []Template) {
	for _, t := range e.tpls {
		if t.AttrKey != key || t.PatternType != "regex" || t.Value != nil {
			continue
		}
		min, max := numericBounds(t)
		gi := numericGroup(t)
		for _, s := range []string{nt, nd} {
			if s == "" {
				continue
			}
			for _, m := range t.re.FindAllStringSubmatchIndex(s, -1) {
				if 2*gi+1 >= len(m) || m[2*gi] < 0 {
					continue
				}
				v, ok := bazos.ParseCZNum(s[m[2*gi] : m[2*gi+1]])
				if !ok {
					continue
				}
				if min != nil && v < *min {
					continue
				}
				if max != nil && v > *max {
					continue
				}
				nums = append(nums, bazos.AreaNumber{
					Value:    v,
					NumStart: m[2*gi],
					NumEnd:   m[2*gi + 1],
					MatchEnd: m[1],
					Text:     s,
				})
				tpls = append(tpls, t)
			}
		}
	}
	return nums, tpls
}

// numericGroup — индекс числового capture'а динамического шаблона:
// именованная «val» (?P<val>...), иначе первая позиционная (наследованные
// шаблоны).
func numericGroup(t Template) int {
	if i := t.re.SubexpIndex("val"); i > 0 {
		return i
	}
	return 1
}

// numericBounds — границы числового шаблона. area_sqm/total_area — те же,
// что у Go-правил Derive (bazos/rules.go areaRangeMin/Max): шаблонный слой
// не расходится с шагами 1–2; реестровая граница (value_min=1) мягче — она
// для LLM-валидации, а меньше 5 м² это пристройка/гараж, не жильё.
// Остальные ключи — границы реестра (NULL — не задана, домысливать
// нельзя).
func numericBounds(t Template) (min, max *float64) {
	switch t.AttrKey {
	case "area_sqm", "total_area":
		mn, mx := 5.0, 5000.0
		return &mn, &mx
	}
	return t.Min, t.Max
}

// literalHit — дословная фраза в тексте с границами: соседний символ до и
// после не буква/цифра (иначе «2kk» сработал бы внутри «12kk», «bez» —
// внутри «bezva»). Unicode-корректно (чешская диакритика — буквы).
func literalHit(normText, normPattern string) bool {
	if normPattern == "" || len(normPattern) > len(normText) {
		return false
	}
	from := 0
	for {
		i := strings.Index(normText[from:], normPattern)
		if i < 0 {
			return false
		}
		i += from
		if leftBoundary(normText, i) && rightBoundary(normText, i+len(normPattern)) {
			return true
		}
		from = i + 1
	}
}

// leftBoundary — позиция i: символ ПЕРЕД ней (s[i-1]) не буква/цифра;
// начало строки — граница.
func leftBoundary(s string, pos int) bool {
	if pos == 0 {
		return true
	}
	left := []rune(s[:pos])
	c := left[len(left)-1]
	return !unicode.IsLetter(c) && !unicode.IsDigit(c)
}

// rightBoundary — позиция pos: символ ПОСЛЕ неё (s[pos]) не буква/цифра;
// конец строки — граница.
func rightBoundary(s string, pos int) bool {
	if pos == len(s) {
		return true
	}
	r := []rune(s[pos:])
	c := r[0]
	return !unicode.IsLetter(c) && !unicode.IsDigit(c)
}
