package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"propertyboss/internal/fx"
	"propertyboss/internal/money"
)

// priceDisplay — конвертированная сумма для отображения.
// Всегда derived: в БД цена живёт в валюте рынка (ТЗ §5).
type priceDisplay struct {
	Minor     int64  `json:"minor"`
	Currency  string `json:"currency"`
	Derived   bool   `json:"derived"`
	RateDate  string `json:"rate_date"`
	RateStale bool   `json:"rate_stale"`
}

// valuationOut — последняя оценка объекта (valuations, этап 5, ТЗ §7.3):
// число (price_deviation) хранится вместе с интервалом, размером выборки
// и качеством модели; в дашборде число без интервала не показывается.
type valuationOut struct {
	ModelVersion      *string    `json:"model_version"`
	PriceDeviation    *float64   `json:"price_deviation"`
	NullReason        *string    `json:"null_reason,omitempty"`
	PredictedMinor    *int64     `json:"predicted_price_minor"`
	IntervalLowMinor  *int64     `json:"interval_low_minor"`
	IntervalHighMinor *int64     `json:"interval_high_minor"`
	SampleSize        int        `json:"sample_size"`
	RSquared          *float64   `json:"r_squared"`
	ZoneFallback      bool       `json:"zone_fallback"`
	ComputedAt        *time.Time `json:"computed_at"`
}

// hazardOut — вероятность ухода объекта с рынка за horizon_days дней
// (liquidity_estimates, этап 7, ТЗ §9.2–9.3): NULL с причиной, пока
// модель не опубликована; предсказанное — уход с рынка, не продажа.
type hazardOut struct {
	HorizonDays      *int       `json:"horizon_days"`
	Probability      *float64   `json:"probability"`
	NullReason       *string    `json:"null_reason,omitempty"`
	ModelVersion     *string    `json:"model_version"`
	EventsInTraining int        `json:"events_in_training"`
	ComputedAt       *time.Time `json:"computed_at"`
}

// translationOut — сохранённый перевод (object_translations, этап 10,
// ТЗ §11): UI читает из БД, без обращения к LLM. Строки без перевода
// нет → поле null, UI показывает оригинал с пометкой «перевод недоступен».
type translationOut struct {
	Text         string    `json:"text"`
	Model        string    `json:"model"`
	TokenCost    *int      `json:"token_cost,omitempty"`
	TranslatedAt time.Time `json:"translated_at"`
}

// pricePointOut — запись истории цены (price_history, TZ_UI v3 §6.3.2):
// момент, когда цена стала известной системе, и её значение в валюте рынка.
type pricePointOut struct {
	PriceMinor int64     `json:"price_minor"`
	Currency   string    `json:"currency"`
	ChangeAt   time.Time `json:"change_at"`
}

// listingOut — объявление объекта на площадке (object_listings →
// raw_listings → sources, TZ_UI v3 §6.3.6): по одному на каждый
// источник объявления, с URL для кнопки на площадку.
type listingOut struct {
	SourceID   string  `json:"source_id"`
	SourceName string  `json:"source_name"`
	ExternalID string  `json:"external_id"`
	SourceURL  *string `json:"source_url,omitempty"`
}

// photoOut — фотография объекта (object_photos, TZ v4 §4.4):
// оригинальная ссылка всегда есть; локальные копии (если скачаны)
// отдаёт pb serve под /photos — card (крупный) и list (мелкий) размер.
type photoOut struct {
	Ord         int     `json:"ord"`
	OriginalURL string  `json:"original_url"`
	CardURL     *string `json:"card_url,omitempty"`
	ListURL     *string `json:"list_url,omitempty"`
}

// photoURL — относительный путь копии (file_card/file_list) → URL /photos.
func photoURL(rel *string) *string {
	if rel == nil || *rel == "" {
		return nil
	}
	u := "/photos/" + *rel
	return &u
}

type objectOut struct {
	ID         int64   `json:"id"`
	Country    string  `json:"country"`
	DealType   string  `json:"deal_type"`
	ZoneID     *int64  `json:"zone_id"`
	ZoneName   *string `json:"zone_name,omitempty"`
	ZoneLevel  *string `json:"zone_level,omitempty"`
	ZoneSource *string `json:"zone_source,omitempty"`
	// U25 (TZ UI v5): координаты и класс геокода в ответе.
	// Lat/Lng непустые только при GeomKind = point. C37: geo_level убран —
	// колонки в схеме нет, дублировал geo_confidence.
	Lat                *float64       `json:"lat"`
	Lng                *float64       `json:"lng"`
	GeoConfidence      *string        `json:"geo_confidence,omitempty"`
	GeomKind           string         `json:"geom_kind"`
	Title              *string        `json:"title,omitempty"`
	Address            *string        `json:"address"`
	Attributes         map[string]any `json:"attributes,omitempty"`
	AttributesUnmapped map[string]any `json:"attributes_unmapped,omitempty"`
	AreaSqM            *float64       `json:"area_sqm"`
	Rooms              *int16         `json:"rooms"`
	PropertyType       *string        `json:"property_type"`
	// SellerType/SellerName — тип продавца и название агентства
	// (TZ v4 §4.7, §6.4 п.7): колонки objects уже заполнены экстракцией,
	// в UI — строка списка (тип) и блок «Продавец» экрана объекта.
	SellerType          *string         `json:"seller_type,omitempty"`
	SellerName          *string         `json:"seller_name,omitempty"`
	PriceMinor          *int64          `json:"price_minor"`
	Currency            *string         `json:"currency"`
	PriceDisplay        *priceDisplay   `json:"price_display,omitempty"`
	DescriptionOriginal *string         `json:"description_original"`
	LanguageOriginal    *string         `json:"language_original"`
	TranslationRu       *translationOut `json:"translation_ru,omitempty"`
	TranslationEn       *translationOut `json:"translation_en,omitempty"`
	Valuation           *valuationOut   `json:"valuation,omitempty"`
	// NotModeled (C25): объект без уровня геокода — структурно не
	// моделируем; публикуемых оценок не несёт (Valuation принудительно
	// nil), флаг показывается в UI.
	NotModeled     bool       `json:"not_modeled"`
	Hazard         *hazardOut `json:"hazard,omitempty"`
	Status         string     `json:"status"`
	DelistedReason *string    `json:"delisted_reason"`
	FirstSeenAt    time.Time  `json:"first_seen_at"`
	LastSeenAt     time.Time  `json:"last_seen_at"`
	DelistedAt     *time.Time `json:"delisted_at"`
	// PriceTrend — признак изменения цены с первого наблюдения:
	// "reduced" | "increased" (TZ_UI v3 §6.2: признак состояния в списке).
	PriceTrend *string `json:"price_trend,omitempty"`
	// Cover — первое фото (обложка карточки в списке, TZ v4 §4.4).
	Cover *photoOut `json:"cover_photo,omitempty"`
	// PriceHistory, Photos и Listings запрашиваются только для экрана
	// объекта (TZ_UI v3 §6.3.2, §6.3.6, TZ v4 §4.4); в списке остаются
	// пустыми (omitempty).
	PriceHistory []pricePointOut `json:"price_history,omitempty"`
	Photos       []photoOut      `json:"photos,omitempty"`
	Listings     []listingOut    `json:"listings,omitempty"`
}

// objectsSelect/objectsFrom — с LEFT JOIN zones (имя/уровень/источник зоны
// рядом с объектом, ТЗ §13), LEFT JOIN LATERAL последней оценки
// (ТЗ §7.3: отклонение всегда с интервалом и размерами выборки) и
// LEFT JOIN LATERAL сохранённых переводов ru/en (ТЗ §11: UI читает из БД).
// Перевод показывается только свежий (source_hash = хеш ТЕКУЩЕГО
// описания): устаревший перевод показывать как текущий — подстановка.
// Квалификация objects.* обязательна: в zones те же имена колонок.
const objectsSelect = `objects.id, objects.country, objects.deal_type, objects.zone_id, objects.title, objects.address, objects.area_sqm, objects.rooms, objects.property_type,
	objects.seller_type, objects.seller_name,
	objects.current_price_minor, objects.currency, objects.status, objects.delisted_reason, objects.first_seen_at, objects.last_seen_at, objects.delisted_at,
	ph.first_price, ph.last_price, ph.n_points,
	z.name AS zone_name, z.level AS zone_level, z.source AS zone_source,
	v.model_version, v.price_deviation, v.deviation_null_reason, v.predicted_price_minor,
	v.interval_low_minor, v.interval_high_minor, v.sample_size, v.r_squared, v.zone_fallback, v.computed_at,
	h.horizon_days, h.hazard_probability, h.null_reason, h.model_version, h.events_in_training, h.computed_at,
	objects.description_original, objects.language_original,
	tr.text AS tr_text, tr.model AS tr_model, tr.token_cost AS tr_tokens, tr.translated_at AS tr_at,
	te.text AS te_text, te.model AS te_model, te.token_cost AS te_tokens, te.translated_at AS te_at,
	cov.ord AS cov_ord, cov.original_url AS cov_url,
	cov.file_card AS cov_card, cov.file_list AS cov_list,
	objects.geo_confidence IS NULL AS not_modeled,
	objects.geo_confidence,
	ST_Y(objects.geom::geometry) AS geo_y,
	ST_X(objects.geom::geometry) AS geo_x`

const objectsFrom = ` FROM objects
		LEFT JOIN zones z ON z.id = objects.zone_id
		LEFT JOIN LATERAL (
			-- Первая/последняя известные цены (TZ_UI v3 §6.2: признак
			-- «цена снижена» в списке). Явные границы окон: результат
			-- не зависит от того, какую строку отберёт LIMIT 1.
			SELECT ph2.first_price, ph2.last_price, ph2.n_points FROM (
				SELECT FIRST_VALUE(price_minor) OVER w AS first_price,
				       LAST_VALUE(price_minor) OVER w AS last_price,
				       count(*) OVER () AS n_points
				FROM price_history
				WHERE object_id = objects.id
				WINDOW w AS (ORDER BY change_at, id ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)
			) ph2 LIMIT 1
		) ph ON true
		LEFT JOIN LATERAL (
			SELECT v2.* FROM valuations v2 WHERE v2.object_id = objects.id
			ORDER BY v2.computed_at DESC, v2.id DESC
			LIMIT 1
		) v ON true
		LEFT JOIN LATERAL (
			SELECT h2.* FROM liquidity_estimates h2 WHERE h2.object_id = objects.id
			ORDER BY h2.computed_at DESC, h2.horizon_days DESC
			LIMIT 1
		) h ON true
		LEFT JOIN LATERAL (
			SELECT t2.* FROM object_translations t2
			WHERE t2.object_id = objects.id AND t2.lang = 'ru'
			  AND t2.source_hash = encode(digest(objects.description_original, 'sha256'), 'hex')
		) tr ON true
		LEFT JOIN LATERAL (
			SELECT t2.* FROM object_translations t2
			WHERE t2.object_id = objects.id AND t2.lang = 'en'
			  AND t2.source_hash = encode(digest(objects.description_original, 'sha256'), 'hex')
		) te ON true
		LEFT JOIN LATERAL (
			-- Обложка — первое фото в опубликованном порядке (TZ v4 §4.4).
			SELECT ord, original_url, file_card, file_list FROM object_photos
			WHERE object_id = objects.id
			ORDER BY ord LIMIT 1
		) cov ON true`

// attachValuation — подвешивает последнюю оценку, если она есть
// (LEFT JOIN LATERAL → все v.* NULL, когда valuations пусто).
// sample_size/zone_fallback в таблице NOT NULL, но приходят NULL
// без строки — указатели защищают от падения scan.
func attachValuation(o *objectOut, val *valuationOut, size *int, fb *bool) {
	if val.ModelVersion == nil {
		return
	}
	val.SampleSize = 0
	if size != nil {
		val.SampleSize = *size
	}
	val.ZoneFallback = fb != nil && *fb
	o.Valuation = val
}

// attachHazard — последняя вероятность ухода с рынка, если она есть
// (LEFT JOIN LATERAL → все h.* NULL, когда прогноза ещё нет).
// events_in_training в таблице NOT NULL, но приходит NULL без строки.
func attachHazard(o *objectOut, hz *hazardOut, size *int) {
	if hz.ModelVersion == nil {
		return
	}
	hz.EventsInTraining = 0
	if size != nil {
		hz.EventsInTraining = *size
	}
	o.Hazard = hz
}

// attachPriceTrend — признак изменения цены с первого наблюдения
// (TZ_UI v3 §6.2: «цена снижена»/«цена повышена» в списке).
// Сравниваются первая и последняя известные цены из price_history;
// одна точка (или ноль) — изменения не было, признак не ставится.
func attachPriceTrend(o *objectOut, first, last *int64, n *int64) {
	if n == nil || *n < 2 || first == nil || last == nil || *first == *last {
		return
	}
	t := ""
	switch {
	case *last < *first:
		t = "reduced"
	case *last > *first:
		t = "increased"
	}
	if t != "" {
		o.PriceTrend = &t
	}
}

// attachTranslation — сохранённый перевод языка lang, если строка есть
// (LEFT JOIN LATERAL → все поля NULL без перевода). model и
// translated_at в таблице NOT NULL, но приходят NULL без строки —
// проверка по text (он же NOT NULL) достаточна.
func attachTranslation(o *objectOut, lang string, text, model *string, tokens *int, at *time.Time) {
	if text == nil || model == nil || at == nil {
		return
	}
	t := &translationOut{Text: *text, Model: *model, TokenCost: tokens, TranslatedAt: *at}
	if lang == "ru" {
		o.TranslationRu = t
	} else {
		o.TranslationEn = t
	}
}

// attachCover — обложка (первое фото), если строка есть. LEFT JOIN
// LATERAL: у объекта без фото LATERAL пуст, все cov.* NULL (включая
// ord — именно по нему падал scan, см. *int16 на вызывающей стороне).
// Guards на обе nil-группы: строка либо целиком есть, либо нет.
func attachCover(o *objectOut, ord *int16, url, card, list *string) {
	if url == nil || ord == nil {
		return
	}
	o.Cover = &photoOut{
		Ord:         int(*ord),
		OriginalURL: *url,
		CardURL:     photoURL(card),
		ListURL:     photoURL(list),
	}
}

// attachGeo — U25 (TZ UI v5): координаты и класс геокода в ответе.
// point — building/street с фактическими координатами; area — ниже уровня
// «улица», отдаётся вместе с zone_id и точкой не отображается никогда
// (TZ v4 §4.7); none — геокода нет (geo_confidence IS NULL) или координат
// в данных нет (conf = building/street, а geom NULL — неконсистентность,
// точкой не показываем). Во всех не-point случаях lat/lng = null (приёмка).
func attachGeo(o *objectOut, conf *string, y, x *float64) {
	o.GeomKind = "none"
	if conf == nil {
		return
	}
	o.GeoConfidence = conf
	switch *conf {
	case "building", "street":
		if y != nil && x != nil {
			o.GeomKind = "point"
			o.Lat = y
			o.Lng = x
		}
	case "municipality", "postcode", "zone":
		o.GeomKind = "area"
	}
}

// applyDisplay заполняет PriceDisplay суммой в displayTo по курсу на дату
// наблюдения (last_seen_at). Отсутствие курса — цена рынка остаётся,
// производного значения нет (ТЗ §5: не молчаливая подстановка).
func (s *Server) applyDisplay(ctx context.Context, o *objectOut, displayTo *money.Currency,
	conv func(minor int64, from money.Currency, onDate time.Time) (int64, *fx.RateLookup, error)) error {
	if displayTo == nil || conv == nil || o.PriceMinor == nil || o.Currency == nil || *o.Currency == displayTo.Code {
		return nil
	}
	from, err := s.currencyRef(ctx, *o.Currency)
	if err != nil {
		return err
	}
	minor2, meta, err := conv(*o.PriceMinor, from, o.LastSeenAt)
	if err != nil || meta == nil {
		return nil
	}
	o.PriceDisplay = &priceDisplay{
		Minor:     minor2,
		Currency:  displayTo.Code,
		Derived:   true,
		RateDate:  meta.RateDate.Format("2006-01-02"),
		RateStale: meta.Stale,
	}
	return nil
}

// listOrderings — U26 (TZ UI v5): сортировка списка. ORDER BY — фиксированный
// набор выражений: пользовательский ввод в SQL не попадает. NULLS LAST:
// строки без сортируемого ключа не вытесняют строки с ключом. date_desc
// (по умолчанию) — по ТЗ: first_seen DESC, id DESC (детерминированно при
// равных датах). ppsm — выражение, идентичное индексу objects_sort_ppsm_idx.
var listOrderings = map[string]string{
	"date_desc":      "objects.first_seen_at DESC, objects.id DESC",
	"date_asc":       "objects.first_seen_at ASC NULLS LAST, objects.id ASC",
	"price_asc":      "objects.current_price_minor ASC NULLS LAST, objects.id DESC",
	"price_desc":     "objects.current_price_minor DESC NULLS LAST, objects.id DESC",
	"deviation_asc":  "v.price_deviation ASC NULLS LAST, objects.id DESC",
	"deviation_desc": "v.price_deviation DESC NULLS LAST, objects.id DESC",
	"ppsm_asc":       "objects.current_price_minor / NULLIF(objects.area_sqm, 0) ASC NULLS LAST, objects.id DESC",
	"ppsm_desc":      "objects.current_price_minor / NULLIF(objects.area_sqm, 0) DESC NULLS LAST, objects.id DESC",
}

// listOrderingNames — для сообщения об ошибке.
const listOrderingNames = "date_desc, date_asc, price_asc, price_desc, deviation_asc, deviation_desc, ppsm_asc, ppsm_desc"

// devClassFilter — U27 (TZ UI v5): класс отклонения → SQL-условие.
// Ключи определены Волной 3 (в ТЗ только русские имена):
// far_below ≤ −15 %, below (−15, −5), near [−5, 5], above (5, 15),
// far_above ≥ 15 %, none — отклонения нет. Границы дублирует devClassOf
// (единственный источник чисел — там).
var devClassFilter = map[string]string{
	"far_below": "v.price_deviation <= -0.15",
	"below":     "v.price_deviation > -0.15 AND v.price_deviation < -0.05",
	"near":      "v.price_deviation >= -0.05 AND v.price_deviation <= 0.05",
	"above":     "v.price_deviation > 0.05 AND v.price_deviation < 0.15",
	"far_above": "v.price_deviation >= 0.15",
	"none":      "v.price_deviation IS NULL",
}

// listParams — все допустимые параметры списка (U27: неизвестный — 400
// с именем, молчаливое игнорирование параметра — дефект).
var listParams = map[string]bool{
	"page": true, "per_page": true, "display_currency": true,
	"country": true, "status": true, "sort": true,
	"price_min": true, "price_max": true, "area_min": true, "area_max": true,
	"rooms": true, "zone_id": true, "seller_type": true,
	"dev_class": true, "has_photo": true, "q": true, "bbox": true,
}

// listFilters — U27 (TZ UI v5) + U28: фильтры цены/площади/комнат/зоны/
// продавца/класса отклонения/фото и поиск по адресу. Каждый параметр
// валидируется с конкретным сообщением; min > max — 400 с обеими границами.
// dev_class ссылается на v.* — вызывающий обязан джойнить objectsFrom
// (LATERAL-оценка): это так и для списка, и для карты (U30).
func listFilters(r *http.Request, conds *[]string, args *[]any) error {
	add := func(cond string, a ...any) {
		*conds = append(*conds, fmt.Sprintf(cond, len(*args)+1))
		*args = append(*args, a...)
	}
	var priceMin, priceMax *int64
	for _, name := range []string{"price_min", "price_max"} {
		v := r.URL.Query().Get(name)
		if v == "" {
			continue
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return httpError(http.StatusBadRequest, "%s: неотрицательное целое (минорные единицы)", name)
		}
		if name == "price_min" {
			priceMin = &n
		} else {
			priceMax = &n
		}
	}
	if priceMin != nil && priceMax != nil && *priceMin > *priceMax {
		return httpError(http.StatusBadRequest, "price_min > price_max: %d > %d", *priceMin, *priceMax)
	}
	if priceMin != nil {
		add("objects.current_price_minor >= $%d", *priceMin)
	}
	if priceMax != nil {
		add("objects.current_price_minor <= $%d", *priceMax)
	}
	var areaMin, areaMax *float64
	for _, name := range []string{"area_min", "area_max"} {
		v := r.URL.Query().Get(name)
		if v == "" {
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 {
			return httpError(http.StatusBadRequest, "%s: неотрицательное число (м²)", name)
		}
		if name == "area_min" {
			areaMin = &f
		} else {
			areaMax = &f
		}
	}
	if areaMin != nil && areaMax != nil && *areaMin > *areaMax {
		return httpError(http.StatusBadRequest, "area_min > area_max: %g > %g", *areaMin, *areaMax)
	}
	if areaMin != nil {
		add("objects.area_sqm >= $%d", *areaMin)
	}
	if areaMax != nil {
		add("objects.area_sqm <= $%d", *areaMax)
	}
	if v := r.URL.Query().Get("rooms"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 32 {
			return httpError(http.StatusBadRequest, "rooms: целое 1..32")
		}
		add("objects.rooms = $%d", n)
	}
	if v := r.URL.Query().Get("zone_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			return httpError(http.StatusBadRequest, "zone_id: положительное целое")
		}
		add("objects.zone_id = $%d", n)
	}
	if v := r.URL.Query().Get("seller_type"); v != "" {
		switch v {
		case "private", "agency", "developer", "auction_organizer":
			add("objects.seller_type = $%d", v)
		default:
			return httpError(http.StatusBadRequest, "seller_type: private | agency | developer | auction_organizer")
		}
	}
	if v := r.URL.Query().Get("dev_class"); v != "" {
		cond, ok := devClassFilter[v]
		if !ok {
			return httpError(http.StatusBadRequest, "dev_class: far_below | below | near | above | far_above | none")
		}
		*conds = append(*conds, cond)
	}
	if v := r.URL.Query().Get("has_photo"); v != "" {
		switch v {
		case "true":
			*conds = append(*conds, "EXISTS (SELECT 1 FROM object_photos op WHERE op.object_id = objects.id)")
		case "false":
			*conds = append(*conds, "NOT EXISTS (SELECT 1 FROM object_photos op WHERE op.object_id = objects.id)")
		default:
			return httpError(http.StatusBadRequest, "has_photo: true | false")
		}
	}
	if v := r.URL.Query().Get("bbox"); v != "" {
		b, err := parseBBox(v)
		if err != nil {
			return err
		}
		n := len(*args)
		*conds = append(*conds, fmt.Sprintf(
			"objects.geom && ST_MakeEnvelope($%d::float8, $%d::float8, $%d::float8, $%d::float8, 0)::geography",
			n+1, n+2, n+3, n+4))
		*args = append(*args, b.minLng, b.minLat, b.maxLng, b.maxLat)
	}
	// U28: регистронезависимый поиск по адресу, минимум 2 символа.
	// GIN pg_trgm (0036) накрывает lower(address) LIKE '%…%'.
	if v := r.URL.Query().Get("q"); v != "" {
		term := strings.ToLower(strings.TrimSpace(v))
		if utf8.RuneCountInString(term) < 2 {
			return httpError(http.StatusBadRequest, "q: минимум 2 символа")
		}
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(term) + "%"
		add("lower(objects.address) LIKE $%d ESCAPE '\\'", like)
	}
	return nil
}

// GET /api/objects — список: U26 сортировка, U27 фильтры, U28 поиск,
// U29 total в каждом ответе.
func (s *Server) handleListObjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	for name := range r.URL.Query() {
		if !listParams[name] {
			writeErr(w, httpError(http.StatusBadRequest, "неизвестный параметр: %s", name))
			return
		}
	}

	page, err := queryInt(r, "page", 1, 100000)
	if err != nil {
		writeErr(w, err)
		return
	}
	perPage, err := queryInt(r, "per_page", 50, 200)
	if err != nil {
		writeErr(w, err)
		return
	}

	var displayTo *money.Currency
	var conv func(minor int64, from money.Currency, onDate time.Time) (int64, *fx.RateLookup, error)
	if dc := r.URL.Query().Get("display_currency"); dc != "" {
		to, err := s.currencyRef(ctx, dc)
		if err != nil {
			writeErr(w, err)
			return
		}
		displayTo = &to
		conv = s.displayConverter(ctx, to)
	}

	sortKey := r.URL.Query().Get("sort")
	if sortKey == "" {
		sortKey = "date_desc"
	}
	order, ok := listOrderings[sortKey]
	if !ok {
		writeErr(w, httpError(http.StatusBadRequest, "sort: %s — допустимо: %s", sortKey, listOrderingNames))
		return
	}

	var conds []string
	var args []any
	if c := r.URL.Query().Get("country"); c != "" {
		conds = append(conds, fmt.Sprintf("objects.country = $%d", len(args)+1))
		args = append(args, c)
	}
	if st := r.URL.Query().Get("status"); st != "" {
		if st != "active" && st != "delisted" {
			writeErr(w, httpError(http.StatusBadRequest, "status: active | delisted"))
			return
		}
		conds = append(conds, fmt.Sprintf("objects.status = $%d", len(args)+1))
		args = append(args, st)
	}
	if err := listFilters(r, &conds, &args); err != nil {
		writeErr(w, err)
		return
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	// U27: count с теми же джойнами, что и список (dev_class/has_photo
	// ссылаются на джойнированные таблицы); LATERAL не множит строки.
	var total int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*)"+objectsFrom+where, args...).Scan(&total); err != nil {
		writeErr(w, err)
		return
	}

	q := "SELECT " + objectsSelect + objectsFrom + where +
		" ORDER BY " + order + " LIMIT $" + strconv.Itoa(len(args)+1) + " OFFSET $" + strconv.Itoa(len(args)+2)
	args = append(args, perPage, (page-1)*perPage)

	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	out := []objectOut{}
	for rows.Next() {
		var o objectOut
		var val valuationOut
		var valSize *int
		var valFB *bool
		var hz hazardOut
		var hzSize *int
		var (
			phFirst, phLast, phN     *int64
			trRuText, trRuModel      *string
			trRuTokens               *int
			trRuAt                   *time.Time
			trEnText, trEnModel      *string
			trEnTokens               *int
			trEnAt                   *time.Time
			covOrd                   *int16
			covURL, covCard, covList *string
			geoY, geoX               *float64
		)
		if err := rows.Scan(&o.ID, &o.Country, &o.DealType, &o.ZoneID, &o.Title, &o.Address,
			&o.AreaSqM, &o.Rooms, &o.PropertyType, &o.SellerType, &o.SellerName,
			&o.PriceMinor, &o.Currency,
			&o.Status, &o.DelistedReason, &o.FirstSeenAt, &o.LastSeenAt, &o.DelistedAt,
			&phFirst, &phLast, &phN,
			&o.ZoneName, &o.ZoneLevel, &o.ZoneSource,
			&val.ModelVersion, &val.PriceDeviation, &val.NullReason, &val.PredictedMinor,
			&val.IntervalLowMinor, &val.IntervalHighMinor, &valSize, &val.RSquared, &valFB, &val.ComputedAt,
			&hz.HorizonDays, &hz.Probability, &hz.NullReason, &hz.ModelVersion, &hzSize, &hz.ComputedAt,
			&o.DescriptionOriginal, &o.LanguageOriginal,
			&trRuText, &trRuModel, &trRuTokens, &trRuAt,
			&trEnText, &trEnModel, &trEnTokens, &trEnAt,
			&covOrd, &covURL, &covCard, &covList, &o.NotModeled, &o.GeoConfidence, &geoY, &geoX); err != nil {
			writeErr(w, err)
			return
		}
		attachGeo(&o, o.GeoConfidence, geoY, geoX)
		attachPriceTrend(&o, phFirst, phLast, phN)
		attachValuation(&o, &val, valSize, valFB)
		if o.NotModeled {
			o.Valuation = nil // C25: not_modeled — публикуемых оценок не несёт
		}
		attachHazard(&o, &hz, hzSize)
		attachTranslation(&o, "ru", trRuText, trRuModel, trRuTokens, trRuAt)
		attachTranslation(&o, "en", trEnText, trEnModel, trEnTokens, trEnAt)
		attachCover(&o, covOrd, covURL, covCard, covList)
		if err := s.applyDisplay(ctx, &o, displayTo, conv); err != nil {
			writeErr(w, err)
			return
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "page": page, "per_page": perPage, "objects": out})
}

// GET /api/objects/{id}
func (s *Server) handleGetObject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, httpError(http.StatusBadRequest, "id: не целое"))
		return
	}
	var o objectOut
	var val valuationOut
	var valSize *int
	var valFB *bool
	var hz hazardOut
	var hzSize *int
	var (
		phFirst, phLast, phN     *int64
		trRuText, trRuModel      *string
		trRuTokens               *int
		trRuAt                   *time.Time
		trEnText, trEnModel      *string
		trEnTokens               *int
		trEnAt                   *time.Time
		covOrd                   *int16
		covURL, covCard, covList *string
		geoY, geoX               *float64
	)
	err = s.Pool.QueryRow(ctx, "SELECT "+objectsSelect+objectsFrom+" WHERE objects.id = $1", id).
		Scan(&o.ID, &o.Country, &o.DealType, &o.ZoneID, &o.Title, &o.Address, &o.AreaSqM, &o.Rooms,
			&o.PropertyType, &o.SellerType, &o.SellerName, &o.PriceMinor, &o.Currency, &o.Status, &o.DelistedReason,
			&o.FirstSeenAt, &o.LastSeenAt, &o.DelistedAt,
			&phFirst, &phLast, &phN,
			&o.ZoneName, &o.ZoneLevel, &o.ZoneSource,
			&val.ModelVersion, &val.PriceDeviation, &val.NullReason, &val.PredictedMinor,
			&val.IntervalLowMinor, &val.IntervalHighMinor, &valSize, &val.RSquared, &valFB, &val.ComputedAt,
			&hz.HorizonDays, &hz.Probability, &hz.NullReason, &hz.ModelVersion, &hzSize, &hz.ComputedAt,
			&o.DescriptionOriginal, &o.LanguageOriginal,
			&trRuText, &trRuModel, &trRuTokens, &trRuAt,
			&trEnText, &trEnModel, &trEnTokens, &trEnAt,
			&covOrd, &covURL, &covCard, &covList, &o.NotModeled, &o.GeoConfidence, &geoY, &geoX)
	if err != nil {
		writeErr(w, httpError(http.StatusNotFound, "объект %d не найден", id))
		return
	}
	attachGeo(&o, o.GeoConfidence, geoY, geoX)
	attachPriceTrend(&o, phFirst, phLast, phN)
	attachValuation(&o, &val, valSize, valFB)
	if o.NotModeled {
		o.Valuation = nil // C25: not_modeled — публикуемых оценок не несёт
	}
	attachHazard(&o, &hz, hzSize)
	attachTranslation(&o, "ru", trRuText, trRuModel, trRuTokens, trRuAt)
	attachTranslation(&o, "en", trEnText, trEnModel, trEnTokens, trEnAt)
	attachCover(&o, covOrd, covURL, covCard, covList)
	// История цены и объявления на площадках — только экран объекта
	// (TZ_UI v3 §6.3.2, §6.3.6); в списке не запрашиваются.
	if err := s.loadObjectExtras(ctx, &o, id); err != nil {
		writeErr(w, err)
		return
	}
	var displayTo *money.Currency
	var conv func(minor int64, from money.Currency, onDate time.Time) (int64, *fx.RateLookup, error)
	if dc := r.URL.Query().Get("display_currency"); dc != "" {
		to, err := s.currencyRef(ctx, dc)
		if err != nil {
			writeErr(w, err)
			return
		}
		displayTo = &to
		conv = s.displayConverter(ctx, to)
	}
	if err := s.applyDisplay(ctx, &o, displayTo, conv); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// loadObjectExtras — история цены (price_history) и объявления на
// площадках (object_listings → raw_listings → sources). Запрашивается
// только для экрана объекта (TZ_UI v3 §6.3.2, §6.3.6), чтобы список
// не нес лишние подзапросы. История — по возрастанию: на экране
// объекта она читается как хроника, а изменение за период UI считает
// от первой до последней записи.
func (s *Server) loadObjectExtras(ctx context.Context, o *objectOut, id int64) error {
	rows, err := s.Pool.Query(ctx,
		`SELECT price_minor, currency, change_at
		 FROM price_history WHERE object_id = $1
		 ORDER BY change_at, id`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	hist := []pricePointOut{}
	for rows.Next() {
		var p pricePointOut
		if err := rows.Scan(&p.PriceMinor, &p.Currency, &p.ChangeAt); err != nil {
			return err
		}
		hist = append(hist, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	o.PriceHistory = hist

	lrows, err := s.Pool.Query(ctx,
		`SELECT ol.source_id, s.name, ol.external_id, rl.source_url
		 FROM object_listings ol
		 JOIN sources s ON s.id = ol.source_id
		 LEFT JOIN raw_listings rl ON rl.id = ol.raw_listing_id
		 WHERE ol.object_id = $1
		 ORDER BY s.name, ol.external_id`, id)
	if err != nil {
		return err
	}
	defer lrows.Close()
	lists := []listingOut{}
	for lrows.Next() {
		var l listingOut
		if err := lrows.Scan(&l.SourceID, &l.SourceName, &l.ExternalID, &l.SourceURL); err != nil {
			return err
		}
		lists = append(lists, l)
	}
	if err := lrows.Err(); err != nil {
		return err
	}
	o.Listings = lists

	// Галерея (TZ v4 §4.4): оригинальная ссылка всегда, локальные
	// копии — URL /photos, если скачаны.
	prows, err := s.Pool.Query(ctx,
		`SELECT ord, original_url, file_card, file_list
		 FROM object_photos WHERE object_id = $1
		 ORDER BY ord`, id)
	if err != nil {
		return err
	}
	defer prows.Close()
	photos := []photoOut{}
	for prows.Next() {
		var p photoOut
		var ord int16
		var card, list *string
		if err := prows.Scan(&ord, &p.OriginalURL, &card, &list); err != nil {
			return err
		}
		p.Ord = int(ord)
		p.CardURL = photoURL(card)
		p.ListURL = photoURL(list)
		photos = append(photos, p)
	}
	if err := prows.Err(); err != nil {
		return err
	}
	o.Photos = photos

	var attrs, unmapped []byte
	if err := s.Pool.QueryRow(ctx,
		`SELECT attributes, attributes_unmapped FROM objects WHERE id = $1`, id).
		Scan(&attrs, &unmapped); err != nil {
		return err
	}
	if len(attrs) > 0 && string(attrs) != "null" {
		_ = json.Unmarshal(attrs, &o.Attributes)
	}
	if len(unmapped) > 0 && string(unmapped) != "null" {
		_ = json.Unmarshal(unmapped, &o.AttributesUnmapped)
	}
	return nil
}
