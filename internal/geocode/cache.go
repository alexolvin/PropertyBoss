package geocode

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Cached — обёртка геокодера: повтор того же нормализованного адреса
// не идёт в сеть (политика Nominatim + C10: много объектов на одном PSČ
// будут спрашивать одну улицу в одном городе).
type Cached struct {
	Inner Geocoder
	Pool  *pgxpool.Pool
}

func (c *Cached) Resolve(ctx context.Context, q Query) (*Point, error) {
	key := q.CacheKey()
	if key == "" {
		return nil, fmt.Errorf("geocode: пустой адрес (улица и город пустые)")
	}
	var hit bool
	var lat, lng *float64
	var typ *string
	err := c.Pool.QueryRow(ctx, `
SELECT hit, lat, lng, nominatim_type FROM geocode_cache WHERE query_norm = $1`, key).
		Scan(&hit, &lat, &lng, &typ)
	if err == nil {
		if !hit || lat == nil || lng == nil {
			return nil, nil
		}
		p := &Point{Lat: *lat, Lng: *lng}
		if typ != nil {
			p.Type = *typ
		}
		return p, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("geocode cache: %w", err)
	}

	p, err := c.Inner.Resolve(ctx, q)
	if err != nil {
		return nil, err // 429 и сеть не кэшируем
	}
	if p == nil {
		if _, ierr := c.Pool.Exec(ctx, `
INSERT INTO geocode_cache (query_norm, hit) VALUES ($1, false)
ON CONFLICT (query_norm) DO NOTHING`, key); ierr != nil {
			return nil, fmt.Errorf("geocode cache miss: %w", ierr)
		}
		return nil, nil
	}
	if _, ierr := c.Pool.Exec(ctx, `
INSERT INTO geocode_cache (query_norm, lat, lng, nominatim_type, hit)
VALUES ($1, $2, $3, $4, true)
ON CONFLICT (query_norm) DO NOTHING`, key, p.Lat, p.Lng, p.Type); ierr != nil {
		return nil, fmt.Errorf("geocode cache hit: %w", ierr)
	}
	return p, nil
}
