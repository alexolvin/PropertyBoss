package ulovdomov

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"propertyboss/internal/ratelimit"
	"propertyboss/internal/scan"
)

const SourceID = "ulovdomov"

const (
	findURL   = "https://ud.api.ulovdomov.cz/v1/offer/find"
	userAgent = "PropertyBoss/0.3 (real-estate price monitoring)"
	perPage   = 50
)

// CZ bbox с главной ulovdomov.cz (NEXT_DATA pageProps.bounds, Playwright 2026-09-11).
var czBounds = map[string]any{
	"northEast": map[string]float64{"lat": 51.06, "lng": 18.87},
	"southWest": map[string]float64{"lat": 48.55, "lng": 12.09},
}

func init() { scan.Register(&Connector{http: &http.Client{Timeout: 30 * time.Second}}) }

type Connector struct {
	http  *http.Client
	delay time.Duration
}

func (c *Connector) SetPageDelay(d time.Duration) { c.delay = d }
func (c *Connector) SourceID() string             { return SourceID }

func (c *Connector) Scan(ctx context.Context, cfg scan.SearchConfig) ([]scan.Listing, *scan.Issue, error) {
	offer := cfg.DealType // sale | rent
	if offer != "sale" && offer != "rent" {
		return nil, nil, fmt.Errorf("ulovdomov: deal_type %q", cfg.DealType)
	}
	body := map[string]any{"bounds": czBounds, "offerType": offer}
	if cfg.PropertyType != nil && *cfg.PropertyType != "" {
		body["propertyType"] = strings.ToLower(*cfg.PropertyType)
	}
	var all []scan.Listing
	seen := map[string]bool{}
	totalPages := 1
	siteTotal := 0
	for page := 1; page <= 500; page++ {
		if err := ctx.Err(); err != nil {
			return all, &scan.Issue{Kind: scan.FailNetwork, Message: err.Error()}, nil
		}
		raw, err := c.postFind(ctx, page, body)
		if err != nil {
			if len(all) == 0 {
				return nil, nil, err
			}
			return all, &scan.Issue{Kind: scan.Classify(err), Message: err.Error()}, nil
		}
		r, err := parseFind(raw)
		if err != nil {
			if len(all) == 0 {
				return nil, &scan.Issue{Kind: scan.FailLayoutChange, Message: err.Error()}, nil
			}
			return all, &scan.Issue{Kind: scan.FailLayoutChange, Message: err.Error()}, nil
		}
		if page == 1 {
			totalPages = r.ExtraData.TotalPages
			siteTotal = r.ExtraData.Total
			if totalPages < 1 {
				totalPages = 1
			}
		}
		ls := listingsFrom(r)
		if len(ls) == 0 {
			break
		}
		for _, l := range ls {
			if seen[l.ExternalID] {
				continue
			}
			seen[l.ExternalID] = true
			all = append(all, l)
		}
		if page >= totalPages {
			break
		}
	}
	_ = siteTotal
	if len(all) == 0 {
		return []scan.Listing{}, nil, nil
	}
	return all, nil, nil
}

func (c *Connector) postFind(ctx context.Context, page int, payload map[string]any) ([]byte, error) {
	if err := ratelimit.WaitSource(ctx); err != nil {
		return nil, scan.NewFail(scan.FailNetwork, err)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	u := fmt.Sprintf("%s?page=%d&perPage=%d&sorting=latest", findURL, page, perPage)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return nil, scan.NewFail(scan.FailNetwork, err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer")
	req.Header.Set("Referer", "https://www.ulovdomov.cz/")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, scan.NewFail(scan.FailNetwork, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, scan.NewFailf(scan.FailHTTP429, "POST %s: 429", u)
	}
	if resp.StatusCode/100 != 2 {
		return nil, scan.NewFailf(scan.FailNetwork, "POST %s: HTTP %d", u, resp.StatusCode)
	}
	return raw, nil
}
