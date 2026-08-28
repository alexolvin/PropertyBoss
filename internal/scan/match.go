package scan

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Методы сопоставления (object_listings.match_method, ТЗ §8.1).
const (
	matchSourceExternal = "source_external"
	matchGeo            = "geo"
	matchAddress        = "address"
)

// matchListing — связывание одной записи с объектом (ТЗ §8.1).
//
// Методы в порядке приоритета:
//  1. source_external: (source_id, external_id) — уверенность 'high';
//  2. geo: координаты в радиусе radius_m + площадь в допуске + совпадают
//     комнаты и тип объекта — 'high';
//  3. address: только при отсутствии координат; сходство нормализованного
//     адреса выше порога из конфига — 'low' (ТЗ §8.1: совпадение только по
//     адресу не сливается молча — низкая уверенность фиксируется в
//     object_listings).
//
// Возвращает true, если объект создан.
func (r *Runner) matchListing(ctx context.Context, tx pgx.Tx, sourceID string, cfg SearchConfig, l Listing, rawID int64, known map[string]bool, now time.Time) (bool, error) {
	kAttrs, uAttrs, err := splitAttributes(known, l.Attributes)
	if err != nil {
		return false, err
	}

	var (
		objectID  int64
		status    string
		matchMeth = matchSourceExternal
		isNew     bool
	)
	err = tx.QueryRow(ctx, `
		SELECT o.id, o.status
		FROM object_listings ol JOIN objects o ON o.id = ol.object_id
		WHERE ol.source_id = $1 AND ol.external_id = $2`,
		sourceID, l.ExternalID,
	).Scan(&objectID, &status)
	switch {
	case err == nil:
		// 1. Тот же источник, тот же external_id — готовая ссылка.
		if status == "delisted" {
			// Объект вернулся в выдачу под тем же id объявления
			// (ТЗ §8.2: «исчезновение — не продажа» — и повторное
			// появление возвращает объект на рынок): статус delisted
			// откатывается.
			// Без отката delisted-объект, найденный снова, получал бы
			// обновления цены/даты, но остался бы delisted навсегда.
			if _, err := tx.Exec(ctx, `
				UPDATE objects
				SET status = 'active', delisted_reason = NULL, delisted_at = NULL
				WHERE id = $1 AND status = 'delisted'`, objectID); err != nil {
				return false, err
			}
		}
	case errors.Is(err, pgx.ErrNoRows):
		// 2/3. Дедупликация по §8.1 (другие источники) или новый объект.
		objectID, matchMeth, isNew, err = r.findOrCreateByDedupe(ctx, tx, sourceID, cfg, l, kAttrs, uAttrs, now)
		if err != nil {
			return false, err
		}
	default:
		return false, err
	}

	// Ссылка объект↔объявление. Для source_external строка уже существует
	// (DO NOTHING); для geo/address — новая.
	confidence := "high"
	if matchMeth == matchAddress {
		confidence = "low" // ТЗ §8.1: совпадение только по адресу
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO object_listings (object_id, raw_listing_id, source_id, external_id,
			match_method, match_confidence)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (object_id, source_id, external_id) DO NOTHING`,
		objectID, rawID, sourceID, l.ExternalID, matchMeth, confidence,
	); err != nil {
		return false, err
	}

	// История цены (ТЗ §9.2). Сравниваем с последней ценой ЭТОГО источника
	// по объекту (история — на (object, source), не на объекте: цена другого
	// источника не считается «изменением» для этого). lastSourcePrice = nil —
	// источник объект ещё не ценовил (включая только что созданный объект).
	if l.PriceMinor != nil && l.Currency != nil {
		var lastSourcePrice *int64
		if err := tx.QueryRow(ctx, `
			SELECT price_minor FROM price_history
			WHERE object_id = $1 AND source_id = $2
			ORDER BY change_at DESC, id DESC LIMIT 1`,
			objectID, sourceID,
		).Scan(&lastSourcePrice); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
		if lastSourcePrice == nil || *lastSourcePrice != *l.PriceMinor {
			// ON CONFLICT: в одном скане объект может попасть дважды под
			// разными external_id (одна цена на (source, момент) — схема).
			if _, err := tx.Exec(ctx, `
				INSERT INTO price_history (object_id, source_id, price_minor, currency, change_at)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (object_id, source_id, change_at) DO NOTHING`,
				objectID, sourceID, *l.PriceMinor, *l.Currency, now,
			); err != nil {
				return false, err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE objects SET current_price_minor = $2, currency = $3 WHERE id = $1`,
				objectID, *l.PriceMinor, *l.Currency,
			); err != nil {
				return false, err
			}
		}
	}

	// §14.5.1: дата публикации на сайте ушла вперёд, не меняя ID —
	// площадка обновила её при редактировании; объект исключается из
	// обучающей выборки модели дожития.
	unreliable := false
	if l.PostedAt != nil {
		var hasOlder bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM raw_listings
				WHERE source_id = $1 AND external_id = $2
				  AND posted_at IS NOT NULL AND posted_at < $3)`,
			sourceID, l.ExternalID, *l.PostedAt,
		).Scan(&hasOlder); err != nil {
			return false, err
		}
		unreliable = hasOlder
	}

	// Обновление объекта: только присутствующие поля (NULL не затирает
	// ранее известные — ТЗ §0.4); attributes — слияние, старые ключи
	// сохраняются.
	if _, err := tx.Exec(ctx, `
		UPDATE objects SET
			last_seen_at = $2,
			address = COALESCE($3, address),
			area_sqm = COALESCE($4, area_sqm),
			rooms = COALESCE($5, rooms),
			property_type = COALESCE($6, property_type),
			description_original = COALESCE($7, description_original),
			language_original = COALESCE($8, language_original),
			geom = CASE WHEN $9::text IS NULL THEN geom ELSE ST_GeogFromText($9) END,
			attributes = attributes || $10::jsonb,
			attributes_unmapped = attributes_unmapped || $11::jsonb,
			posted_date_unreliable = posted_date_unreliable OR $12
		WHERE id = $1`,
		objectID, now, l.Address, l.AreaSqM, l.Rooms, l.PropertyType,
		l.Description, l.LanguageOriginal, ewkt(l.Lat, l.Lng),
		kAttrs, uAttrs, unreliable,
	); err != nil {
		return false, err
	}
	return isNew, nil
}

// findOrCreateByDedupe — поиск существующего объекта по критериям ТЗ §8.1
// (другой источник, тот же физический объект); при отсутствии — создание.
// Возвращает (objectID, matchMethod, isNew).
//
// Кандидатами НЕ бывают объекты, у которых уже есть объявление ЭТОГО
// источника: внутри источника идентичность — (source_id, external_id),
// другой external_id того же источника — другой физический объект.
// Без исключения координатная/адресная дедупликация (ТЗ §8.1 — «из
// разных источников») сливала в один объект все объявления одного
// почтового района (адрес источника = город + PSČ) и смешивала
// price_history разных квартир (промышленный скан 2026-08-27:
// 7 677 объявлений → 925 «объектов»).
func (r *Runner) findOrCreateByDedupe(ctx context.Context, tx pgx.Tx, sourceID string, cfg SearchConfig, l Listing, kAttrs, uAttrs string, now time.Time) (int64, string, bool, error) {
	ded, ok := r.Dedupe[cfg.Country]
	if !ok {
		return 0, "", false, errors.New("нет параметров дедупликации для страны (config: dedupe.by_country)")
	}

	// geo: координаты в радиусе + критерии §8.1.
	if l.Lat != nil && l.Lng != nil {
		geog := ewkt(l.Lat, l.Lng)
		rows, err := tx.Query(ctx, `
			SELECT id, area_sqm, rooms, property_type
			FROM objects
			WHERE country = $1 AND status = 'active' AND geom IS NOT NULL
			  AND NOT EXISTS (SELECT 1 FROM object_listings ol
			                  WHERE ol.object_id = objects.id AND ol.source_id = $2)
			  AND ST_DWithin(geom, ST_GeogFromText($3::text), $4)
			ORDER BY geom <-> ST_GeogFromText($3::text)
			LIMIT 20`,
			cfg.Country, sourceID, *geog, ded.RadiusM)
		if err != nil {
			return 0, "", false, err
		}
		var candidates []dedupeCandidate
		for rows.Next() {
			var c dedupeCandidate
			if err := rows.Scan(&c.id, &c.area, &c.rooms, &c.propertyType); err != nil {
				rows.Close()
				return 0, "", false, err
			}
			candidates = append(candidates, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, "", false, err
		}
		for _, c := range candidates {
			if criteriaMatch(l, c, ded.AreaTolerancePct) {
				return c.id, matchGeo, false, nil
			}
		}
	} else if l.Address != nil && strings.TrimSpace(*l.Address) != "" {
		// address: нет координат — сравнение нормализованных адресов
		// (порог сходства из конфига, ТЗ §8.1).
		rows, err := tx.Query(ctx, `
			SELECT id, area_sqm, rooms, property_type, address
			FROM objects
			WHERE country = $1 AND status = 'active' AND address IS NOT NULL
			  AND geom IS NULL
			  AND NOT EXISTS (SELECT 1 FROM object_listings ol
			                  WHERE ol.object_id = objects.id AND ol.source_id = $2)`,
			cfg.Country, sourceID)
		if err != nil {
			return 0, "", false, err
		}
		defer rows.Close()
		for rows.Next() {
			var c dedupeCandidate
			if err := rows.Scan(&c.id, &c.area, &c.rooms, &c.propertyType, &c.address); err != nil {
				return 0, "", false, err
			}
			if addressSimilarity(*l.Address, *c.address) >= ded.AddressSimilarity &&
				criteriaMatch(l, c, ded.AreaTolerancePct) {
				return c.id, matchAddress, false, nil
			}
		}
		if err := rows.Err(); err != nil {
			return 0, "", false, err
		}
	}

	id, err := createObject(ctx, tx, cfg, l, kAttrs, uAttrs, now)
	if err != nil {
		return 0, "", false, err
	}
	return id, matchSourceExternal, true, nil
}

// dedupeCandidate — существующий объект-кандидат для §8.1.
type dedupeCandidate struct {
	id           int64
	area         *string
	rooms        *int
	propertyType *string
	address      *string
}

// criteriaMatch — критерии §8.1: площадь в допуске, комнаты и тип объекта
// совпадают. Отсутствующее поле не является основанием для расхождения
// (площадка может не публиковать площадь/комнаты).
func criteriaMatch(l Listing, c dedupeCandidate, areaTolerancePct int) bool {
	if l.Rooms != nil && c.rooms != nil && *l.Rooms != *c.rooms {
		return false
	}
	if l.PropertyType != nil && c.propertyType != nil && *l.PropertyType != *c.propertyType {
		return false
	}
	if l.AreaSqM != nil && c.area != nil {
		a, okA := new(big.Rat).SetString(*l.AreaSqM)
		b, okB := new(big.Rat).SetString(*c.area)
		if okA && okB && a.Sign() > 0 && b.Sign() > 0 {
			// |a−b| / b ≤ tol: отклонение от площади, уже известной объекту.
			diff := new(big.Rat).Sub(a, b)
			diff.Abs(diff)
			rel := new(big.Rat).Quo(diff, b)
			tol := new(big.Rat).SetFrac64(int64(areaTolerancePct), 100)
			if rel.Cmp(tol) > 0 {
				return false
			}
		}
	}
	return true
}

// createObject — новый объект: status 'active' (default), match_confidence
// 'high', first_seen = last_seen = время скана.
func createObject(ctx context.Context, tx pgx.Tx, cfg SearchConfig, l Listing, kAttrs, uAttrs string, now time.Time) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO objects (country, deal_type, geom, address, area_sqm, rooms, property_type,
			attributes, attributes_unmapped, current_price_minor, currency,
			description_original, language_original, match_confidence, first_seen_at, last_seen_at)
		VALUES ($1, $2,
			CASE WHEN $3::text IS NULL THEN NULL ELSE ST_GeogFromText($3) END,
			$4, $5, $6, $7, $8::jsonb, $9::jsonb, $10, $11, $12, $13, 'high', $14, $14)
		RETURNING id`,
		cfg.Country, cfg.DealType, ewkt(l.Lat, l.Lng), l.Address, l.AreaSqM, l.Rooms, l.PropertyType,
		kAttrs, uAttrs, l.PriceMinor, l.Currency, l.Description, l.LanguageOriginal, now,
	).Scan(&id)
	return id, err
}

// splitAttributes — разделение атрибутов коннектора на ключи реестра
// (objects.attributes) и вне-реестровые (objects.attributes_unmapped;
// ТЗ §6: «незнакомый ключ не отбрасывается молча»).
func splitAttributes(known map[string]bool, attrs map[string]any) (string, string, error) {
	k := map[string]any{}
	u := map[string]any{}
	for key, val := range attrs {
		if known[key] {
			k[key] = val
		} else {
			u[key] = val
		}
	}
	kj, err := json.Marshal(k)
	if err != nil {
		return "", "", err
	}
	uj, err := json.Marshal(u)
	if err != nil {
		return "", "", err
	}
	return string(kj), string(uj), nil
}

// normalizeAddress — для сравнения: нижний регистр, только буквы a-z и
// цифры (пунктуация, пробелы и диакритика отбрасываются одинаково у обоих
// адресов). Адресы — не деньги, float/прямые строковые операции допустимы.
func normalizeAddress(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// levenshtein — классическое динамическое расстояние, O(len(a)·len(b)).
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			m := prev[j] + 1
			if cur[j-1]+1 < m {
				m = cur[j-1] + 1
			}
			if prev[j-1]+cost < m {
				m = prev[j-1] + cost
			}
			cur[j] = m
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// addressSimilarity — 1 − dist/maxLen; равные строки → 1.
func addressSimilarity(a, b string) float64 {
	na, nb := normalizeAddress(a), normalizeAddress(b)
	switch {
	case na == "" && nb == "":
		return 1
	case na == "" || nb == "":
		return 0
	}
	maxLen := len(na)
	if len(nb) > maxLen {
		maxLen = len(nb)
	}
	return 1 - float64(levenshtein(na, nb))/float64(maxLen)
}
