package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

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

type objectOut struct {
	ID                  int64           `json:"id"`
	Country             string          `json:"country"`
	DealType            string          `json:"deal_type"`
	ZoneID              *int64          `json:"zone_id"`
	ZoneName            *string         `json:"zone_name,omitempty"`
	ZoneLevel           *string         `json:"zone_level,omitempty"`
	ZoneSource          *string         `json:"zone_source,omitempty"`
	Address             *string         `json:"address"`
	AreaSqM             *float64        `json:"area_sqm"`
	Rooms               *int16          `json:"rooms"`
	PropertyType        *string         `json:"property_type"`
	PriceMinor          *int64          `json:"price_minor"`
	Currency            *string         `json:"currency"`
	PriceDisplay        *priceDisplay   `json:"price_display,omitempty"`
	DescriptionOriginal *string         `json:"description_original"`
	LanguageOriginal    *string         `json:"language_original"`
	TranslationRu       *translationOut `json:"translation_ru,omitempty"`
	TranslationEn       *translationOut `json:"translation_en,omitempty"`
	Valuation           *valuationOut   `json:"valuation,omitempty"`
	Hazard              *hazardOut      `json:"hazard,omitempty"`
	Status              string          `json:"status"`
	DelistedReason      *string         `json:"delisted_reason"`
	FirstSeenAt         time.Time       `json:"first_seen_at"`
	LastSeenAt          time.Time       `json:"last_seen_at"`
	DelistedAt          *time.Time      `json:"delisted_at"`
}

// objectsSelect/objectsFrom — с LEFT JOIN zones (имя/уровень/источник зоны
// рядом с объектом, ТЗ §13), LEFT JOIN LATERAL последней оценки
// (ТЗ §7.3: отклонение всегда с интервалом и размерами выборки) и
// LEFT JOIN LATERAL сохранённых переводов ru/en (ТЗ §11: UI читает из БД).
// Перевод показывается только свежий (source_hash = хеш ТЕКУЩЕГО
// описания): устаревший перевод показывать как текущий — подстановка.
// Квалификация objects.* обязательна: в zones те же имена колонок.
const objectsSelect = `objects.id, objects.country, objects.deal_type, objects.zone_id, objects.address, objects.area_sqm, objects.rooms, objects.property_type,
	objects.current_price_minor, objects.currency, objects.status, objects.delisted_reason, objects.first_seen_at, objects.last_seen_at, objects.delisted_at,
	z.name AS zone_name, z.level AS zone_level, z.source AS zone_source,
	v.model_version, v.price_deviation, v.deviation_null_reason, v.predicted_price_minor,
	v.interval_low_minor, v.interval_high_minor, v.sample_size, v.r_squared, v.zone_fallback, v.computed_at,
	h.horizon_days, h.hazard_probability, h.null_reason, h.model_version, h.events_in_training, h.computed_at,
	objects.description_original, objects.language_original,
	tr.text AS tr_text, tr.model AS tr_model, tr.token_cost AS tr_tokens, tr.translated_at AS tr_at,
	te.text AS te_text, te.model AS te_model, te.token_cost AS te_tokens, te.translated_at AS te_at`

const objectsFrom = ` FROM objects
		LEFT JOIN zones z ON z.id = objects.zone_id
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
		) te ON true`

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

// GET /api/objects?country=&status=&page=&per_page=&display_currency=
func (s *Server) handleListObjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

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
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	var total int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM objects"+where, args...).Scan(&total); err != nil {
		writeErr(w, err)
		return
	}

	q := "SELECT " + objectsSelect + objectsFrom + where +
		" ORDER BY objects.id DESC LIMIT $" + strconv.Itoa(len(args)+1) + " OFFSET $" + strconv.Itoa(len(args)+2)
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
			trRuText, trRuModel *string
			trRuTokens          *int
			trRuAt              *time.Time
			trEnText, trEnModel *string
			trEnTokens          *int
			trEnAt              *time.Time
		)
		if err := rows.Scan(&o.ID, &o.Country, &o.DealType, &o.ZoneID, &o.Address,
			&o.AreaSqM, &o.Rooms, &o.PropertyType, &o.PriceMinor, &o.Currency,
			&o.Status, &o.DelistedReason, &o.FirstSeenAt, &o.LastSeenAt, &o.DelistedAt,
			&o.ZoneName, &o.ZoneLevel, &o.ZoneSource,
			&val.ModelVersion, &val.PriceDeviation, &val.NullReason, &val.PredictedMinor,
			&val.IntervalLowMinor, &val.IntervalHighMinor, &valSize, &val.RSquared, &valFB, &val.ComputedAt,
			&hz.HorizonDays, &hz.Probability, &hz.NullReason, &hz.ModelVersion, &hzSize, &hz.ComputedAt,
			&o.DescriptionOriginal, &o.LanguageOriginal,
			&trRuText, &trRuModel, &trRuTokens, &trRuAt,
			&trEnText, &trEnModel, &trEnTokens, &trEnAt); err != nil {
			writeErr(w, err)
			return
		}
		attachValuation(&o, &val, valSize, valFB)
		attachHazard(&o, &hz, hzSize)
		attachTranslation(&o, "ru", trRuText, trRuModel, trRuTokens, trRuAt)
		attachTranslation(&o, "en", trEnText, trEnModel, trEnTokens, trEnAt)
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
		trRuText, trRuModel *string
		trRuTokens          *int
		trRuAt              *time.Time
		trEnText, trEnModel *string
		trEnTokens          *int
		trEnAt              *time.Time
	)
	err = s.Pool.QueryRow(ctx, "SELECT "+objectsSelect+objectsFrom+" WHERE objects.id = $1", id).
		Scan(&o.ID, &o.Country, &o.DealType, &o.ZoneID, &o.Address, &o.AreaSqM, &o.Rooms,
			&o.PropertyType, &o.PriceMinor, &o.Currency, &o.Status, &o.DelistedReason,
			&o.FirstSeenAt, &o.LastSeenAt, &o.DelistedAt,
			&o.ZoneName, &o.ZoneLevel, &o.ZoneSource,
			&val.ModelVersion, &val.PriceDeviation, &val.NullReason, &val.PredictedMinor,
			&val.IntervalLowMinor, &val.IntervalHighMinor, &valSize, &val.RSquared, &valFB, &val.ComputedAt,
			&hz.HorizonDays, &hz.Probability, &hz.NullReason, &hz.ModelVersion, &hzSize, &hz.ComputedAt,
			&o.DescriptionOriginal, &o.LanguageOriginal,
			&trRuText, &trRuModel, &trRuTokens, &trRuAt,
			&trEnText, &trEnModel, &trEnTokens, &trEnAt)
	if err != nil {
		writeErr(w, httpError(http.StatusNotFound, "объект %d не найден", id))
		return
	}
	attachValuation(&o, &val, valSize, valFB)
	attachHazard(&o, &hz, hzSize)
	attachTranslation(&o, "ru", trRuText, trRuModel, trRuTokens, trRuAt)
	attachTranslation(&o, "en", trEnText, trEnModel, trEnTokens, trEnAt)
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
