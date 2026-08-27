package scan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LoadSearchConfig — снимок конфигурации поиска из БД (для `pb scan` и
// тестов). Числа filter_attributes остаются json.Number — без float на
// пути (ТЗ §5 — по духу: точность до явной проверки).
func LoadSearchConfig(ctx context.Context, pool *pgxpool.Pool, id int64) (SearchConfig, error) {
	var (
		sc   SearchConfig
		pt   *string
		minP *int64
		maxP *int64
		minA *string
		maxA *string
		ccy  *string
		filt []byte
	)
	err := pool.QueryRow(ctx, `
		SELECT source_id, country, deal_type, property_type, min_price_minor, max_price_minor,
		       min_area_sqm, max_area_sqm, filter_attributes, currency, active
		FROM search_configs WHERE id = $1`, id,
	).Scan(&sc.SourceID, &sc.Country, &sc.DealType, &pt, &minP, &maxP, &minA, &maxA, &filt, &ccy, &sc.Active)
	if err != nil {
		return SearchConfig{}, fmt.Errorf("scan: конфигурация поиска %d: %w", id, err)
	}
	sc.ID = id
	sc.PropertyType = pt
	sc.MinPriceMinor = minP
	sc.MaxPriceMinor = maxP
	sc.MinAreaSqM = minA
	sc.MaxAreaSqM = maxA
	sc.Currency = ccy
	if len(filt) > 0 && string(filt) != "{}" {
		dec := json.NewDecoder(bytes.NewReader(filt))
		dec.UseNumber()
		if err := dec.Decode(&sc.FilterAttrs); err != nil {
			return SearchConfig{}, fmt.Errorf("scan: filter_attributes конфигурации %d: %w", id, err)
		}
	}
	return sc, nil
}

// ErrConfigInactive — конфигурация поиска деактивна: сканировать нельзя.
var ErrConfigInactive = errors.New("конфигурация поиска деактивна")
