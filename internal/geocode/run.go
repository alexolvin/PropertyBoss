package geocode

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/config"
)

// RunReport — результат бэкфилла `pb geocode run`.
type RunReport struct {
	Candidates int // объектов ниже порога с описанием
	NoStreet   int // в тексте нет улицы — PSČ оставляем, API не зовём
	Tried      int // запросов к геокодеру (включая кэш)
	Upgraded   int // записан street/building
	Kept       int // геокодер не дал street+ — координаты площадки
	Errors     int // сеть/429 (повторятся)
	Dry        bool
}

// Run — бэкфилл геокодирования по сохранённым описаниям.
// Площадку не трогает (скан paused, C36). Кандидаты: active, уровень
// ниже refine_below. Без улицы в тексте — не тратим лимит.
func Run(ctx context.Context, pool *pgxpool.Pool, cfg config.GeocodeConf, limit int, country string, dry bool) (*RunReport, error) {
	rep := &RunReport{Dry: dry}
	th := refineThreshold(cfg)

	var gc Geocoder
	if cfg.BaseURL != "" && !dry {
		gc = &Cached{
			Inner: NewClient(cfg.BaseURL, cfg.UserAgent, cfg.RatePerMin, cfg.TimeoutSec),
			Pool:  pool,
		}
	}

	q := `
SELECT o.id, o.country, COALESCE(o.address,''), COALESCE(o.description_original,''),
       ST_Y(o.geom::geometry), ST_X(o.geom::geometry), o.geo_confidence
FROM objects o
WHERE o.status = 'active'
  AND o.description_original IS NOT NULL
  AND (o.geo_confidence IS NULL OR o.geo_confidence IN ('postcode','municipality','zone'))`
	args := []any{}
	if country != "" {
		args = append(args, country)
		q += fmt.Sprintf("\n  AND o.country = $%d AND o.property_country = $%d", len(args), len(args))
	}
	q += "\nORDER BY o.id"
	if limit > 0 {
		args = append(args, limit)
		q += fmt.Sprintf("\nLIMIT $%d", len(args))
	}

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("geocode run: кандидаты: %w", err)
	}
	defer rows.Close()

	type cand struct {
		id         int64
		country    string
		addr, desc string
		lat, lng   *float64
		conf       *string
	}
	var cs []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.country, &c.addr, &c.desc, &c.lat, &c.lng, &c.conf); err != nil {
			return nil, err
		}
		cs = append(cs, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rep.Candidates = len(cs)

	countryName := map[string]string{"CZ": "Czechia", "IT": "Italy", "NL": "Netherlands"}

	for _, c := range cs {
		if ctx.Err() != nil {
			return rep, ctx.Err()
		}
		city, postcode := ParseCityPostcode(c.addr)
		street := ExtractStreet(c.desc, city)
		if street == "" {
			rep.NoStreet++
			continue
		}
		qry := Query{
			Street: street, City: city, Postcode: postcode,
			Country: countryName[c.country], CountryCode: c.country,
		}
		if dry {
			rep.Tried++
			log.Printf("geocode dry: id=%d q=%q", c.id, qry.CacheKey())
			continue
		}
		if gc == nil {
			return nil, fmt.Errorf("geocode run: geocode.base_url не задан")
		}

		var src *Point
		srcConf := ""
		if c.lat != nil && c.lng != nil {
			src = &Point{Lat: *c.lat, Lng: *c.lng}
		}
		if c.conf != nil {
			srcConf = *c.conf
		}

		rep.Tried++
		res, err := Resolve(ctx, cfg, src, srcConf, 0, qry, gc)
		if err != nil {
			rep.Errors++
			log.Printf("geocode: объект %d: %v", c.id, err)
			continue
		}
		if res == nil || res.Source != "geocoder" || Rank(res.Confidence) < Rank(th) {
			rep.Kept++
			continue
		}
		addr := street
		if city != "" {
			addr = street + ", " + city
		}
		if postcode != "" {
			addr += ", " + postcode
		}
		if _, err := pool.Exec(ctx, `
UPDATE objects SET
    geom = ST_SetSRID(ST_MakePoint($2::float8, $3::float8), 4326)::geography,
    geo_confidence = $4,
    geo_source = 'geocoder',
    address = CASE WHEN $5 <> '' THEN $5 ELSE address END,
    zone_dirty = CASE WHEN geom IS DISTINCT FROM ST_SetSRID(ST_MakePoint($2::float8, $3::float8), 4326)::geography
                      THEN TRUE ELSE zone_dirty END,
    zone_terminal = CASE WHEN geom IS DISTINCT FROM ST_SetSRID(ST_MakePoint($2::float8, $3::float8), 4326)::geography
                         THEN FALSE ELSE zone_terminal END
WHERE id = $1`, c.id, res.Lng, res.Lat, res.Confidence, addr); err != nil {
			rep.Errors++
			log.Printf("geocode: объект %d запись: %v", c.id, err)
			continue
		}
		rep.Upgraded++
	}
	return rep, nil
}
