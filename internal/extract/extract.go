// extract.go — LLM-извлечение атрибутов из описаний объявлений
// (ТЗ v4 §4.3, шаг 3 порядка получения значений §4.2).
//
// Выполняется ТОЛЬКО для атрибутов, не полученных на шагах 1–2
// (source_field/rule): кандидат — активный объект с детальным текстом
// и хотя бы одним атрибутом реестра страны, которого нет ни в
// objects.attributes, ни в attribute_values.
//
// Модели передаются оригинальный текст объявления и перечень
// запрашиваемых атрибутов с типами и допустимыми значениями из
// реестра; инструкция — пустое значение, если информации нет.
// Ответ программно валидируется по реестру (validate.go); провалив
// значение отбрасывается, а не «исправляется».
//
// Идемпотентность (ТЗ v4 §4.3): повторное извлечение того же текста
// той же моделью той же версией перечня не выполняется — ключ
// llm_extractions (object_id, model, attr_list_version, source_hash),
// source_hash = sha256(description_original). Строка пишется на любое
// успешное обращение к модели — в том числе с пустым/непарсеющимся
// ответом: повторный вызов по той же тройке бесполезен, а без записи
// объект стал бы «ядовитым» (бесконечный расход токенов).
//
// Бюджет: расход — SUM(llm_extractions.tokens); при исчерпании
// budget_tokens прогон останавливается, необработанные задачи
// остаются в очереди (кандидаты не помечаются выполненными —
// llm_extractions-строка не пишется).
package extract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/attrules"
)

// Config — параметры прогона (зеркало config.Config.Extract;
// передаётся явно, чтобы пакет не зависел от всего конфига).
type Config struct {
	BaseURL         string
	APIKey          string
	Model           string
	TimeoutSec      int
	MaxConfidence   float64
	AttrListVersion string
	MaxChars        int
	BudgetTokens    int // 0 — без лимита
	// MaxTokens — потолок completion. 0 → 1024 (эмпирика 2026-09-11:
	// объект 23141 без лимита оборвал JSON, total_tokens=1227).
	MaxTokens int
	// Мининг (ТЗ v4 §4.3, гибридное извлечение): LLM-успех с дословным
	// доказательством (quote/pattern) → mined-шаблон в attribute_templates.
	MinedMinOccurrences int     // порога повторений до промоушна (rules.*)
	MinedConfidenceMax  float64 // потолок уверенности mined-шаблонов
	MinedExampleObjects int     // id примеров, хранимых для ревизии
	MinedAutoPromote    bool    // авто-промоушн в конце прогона (rules.auto_promote); false — удержать до ревизии
	// Теневой контроль (C8): доля (%, 0-100) объектов, обработанных
	// промоутнутым шаблоном, которые дополнительно уходят в LLM;
	// расхождение — авто-демоушн шаблона. 0 — выключено.
	// (rules.shadow_sample_pct)
	ShadowSamplePct int
}

// Report — результат прогона.
type Report struct {
	Candidates  int  // кандидатов выбрано
	Extracted   int  // успешных обращений к модели
	Skipped     int  // идемпотентность: уже извлекалось
	TooLong     int  // описание длиннее max_chars — не отправлялось
	NoMissing   int  // пропавших атрибутов не оказалось (гонка с enrich)
	CallErrors  int  // ошибки обращения — повторится в следующем прогоне
	ParseFailed int  // модель ответила, но ответ не JSON — значений нет
	Valid       int  // значений записано
	Dropped     int  // значений отброшено проверкой по реестру
	Tokens      int  // токенов потрачено этим прогоном
	BudgetHit   bool // бюджет исчерпан — прогон остановлен
	// Мининг: шаблонов создано/усилено; значений без доказательной цитаты
	// (не минятся); candidate → active в конце прогона (только если
	// авто-промоушн включён); готово к промоушну, ждёт ревизии (если удержан).
	Mined    int
	NoQuote  int
	Promoted int
	Ready    int
	// Теневой контроль (C8): строк теневого контроля завершено
	// (LLM-значение совпало), расхождений (расхождение — авто-демоушн
	// шаблона; включает пустой ответ LLM), осталось pending (бюджет
	// исчерпан / остановка — повторится в следующем прогоне), ошибок
	// запроса реестра (строки остаются pending).
	ShadowDone     int
	ShadowMismatch int
	ShadowPending  int
	ShadowErrors   int
	// MirrorFilled (D15): пустых зеркальных колонок заполнено
	// (area_sqm/rooms/seller_type) — LLM-путь пишет зеркала так же, как
	// enrich и attrules, чтобы метрики «по колонке» не занижались.
	MirrorFilled int
}

const systemPrompt = `You extract structured attributes from real-estate listings.
Respond with a JSON object only — no comments, no markdown, no extra text.
Format: {"<key>": {"value": <value of the type requested>, "confidence": <number 0..1>, "quote": <verbatim phrase from the listing stating the value>, "pattern": <the shortest verbatim substring of "quote" that alone carries the signal>}}.
Include an attribute ONLY if its value is stated in the listing text AND you can quote the exact phrase that states it; omit every other attribute — guessing is forbidden.
"quote" must be copied verbatim from the listing (same characters, including any diacritics, at most 120 characters). "pattern" must be a verbatim substring of "quote". If no quotable phrase exists, omit the attribute entirely.
Confidence: how directly the text states the value (1 = stated explicitly, 0.5 = follows unambiguously, below 0.5 — omit).

Area keys are different quantities. Never copy one number into another key just because the magnitudes match.
- area_sqm: living/usable interior area only. Czech: obytná plocha, užitná plocha. Not celková, not pozemek.
- total_area: gross/total area of the building or unit. Czech: celková plocha, celková výměra of the house/flat (not the land). Never copy obytná/užitná into total_area. If the listing only states obytná/užitná, omit total_area.
- plot_area: land/garden/plot. Czech: pozemek, plocha pozemku, zahrada, parcela. Never a building floor area.
If the same phrase would fill two area keys with the same number, keep only the key whose definition matches that phrase.`

// Run — прогон извлечения по кандидатам (limit <= 0 — все;
// country — пусто или ISO-код). Останавливается с ошибкой на
// ErrPermanent (неверный ключ и прочие 4xx: прогон дальше бессмыслен).
func Run(ctx context.Context, pool *pgxpool.Pool, c *Config, limit int, country string) (*Report, error) {
	country = strings.ToUpper(strings.TrimSpace(country))
	if country != "" && len(country) != 2 {
		return nil, fmt.Errorf("extract: country: код из двух букв, получено %q", country)
	}
	rep := &Report{}

	// Бюджет (ТЗ v4 §4.3): расход за всё время — SUM(llm_extractions.tokens).
	var spent int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(tokens), 0) FROM llm_extractions`).Scan(&spent); err != nil {
		return nil, fmt.Errorf("extract: бюджет: %w", err)
	}
	if c.BudgetTokens > 0 && int(spent) >= c.BudgetTokens {
		rep.BudgetHit = true
		return rep, nil
	}

	client := newClient(c)

	cands, err := candidates(ctx, pool, limit, country)
	if err != nil {
		return nil, err
	}
	rep.Candidates = len(cands)
	if len(cands) == 0 {
		// Теневой контроль (C8) — независимо от извлекательской очереди:
		// теневые строки ставят проходы enrich и rules, а не extract.
		var remaining int
		if c.BudgetTokens > 0 {
			remaining = c.BudgetTokens - int(spent)
			if remaining < 0 {
				remaining = 0
			}
		}
		if err := shadowInto(rep, ctx, pool, client, c, remaining); err != nil {
			return nil, err
		}
		return rep, nil
	}
	log.Printf("extract: кандидатов %d", len(cands))

	// Реестр по странам кандидатов: один запрос (ТЗ v4 §4.3 — перечень
	// атрибутов и допустимые значения модель получает из реестра).
	reg, err := registryByCountry(ctx, pool, countriesOf(cands))
	if err != nil {
		return nil, err
	}
	// Провенанс: какие атрибуты уже у каждого объекта (attribute_values).
	avKeys, err := attrValuesKeys(ctx, pool, idsOf(cands))
	if err != nil {
		return nil, err
	}

	for i, o := range cands {
		if ctx.Err() != nil {
			return rep, ctx.Err()
		}
		specs := reg[o.country]
		if err := extractOne(ctx, pool, client, c, o, specs, avKeys[o.id], rep); err != nil {
			return rep, err
		}
		if (i+1)%50 == 0 {
			log.Printf("extract: %d/%d (вызовов %d, значений %d, отброшено %d, токенов %d)",
				i+1, len(cands), rep.Extracted, rep.Valid, rep.Dropped, rep.Tokens)
		}
		// Бюджет в полёте: исчерпан — задачи остаются в очереди.
		if c.BudgetTokens > 0 && int(spent)+rep.Tokens >= c.BudgetTokens {
			rep.BudgetHit = true
			log.Printf("extract: бюджет %d токенов исчерпан — прогон остановлен, необработанные задачи остаются в очереди", c.BudgetTokens)
			break
		}
	}
	// Теневой контроль (C8) — до промоушна: pending-строки очереди —
	// реальные LLM-вызовы; расхождение — авто-демоушн шаблона. Токены
	// считаются в бюджет прогона (но не пишутся в llm_extractions:
	// это не извлечение, а проверка — PK там (object_id, model,
	// attr_list_version, source_hash) был бы занят основным прогоном).
	{
		remaining := 0
		if c.BudgetTokens > 0 {
			remaining = c.BudgetTokens - int(spent) - rep.Tokens
			if remaining < 0 {
				remaining = 0
			}
		}
		if err := shadowInto(rep, ctx, pool, client, c, remaining); err != nil {
			return nil, err
		}
	}
	// Промоушн (конец прогона). По умолчанию (rules.auto_promote=false)
	// держать до ревизии: mined-кандидаты НЕ применяются к корпусу, пока
	// оператор не включит их руками (pb rules set <id> --status active).
	// При включённом авто-промоушне кандидат проходит холдаут-проверку
	// (C8): последние 2 подтверждения — шаблон обязан срабатывать в
	// Apply и совпадать с LLM-значением на каждом. Промоушн по стране
	// прогона: мининг в прогоне — только своей страны, чужие кандидаты
	// не трогаем (и live-тест на VX не промоутит реальных).
	if c.MinedAutoPromote {
		promoted, err := attrules.Promote(ctx, pool, country, c.MinedMinOccurrences)
		if err != nil {
			return nil, fmt.Errorf("extract: промоушн шаблонов: %w", err)
		}
		if promoted > 0 {
			rep.Promoted = promoted
			log.Printf("extract: промоутед шаблонов %d", promoted)
		}
	} else {
		ready, err := attrules.ReadyCount(ctx, pool, country, c.MinedMinOccurrences)
		if err != nil {
			return nil, fmt.Errorf("extract: готовых к промоушну: %w", err)
		}
		rep.Ready = ready
		log.Printf("extract: готово к промоушну (ждёт ревизии, auto_promote=false) %d", ready)
	}
	return rep, nil
}

// candObj — кандидат: объект с полным текстом объявления.
type candObj struct {
	id      int64
	country string
	desc    string
	attrs   map[string]any // objects.attributes
}

// candidateFilter — условие кандидата (ТЗ v4 §4.3): есть детальная
// страница, непустое описание, и хотя бы один атрибут реестра страны,
// не полученный на шагах 1–2 (ни в attribute_values, ни в attributes).
// c — плейсхолдер аргумента страны.
func candidateFilter(c string) string {
	return `o.status = 'active'
		AND o.detail_fetched_at IS NOT NULL
		AND o.description_original IS NOT NULL
		AND o.description_original <> ''
		AND (` + c + `::text = '' OR o.country = ` + c + `)
		AND EXISTS (
		    SELECT 1 FROM attribute_registry ar
		    WHERE ar.country = o.country
		      AND NOT EXISTS (
		          SELECT 1 FROM attribute_values av
		          WHERE av.object_id = o.id AND av.attr_key = ar.key)
		      AND NOT (o.attributes ? ar.key))`
}

func candidates(ctx context.Context, pool *pgxpool.Pool, limit int, country string) ([]candObj, error) {
	args := []any{}
	ph := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	c := ph(country)
	q := "SELECT o.id, o.country, o.description_original, o.attributes\nFROM objects o\nWHERE " +
		candidateFilter(c) + "\nORDER BY o.id"
	if limit > 0 {
		q += " LIMIT " + ph(limit)
	}
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("extract: кандидаты: %w", err)
	}
	defer rows.Close()
	var out []candObj
	for rows.Next() {
		var o candObj
		var attrsRaw []byte
		if err := rows.Scan(&o.id, &o.country, &o.desc, &attrsRaw); err != nil {
			return nil, fmt.Errorf("extract: чтение кандидатов: %w", err)
		}
		o.attrs = map[string]any{}
		if len(attrsRaw) > 0 {
			if err := json.Unmarshal(attrsRaw, &o.attrs); err != nil {
				return nil, fmt.Errorf("extract: attributes объекта %d: %w", o.id, err)
			}
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("extract: чтение кандидатов: %w", err)
	}
	return out, nil
}

// CountCandidates — число объектов, ожидающих извлечения (для status).
func CountCandidates(ctx context.Context, pool *pgxpool.Pool, country string) (int, error) {
	args := []any{}
	ph := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	q := "SELECT count(*) FROM objects o WHERE " + candidateFilter(ph(country))
	var n int
	err := pool.QueryRow(ctx, q, args...).Scan(&n)
	return n, err
}

func newClient(c *Config) *Client {
	to := time.Duration(c.TimeoutSec) * time.Second
	if to <= 0 {
		to = 60 * time.Second
	}
	mt := c.MaxTokens
	if mt <= 0 {
		mt = 1024
	}
	return &Client{
		BaseURL:   c.BaseURL,
		APIKey:    c.APIKey,
		Model:     c.Model,
		HTTP:      &http.Client{Timeout: to},
		MaxTokens: mt,
	}
}

// ProcessObject — шаг 3 (ТЗ v4 §4.3) для одного объекта: атрибуты
// реестра, которых нет после шагов 1–2. Без теневого контроля и
// промоушна (это хвост пакетного `pb extract run`).
func ProcessObject(ctx context.Context, pool *pgxpool.Pool, c *Config, objectID int64) (*Report, error) {
	rep := &Report{}
	var spent int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(tokens), 0) FROM llm_extractions`).Scan(&spent); err != nil {
		return nil, fmt.Errorf("extract: бюджет: %w", err)
	}
	if c.BudgetTokens > 0 && int(spent) >= c.BudgetTokens {
		rep.BudgetHit = true
		return rep, nil
	}
	o, err := loadObject(ctx, pool, objectID)
	if err != nil {
		return nil, err
	}
	if o.id == 0 {
		return nil, fmt.Errorf("extract: объект %d не найден", objectID)
	}
	if strings.TrimSpace(o.desc) == "" {
		return rep, nil
	}
	rep.Candidates = 1
	reg, err := registryByCountry(ctx, pool, []string{o.country})
	if err != nil {
		return nil, err
	}
	avKeys, err := attrValuesKeys(ctx, pool, []int64{o.id})
	if err != nil {
		return nil, err
	}
	if err := extractOne(ctx, pool, newClient(c), c, o, reg[o.country], avKeys[o.id], rep); err != nil {
		return rep, err
	}
	return rep, nil
}

func loadObject(ctx context.Context, pool *pgxpool.Pool, id int64) (candObj, error) {
	var o candObj
	var attrsRaw []byte
	var desc *string
	err := pool.QueryRow(ctx, `
		SELECT o.id, o.country, o.description_original, o.attributes
		FROM objects o WHERE o.id = $1`, id).Scan(&o.id, &o.country, &desc, &attrsRaw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return candObj{}, nil
		}
		return o, fmt.Errorf("extract: объект %d: %w", id, err)
	}
	if desc != nil {
		o.desc = *desc
	}
	o.attrs = map[string]any{}
	if len(attrsRaw) > 0 && string(attrsRaw) != "null" {
		if err := json.Unmarshal(attrsRaw, &o.attrs); err != nil {
			return o, fmt.Errorf("extract: attributes объекта %d: %w", id, err)
		}
	}
	return o, nil
}

// extractOne — одно обращение к модели по объекту. ErrPermanent
// пробрасывается; сетевые сбои — CallErrors в отчёте, не ошибка прогона.
func extractOne(ctx context.Context, pool *pgxpool.Pool, client *Client, c *Config, o candObj, specs map[string]attrSpec, avKeys map[string]bool, rep *Report) error {
	if c.MaxChars > 0 && utf8.RuneCountInString(o.desc) > c.MaxChars {
		rep.TooLong++
		return nil
	}
	hash := sha256hex(o.desc)
	done, err := alreadyExtracted(ctx, pool, o.id, hash, c)
	if err != nil {
		return fmt.Errorf("extract: идемпотентность: %w", err)
	}
	if done {
		rep.Skipped++
		return nil
	}
	if len(specs) == 0 {
		rep.NoMissing++
		return nil
	}
	missing := missingAttrs(specs, o.attrs, avKeys)
	if len(missing) == 0 {
		rep.NoMissing++
		return nil
	}

	user := buildUserPrompt(o.desc, missing, o.attrs)
	text, servedModel, tokens, err := client.Complete(ctx, systemPrompt, user)
	if err != nil {
		var perm *ErrPermanent
		if errors.As(err, &perm) {
			return fmt.Errorf("extract: %w", perm)
		}
		rep.CallErrors++
		log.Printf("extract: объект %d: %v", o.id, err)
		return nil
	}
	rep.Extracted++
	rep.Tokens += tokens

	if err := markExtracted(ctx, pool, o.id, c.Model, c.AttrListVersion, hash, tokens); err != nil {
		return fmt.Errorf("extract: учёт вызова: %w", err)
	}

	got, err := parseResponse(text)
	if err != nil {
		rep.ParseFailed++
		snip := strings.TrimSpace(text)
		if len(snip) > 240 {
			snip = snip[:240] + "…"
		}
		log.Printf("extract: объект %d: ответ модели не JSON: %v; начало %q", o.id, err, snip)
		return nil
	}
	valid, dropped := validateAttrs(got, specs, c.MaxConfidence, o.attrs)
	rep.Dropped += dropped
	if len(valid) == 0 {
		return nil
	}
	mirrors, err := save(ctx, pool, o.id, valid, servedModel, c.AttrListVersion, hash)
	if err != nil {
		return fmt.Errorf("extract: запись объекта %d: %w", o.id, err)
	}
	rep.Valid += len(valid)
	rep.MirrorFilled += mirrors
	for _, v := range valid {
		if attrules.MirrorKeys[v.Key] {
			continue
		}
		e := got[v.Key]
		if e == nil || e.Quote == "" || e.Pattern == "" {
			rep.NoQuote++
			continue
		}
		conf := math.Min(v.Confidence, c.MinedConfidenceMax)
		mined, err := attrules.Mine(ctx, pool, o.country, v.Key, v.Value,
			conf, o.desc, e.Quote, e.Pattern, o.id, c.MinedExampleObjects)
		if err != nil {
			return fmt.Errorf("extract: мининг объекта %d %s: %w", o.id, v.Key, err)
		}
		if mined {
			rep.Mined++
		}
	}
	return nil
}

func countriesOf(cands []candObj) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range cands {
		if !seen[c.country] {
			seen[c.country] = true
			out = append(out, c.country)
		}
	}
	return out
}

func idsOf(cands []candObj) []int64 {
	out := make([]int64, len(cands))
	for i, c := range cands {
		out[i] = c.id
	}
	return out
}

// registryByCountry — реестр атрибутов по странам (ключ — страна).
// allowed_values: JSONB-массив строк; null/пусто — для bool
// значения "false"/"true", для enum — реестр неполон (ошибка ниже).
func registryByCountry(ctx context.Context, pool *pgxpool.Pool, countries []string) (map[string]map[string]attrSpec, error) {
	rows, err := pool.Query(ctx, `
		SELECT country, key, data_type, allowed_values, value_min, value_max, label_en
		FROM attribute_registry
		WHERE country = ANY($1)
		ORDER BY country, key`, countries)
	if err != nil {
		return nil, fmt.Errorf("extract: реестр: %w", err)
	}
	defer rows.Close()
	out := map[string]map[string]attrSpec{}
	for rows.Next() {
		var (
			ctr, key, dt string
			allowed      []byte
			min, max     *float64
			label        *string
		)
		if err := rows.Scan(&ctr, &key, &dt, &allowed, &min, &max, &label); err != nil {
			return nil, fmt.Errorf("extract: чтение реестра: %w", err)
		}
		spec := attrSpec{Key: key, DataType: dt, Min: min, Max: max}
		if label != nil {
			spec.Label = strings.TrimSpace(*label)
		}
		if len(allowed) > 0 && string(allowed) != "null" {
			if err := json.Unmarshal(allowed, &spec.Values); err != nil {
				return nil, fmt.Errorf("extract: allowed_values атрибута %s: %w", key, err)
			}
		}
		switch dt {
		case "bool":
			if len(spec.Values) == 0 {
				spec.Values = []string{"false", "true"}
			}
		case "enum":
			if len(spec.Values) == 0 {
				return nil, fmt.Errorf("extract: атрибут %s (enum) без allowed_values — реестр неполон", key)
			}
		case "int", "float":
		default:
			return nil, fmt.Errorf("extract: неизвестный data_type %q атрибута %s (реестр)", dt, key)
		}
		if out[ctr] == nil {
			out[ctr] = map[string]attrSpec{}
		}
		out[ctr][key] = spec
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("extract: чтение реестра: %w", err)
	}
	return out, nil
}

// attrValuesKeys — attribute_values по объектам (провенанс шагов 1–2
// и предыдущих прогонов).
func attrValuesKeys(ctx context.Context, pool *pgxpool.Pool, ids []int64) (map[int64]map[string]bool, error) {
	out := map[int64]map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT object_id, attr_key FROM attribute_values
		WHERE object_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("extract: provenance: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var oid int64
		var key string
		if err := rows.Scan(&oid, &key); err != nil {
			return nil, fmt.Errorf("extract: чтение provenance: %w", err)
		}
		if out[oid] == nil {
			out[oid] = map[string]bool{}
		}
		out[oid][key] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("extract: чтение provenance: %w", err)
	}
	return out, nil
}

// missingAttrs — атрибуты реестра, которых нет у объекта ни в
// attributes, ни в attribute_values (ТЗ v4 §4.3: только то, что не
// получено на шагах 1–2).
func missingAttrs(specs map[string]attrSpec, attrs map[string]any, avKeys map[string]bool) []attrSpec {
	var out []attrSpec
	for key, s := range specs {
		if _, has := attrs[key]; has {
			continue
		}
		if avKeys[key] {
			continue
		}
		out = append(out, s)
	}
	// Стабильный порядок для детерминированного промпта.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Key < out[i].Key {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func alreadyExtracted(ctx context.Context, pool *pgxpool.Pool, objID int64, hash string, c *Config) (bool, error) {
	var ok bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM llm_extractions le
		    WHERE le.object_id = $1 AND le.source_hash = $2
		      AND le.attr_list_version = $3 AND le.model = $4)`,
		objID, hash, c.AttrListVersion, c.Model).Scan(&ok)
	return ok, err
}

// markExtracted — учёт вызова (идемпотентность + бюджет). model —
// модель из конфига (стабильная часть ключа); реальное имя,
// обслужившее запрос, — в attribute_values.model (провенанс).
func markExtracted(ctx context.Context, pool *pgxpool.Pool, objID int64, model, listVersion, hash string, tokens int) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO llm_extractions (object_id, model, attr_list_version, source_hash, tokens)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (object_id, model, attr_list_version, source_hash) DO NOTHING`,
		objID, model, listVersion, hash, tokens)
	return err
}

// parseResponse — ответ модели: JSON {key: {value, confidence}}.
// markdown-ограды («```json … ```») — отбрасываются: модели иногда
// оборачивают JSON в fence, сам JSON при этом валиден.
func parseResponse(text string) (map[string]*extractedAttr, error) {
	s := strings.TrimSpace(text)
	// Qwen thinking: даже с enable_thinking=false иногда приходит
	// <think>…</think> перед JSON.
	if i := strings.Index(s, "</think>"); i >= 0 {
		s = strings.TrimSpace(s[i+len("</think>"):])
	}
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
		s = strings.TrimSpace(s)
	}
	if i := strings.IndexByte(s, '{'); i >= 0 {
		if j := strings.LastIndexByte(s, '}'); j > i {
			s = s[i : j+1]
		}
	}
	out := map[string]*extractedAttr{}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("%v", err)
	}
	return out, nil
}

// save — запись проверенных значений: слияние в objects.attributes
// (типизированные значения) + provenance в attribute_values + зеркальные
// колонки (D15: LLM-путь согласован с enrich/attrules — только в ПУСТУЮ
// колонку, IS NULL в WHERE: атомарно и идемпотентно, «уже полученное
// значение не переписывается»). Возвращает число заполненных зеркал.
// ON CONFLICT DO NOTHING: приоритет источников §4.2 — если между
// выбором кандидата и записью enrich положил rule-значение,
// llm_extraction его не затирает.
func save(ctx context.Context, pool *pgxpool.Pool, objID int64, valid []validated, servedModel, listVersion, hash string) (mirrors int, err error) {
	merged := map[string]any{}
	for _, v := range valid {
		merged[v.Key] = v.Value
	}
	mergedJSON, err := json.Marshal(merged)
	if err != nil {
		return 0, fmt.Errorf("attributes JSON: %w", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		UPDATE objects SET attributes = attributes || $2::jsonb
		WHERE id = $1`, objID, mergedJSON); err != nil {
		return 0, fmt.Errorf("attributes: %w", err)
	}
	for _, v := range valid {
		if _, err := tx.Exec(ctx, `
			INSERT INTO attribute_values
			    (object_id, attr_key, extracted_by, confidence, model, attr_list_version, source_hash)
			VALUES ($1, $2, 'llm_extraction', $3, $4, $5, $6)
			ON CONFLICT (object_id, attr_key) DO NOTHING`,
			objID, v.Key, v.Confidence, servedModel, listVersion, hash); err != nil {
			return 0, fmt.Errorf("attribute_values %s: %w", v.Key, err)
		}
	}
	// Зеркальные колонки (D15): типы уже проверены реестром (normValue):
	// area_sqm — float64, rooms — int64, seller_type — строка из
	// допустимого набора. IS NULL в WHERE — атомарно и идемпотентно:
	// «уже полученное значение не переписывается», как в enrich/attrules.
	for _, v := range valid {
		var (
			tag pgconn.CommandTag
		)
		switch v.Key {
		case "area_sqm":
			f, ok := v.Value.(float64)
			if !ok {
				continue
			}
			tag, err = tx.Exec(ctx, `
			UPDATE objects SET area_sqm = $2
			WHERE id = $1 AND area_sqm IS NULL`, objID, f)
		case "rooms":
			n, ok := v.Value.(int64)
			if !ok {
				continue
			}
			tag, err = tx.Exec(ctx, `
			UPDATE objects SET rooms = $2
			WHERE id = $1 AND rooms IS NULL`, objID, n)
		case "seller_type":
			s, ok := v.Value.(string)
			if !ok {
				continue
			}
			tag, err = tx.Exec(ctx, `
			UPDATE objects SET seller_type = $2
			WHERE id = $1 AND seller_type IS NULL`, objID, s)
		default:
			continue // остальные атрибуты: JSONB — единственное хранение
		}
		if err != nil {
			return 0, fmt.Errorf("mirror %s объекта %d: %w", v.Key, objID, err)
		}
		if tag.RowsAffected() > 0 {
			mirrors++
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return mirrors, nil
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
