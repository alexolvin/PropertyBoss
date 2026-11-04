package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"propertyboss/internal/config"
	"propertyboss/internal/connectors/bazos"
	"propertyboss/internal/db"
	"propertyboss/internal/probe"
)

func runVerifySample(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("verify-sample", flag.ExitOnError)
	n := fs.Int("n", 20, "размер выборки")
	seed := fs.Int("seed", 1, "seed для md5-порядка")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: pb verify-sample <source-id> --n 20 --seed N")
	}
	sourceID := fs.Arg(0)
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	rows, err := pool.Query(ctx, `
SELECT o.id, ol.external_id, rl.source_url, o.area_sqm::text, o.property_type, o.deal_type, o.current_price_minor
FROM objects o
JOIN object_listings ol ON ol.object_id = o.id AND ol.source_id = $1
JOIN raw_listings rl ON rl.id = ol.raw_listing_id
WHERE o.status = 'active' AND rl.source_url IS NOT NULL
ORDER BY md5(o.id::text || $2::text)
LIMIT $3`, sourceID, fmt.Sprintf("%d", *seed), *n)
	if err != nil {
		return err
	}
	defer rows.Close()
	type row struct {
		id, ext, url string
		area         *string
		ptype, deal  string
		price        *int64
	}
	var sample []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.ext, &r.url, &r.area, &r.ptype, &r.deal, &r.price); err != nil {
			return err
		}
		sample = append(sample, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(sample) == 0 {
		return fmt.Errorf("verify-sample: нет активных объектов источника %s", sourceID)
	}

	ua := cfg.Source.UserAgent
	cli := probe.NewClient(ua, 30*time.Second)
	type cmp struct {
		ObjectID string `json:"object_id"`
		URL      string `json:"url"`
		Field    string `json:"field"`
		DB       string `json:"db"`
		Live     string `json:"live"`
		OK       bool   `json:"ok"`
		Note     string `json:"note,omitempty"`
	}
	var diffs []cmp
	okN := 0
	for _, r := range sample {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, rec, kind, err := cli.Get(ctx, r.url)
		if err != nil {
			diffs = append(diffs, cmp{ObjectID: r.id, URL: r.url, Field: "fetch", OK: false, Note: err.Error()})
			continue
		}
		if kind != probe.KindNone {
			diffs = append(diffs, cmp{ObjectID: r.id, URL: r.url, Field: "fetch", OK: false, Note: string(kind) + " status=" + fmt.Sprint(rec.Status)})
			continue
		}
		switch sourceID {
		case bazos.SourceID:
			d, err := bazos.ParseDetail(string(body))
			if err != nil {
				diffs = append(diffs, cmp{ObjectID: r.id, URL: r.url, Field: "parse", OK: false, Note: err.Error()})
				continue
			}
			// цена: расхождение допустимо, если raw_listings скана уже имел то же, что БД
			liveP := ""
			if d.PriceMinor != nil {
				liveP = fmt.Sprintf("%d", *d.PriceMinor)
			}
			dbP := ""
			if r.price != nil {
				dbP = fmt.Sprintf("%d", *r.price)
			}
			priceOK := liveP == dbP
			if !priceOK {
				var rawP *int64
				_ = pool.QueryRow(ctx, `SELECT price_minor FROM raw_listings WHERE source_id=$1 AND external_id=$2 ORDER BY fetched_at DESC LIMIT 1`,
					sourceID, r.ext).Scan(&rawP)
				if (rawP == nil && r.price == nil) || (rawP != nil && r.price != nil && *rawP == *r.price) {
					diffs = append(diffs, cmp{ObjectID: r.id, URL: r.url, Field: "price", DB: dbP, Live: liveP, OK: true, Note: "цена площадки изменилась; БД=raw_listings"})
					okN++
					continue
				}
			}
			if priceOK {
				okN++
			} else {
				diffs = append(diffs, cmp{ObjectID: r.id, URL: r.url, Field: "price", DB: dbP, Live: liveP, OK: false})
			}
		default:
			diffs = append(diffs, cmp{ObjectID: r.id, URL: r.url, Field: "parser", OK: false,
				Note: "НЕ ИЗМЕРЕНО: парсер источника не зарегистрирован в verify-sample"})
		}
	}
	out := map[string]any{
		"source_id": sourceID, "n": *n, "seed": *seed, "got": len(sample),
		"ok": okN, "diffs": diffs, "at": time.Now().UTC().Format(time.RFC3339),
		"ua": ua, "requests": cli.Requests(),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	log.Printf("verify-sample: source=%s n=%d got=%d ok=%d diffs=%d", sourceID, *n, len(sample), okN, len(diffs))
	return nil
}
