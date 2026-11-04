// validate.go — программная проверка ответа модели по реестру
// атрибутов (ТЗ v4 §4.3). Провалившее значение отбрасывается, а не
// «исправляется вручную»: догадка запрещена (ТЗ §0.3). Проверки:
//   - enum: значение из allowed_values (регистр нормализуется —
//     допустимые значения реестра хранятся в нижнем регистре);
//   - int/float: число, целое для int, в диапазоне value_min..value_max.
//     Границы — данные реестра (ТЗ §0.10); NULL в реестре — граница не
//     задана, код домысливать её не вправе;
//   - bool: только true/false;
//   - площадь: жилая не больше общей (area_sqm ≤ total_area), если
//     вторая площадь известна (из ответа или из уже сохранённых
//     значений); недостоверная пара отбрасывается целиком.
//
// Confidence модели — число 0..1; сверху ограничивается max_confidence
// (ТЗ v4 §4.3: «уверенность модели, ограничена значением из конфига»).
// Ответ без confidence — без доказательной базы: значение отбрасывается.
//
// Значение сохраняется типизированным (bool/число/строка) — в
// objects.attributes JSONB; attribute_values хранит только provenance.
package extract

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// attrSpec — атрибут реестра в виде, нужном проверке и промпту.
type attrSpec struct {
	Key      string
	DataType string // bool | enum | int | float
	Label    string // label_en из реестра — в промпт, чтобы не путать ключи
	Values   []string
	Min      *float64
	Max      *float64
}

// extractedAttr — атрибут из ответа модели: {value, confidence, quote,
// pattern}. quote/pattern — дословное доказательство (ТЗ v4 §4.3,
// гибридное извлечение): цитата из текста и кратчайшая её фраза-сигнал;
// нужны для мининга (attribute_templates). Отсутствие — значение
// записывается, но не минится (счётчик NoQuote).
type extractedAttr struct {
	Value      any      `json:"value"`
	Confidence *float64 `json:"confidence"`
	Quote      string   `json:"quote"`
	Pattern    string   `json:"pattern"`
}

// validated — прошедший проверку атрибут: типизированное значение для
// objects.attributes + уверенность для attribute_values.
type validated struct {
	Key        string
	Value      any
	Confidence float64 // min(confidence модели, max_confidence)
}

// areaPairs — пары площадей, где первая не должна превышать вторую
// (ключи реестра).
var areaPairs = [][2]string{{"area_sqm", "total_area"}}

// validateAttrs — проверяет атрибуты ответа по реестру. Возвращает
// прошедшие (типизированные значения) и число отброшенных. known —
// уже сохранённые значения объекта (attributes JSONB) для проверки
// консистентности площадей.
func validateAttrs(got map[string]*extractedAttr, specs map[string]attrSpec, maxConf float64, known map[string]any) (out []validated, dropped int) {
	for key, e := range got {
		if e == nil || e.Value == nil {
			dropped++
			continue
		}
		spec, ok := specs[key]
		if !ok {
			// Модели просят только атрибуты из реестра; чужой ключ —
			// галлюцинация. Реестр — единственный источник допустимых
			// атрибутов (ТЗ §0.10).
			dropped++
			continue
		}
		if e.Confidence == nil || *e.Confidence < 0 || *e.Confidence > 1 {
			dropped++
			continue
		}
		val, ok := normValue(e.Value, spec)
		if !ok {
			dropped++
			continue
		}
		// obytná/užitná plocha — жилая (area_sqm), не общая (total_area).
		// Модель на 23141 записала 57 m² из «obytnou plochu» как total_area.
		if key == "total_area" && quoteIsLivingArea(e.Quote+" "+e.Pattern) {
			dropped++
			continue
		}
		out = append(out, validated{
			Key:        key,
			Value:      val,
			Confidence: math.Min(*e.Confidence, maxConf),
		})
	}

	// Консистентность площадей — по состоянию объекта ПОСЛЕ записи:
	// ответ плюс уже сохранённые.
	merged := make(map[string]any, len(known)+len(out))
	for k, v := range known {
		merged[k] = v
	}
	for _, v := range out {
		merged[v.Key] = v.Value
	}
	bad := map[string]bool{}
	for _, pair := range areaPairs {
		a, ea := toFloat(merged[pair[0]])
		b, eb := toFloat(merged[pair[1]])
		if !ea || !eb {
			continue // не числа — проверить нечем
		}
		if a > b {
			// Жилая больше общей — данные недостоверны: оба значения
			// отбрасываем, а не «чиним».
			bad[pair[0]] = true
			bad[pair[1]] = true
		}
	}
	if len(bad) > 0 {
		kept := out[:0]
		for _, v := range out {
			if bad[v.Key] {
				dropped++
				continue
			}
			kept = append(kept, v)
		}
		out = kept
	}
	return out, dropped
}

func quoteIsLivingArea(q string) bool {
	s := strings.ToLower(q)
	living := strings.Contains(s, "obytn") || strings.Contains(s, "užitn") || strings.Contains(s, "uzitn")
	total := strings.Contains(s, "celkov") || strings.Contains(s, "total")
	return living && !total
}

// normValue — приводит значение ответа к типу реестра или сообщает,
// что проверка провалена. int → int64, float → float64, bool → bool,
// enum → строка в нижнем регистре.
func normValue(v any, spec attrSpec) (any, bool) {
	switch spec.DataType {
	case "bool":
		switch t := v.(type) {
		case bool:
			return t, true
		case string:
			t = strings.ToLower(strings.TrimSpace(t))
			if t == "true" || t == "false" {
				return t == "true", true
			}
		}
		return nil, false
	case "enum":
		s, ok := v.(string)
		if !ok {
			return nil, false
		}
		s = strings.ToLower(strings.TrimSpace(s))
		for _, allowed := range spec.Values {
			if s == allowed {
				return s, true
			}
		}
		return nil, false
	case "int":
		f, ok := toFloat(v)
		if !ok || f != math.Trunc(f) || !inRange(f, spec.Min, spec.Max) {
			return nil, false
		}
		return int64(f), true
	case "float":
		f, ok := toFloat(v)
		if !ok || !inRange(f, spec.Min, spec.Max) {
			return nil, false
		}
		return f, true
	}
	return nil, false
}

// inRange — значение в границах реестра; nil — граница не задана
// (проверка по ней не выполняется: домысливать нельзя, ТЗ §0.10).
func inRange(f float64, min, max *float64) bool {
	if min != nil && f < *min {
		return false
	}
	if max != nil && f > *max {
		return false
	}
	return true
}

// toFloat — число из ответа модели: number из unmarshal (float64/int64)
// или строка с числом. Модели иногда присылают числа в кавычках —
// принимаем, но дальше строгая проверка типа и диапазона.
func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int64:
		return float64(t), true
	case string:
		s, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, false
		}
		return s, true
	default:
		return 0, false
	}
}

// promptAttrs — описание запрошенных атрибутов для промпта: тип,
// допустимые значения, границы. Модель получает только реестр
// (ТЗ v4 §4.3), никаких «разумных значений» из кода.
func promptAttrs(specs []attrSpec) string {
	var b strings.Builder
	for _, s := range specs {
		fmt.Fprintf(&b, "- %s (%s)", s.Key, s.DataType)
		if s.Label != "" {
			fmt.Fprintf(&b, ": %s", s.Label)
		}
		if len(s.Values) > 0 {
			fmt.Fprintf(&b, "; allowed values: %s", strings.Join(s.Values, ", "))
		}
		if s.Min != nil {
			fmt.Fprintf(&b, "; min: %g", *s.Min)
		}
		if s.Max != nil {
			fmt.Fprintf(&b, "; max: %g", *s.Max)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

var areaKeys = []string{"area_sqm", "total_area", "plot_area"}

func promptKnownAreas(attrs map[string]any) string {
	if len(attrs) == 0 {
		return ""
	}
	var lines []string
	for _, k := range areaKeys {
		v, ok := attrs[k]
		if !ok || v == nil {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s = %v", k, v))
	}
	if len(lines) == 0 {
		return ""
	}
	return "Already known area values (do not copy a number into a different area key unless the text names a different kind of area):\n" +
		strings.Join(lines, "\n") + "\n\n"
}

func buildUserPrompt(desc string, missing []attrSpec, known map[string]any) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Listing text (original):\n\"\"\"%s\"\"\"\n\n", desc)
	b.WriteString(promptKnownAreas(known))
	fmt.Fprintf(&b, "Requested attributes (key (type); constraints from the attribute registry):\n%s\n\n", promptAttrs(missing))
	b.WriteString(`Respond with JSON: {"<key>": {"value": ..., "confidence": ..., "quote": <verbatim phrase from the listing>, "pattern": <shortest verbatim substring of quote carrying the signal>}, ...}`)
	return b.String()
}
