package enrich

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/config"
	"propertyboss/internal/connectors/ulovdomov"
	"propertyboss/internal/extract"
)

var ulovExtractSkipLog sync.Once

func processUlovObject(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, c candidate, regKeys map[string]bool, rep *Report) error {
	d, raw, err := ulovdomov.FetchDetail(ctx, c.url)
	if err != nil {
		rep.Failed++
		log.Printf("enrich: ulovdomov объект %d: %v", c.id, err)
		return nil
	}
	rep.Verified++
	sum := sha256.Sum256(raw)
	detailHash := hex.EncodeToString(sum[:])

	attrs := map[string]any{}
	unmapped := map[string]any{}
	for k, v := range d.Attrs {
		if regKeys[k] {
			attrs[k] = v
		} else {
			unmapped[k] = v
		}
	}
	for k, v := range d.Unmapped {
		unmapped[k] = v
	}
	if d.SellerType != "" && regKeys["seller_type"] {
		attrs["seller_type"] = d.SellerType
	}
	attrsJSON, _ := json.Marshal(attrs)
	unmappedJSON, _ := json.Marshal(unmapped)

	var sellerCol *string
	if d.SellerType != "" {
		sellerCol = &d.SellerType
	}
	src, conf := "source", "building"
	var geoLng, geoLat *float64
	var geoConf, geoSrc *string
	if d.Lat != nil && d.Lng != nil {
		geoLat, geoLng = d.Lat, d.Lng
		geoConf, geoSrc = &conf, &src
		rep.Geocoded++
	}
	if sellerCol != nil {
		rep.SellerType++
	}
	rep.Attrs += len(attrs)

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
UPDATE objects SET
    detail_fetched_at = now(),
    detail_hash = $2,
    title = COALESCE(NULLIF($3, ''), title),
    description_original = COALESCE(NULLIF($4, ''), description_original),
    address = COALESCE(NULLIF($5, ''), address),
    area_sqm = COALESCE($6, area_sqm),
    rooms = COALESCE($7, rooms),
    attributes = attributes || $8::jsonb,
    attributes_unmapped = attributes_unmapped || $9::jsonb,
    seller_type = COALESCE($10, seller_type),
    seller_name = COALESCE(NULLIF($11, ''), seller_name),
    geom = CASE WHEN $12::float8 IS NOT NULL
               THEN ST_SetSRID(ST_MakePoint($12::float8, $13::float8), 4326)::geography
               ELSE geom END,
    geo_confidence = COALESCE($14, geo_confidence),
    geo_source = COALESCE($15, geo_source),
    current_price_minor = COALESCE(current_price_minor, $16),
    currency = COALESCE(currency, NULLIF($17, ''))
WHERE id = $1`,
		c.id, detailHash, d.Title, d.Description, d.Address,
		d.AreaSqM, d.Rooms, attrsJSON, unmappedJSON,
		sellerCol, d.SellerName,
		geoLng, geoLat, geoConf, geoSrc,
		d.PriceMinor, d.Currency,
	); err != nil {
		return fmt.Errorf("ulovdomov objects update: %w", err)
	}

	for k := range attrs {
		if _, err := tx.Exec(ctx, `
INSERT INTO attribute_values (object_id, attr_key, extracted_by, confidence, source_hash, extracted_at)
VALUES ($1, $2, 'source_field', 1.0, $3, now())
ON CONFLICT (object_id, attr_key) DO UPDATE SET
    extracted_by = EXCLUDED.extracted_by,
    confidence = EXCLUDED.confidence,
    source_hash = EXCLUDED.source_hash,
    extracted_at = EXCLUDED.extracted_at`,
			c.id, k, detailHash); err != nil {
			return fmt.Errorf("ulovdomov attribute_values %s: %w", k, err)
		}
		rep.Attrs++
	}

	if len(d.Photos) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM object_photos WHERE object_id = $1`, c.id); err != nil {
			return err
		}
		max := len(d.Photos)
		if cfg.Detail.MaxPhotos > 0 && max > cfg.Detail.MaxPhotos {
			max = cfg.Detail.MaxPhotos
		}
		for i, u := range d.Photos[:max] {
			if _, err := tx.Exec(ctx, `
INSERT INTO object_photos (object_id, ord, original_url) VALUES ($1, $2, $3)`,
				c.id, i+1, u); err != nil {
				return err
			}
		}
		rep.Photos += max
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	rep.Done++

	if err := runUlovExtract(ctx, pool, cfg, c.id, rep); err != nil {
		return err
	}
	return nil
}

func extractConfigOf(cfg *config.Config) *extract.Config {
	if strings.TrimSpace(cfg.Extract.APIKey) == "" || strings.TrimSpace(cfg.Extract.Model) == "" {
		return nil
	}
	return &extract.Config{
		BaseURL:             cfg.Extract.BaseURL,
		APIKey:              cfg.Extract.APIKey,
		Model:               cfg.Extract.Model,
		TimeoutSec:          cfg.Extract.TimeoutSec,
		MaxConfidence:       cfg.Extract.MaxConfidence,
		AttrListVersion:     cfg.Extract.AttrListVersion,
		MaxChars:            cfg.Extract.MaxChars,
		BudgetTokens:        cfg.Extract.BudgetTokens,
		MinedMinOccurrences: cfg.Rules.MinMinedOccurrences,
		MinedConfidenceMax:  cfg.Rules.MinedConfidenceMax,
		MinedExampleObjects: cfg.Rules.ExampleObjects,
		// Пакетный промоушн/тень — только `pb extract run`, не каждый объект импорта.
		MinedAutoPromote: false,
		ShadowSamplePct:  0,
	}
}

func runUlovExtract(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, objectID int64, rep *Report) error {
	ec := extractConfigOf(cfg)
	if ec == nil {
		ulovExtractSkipLog.Do(func() {
			log.Printf("enrich: ulovdomov: LLM-извлечение пропущено (extract.api_key/model пуст) — шаг 3 не выполнен, значения площадки записаны")
		})
		return nil
	}
	erep, err := extract.ProcessObject(ctx, pool, ec, objectID)
	if err != nil {
		rep.LLMErr++
		return err
	}
	rep.LLMCalls += erep.Extracted
	rep.LLMValid += erep.Valid
	rep.LLMErr += erep.CallErrors + erep.ParseFailed
	if erep.Extracted > 0 || erep.Valid > 0 || erep.NoMissing > 0 || erep.Skipped > 0 {
		log.Printf("enrich: ulovdomov объект %d: LLM вызовов %d, значений %d, пропущено %d, без дырок %d, ошибок %d, токенов %d",
			objectID, erep.Extracted, erep.Valid, erep.Skipped, erep.NoMissing, erep.CallErrors+erep.ParseFailed, erep.Tokens)
	}
	return nil
}

func isUlovURL(u string) bool {
	return strings.Contains(u, "ulovdomov.cz")
}
