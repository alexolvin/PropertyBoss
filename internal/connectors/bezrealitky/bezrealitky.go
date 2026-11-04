package bezrealitky

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"propertyboss/internal/ratelimit"
	"propertyboss/internal/robots"
	"propertyboss/internal/scan"
)

const SourceID = "bezrealitky"

const (
	origin    = "https://www.bezrealitky.cz"
	userAgent = "PropertyBoss/0.3 (real-estate price monitoring)"
)

var dealPath = map[string]string{
	"sale": "prodej",
	"rent": "pronajem",
}
var typePath = map[string]string{
	"flat":  "byt",
	"house": "dum",
	"land":  "pozemek",
}

func init() { scan.Register(&Connector{http: &http.Client{Timeout: 30 * time.Second}}) }

type Connector struct {
	http  *http.Client
	delay time.Duration
}

func (c *Connector) SetPageDelay(d time.Duration) { c.delay = d }
func (c *Connector) SourceID() string             { return SourceID }

func (c *Connector) Scan(ctx context.Context, cfg scan.SearchConfig) ([]scan.Listing, *scan.Issue, error) {
	deal, ok := dealPath[cfg.DealType]
	if !ok {
		return nil, nil, fmt.Errorf("bezrealitky: deal_type %q", cfg.DealType)
	}
	if cfg.PropertyType == nil {
		return nil, nil, fmt.Errorf("bezrealitky: нужен property_type")
	}
	cat, ok := typePath[strings.ToLower(*cfg.PropertyType)]
	if !ok {
		return nil, nil, fmt.Errorf("bezrealitky: property_type %q не в byt/dum/pozemek", *cfg.PropertyType)
	}
	if err := c.checkRobots(ctx, "/vypis/nabidka-"+deal+"/"+cat); err != nil {
		return nil, nil, err
	}
	base := origin + "/vypis/nabidka-" + deal + "/" + cat
	var all []scan.Listing
	seen := map[string]bool{}
	for page := 1; page <= 5000; page++ {
		if err := ctx.Err(); err != nil {
			return all, &scan.Issue{Kind: scan.FailNetwork, Message: err.Error()}, nil
		}
		u := base
		if page > 1 {
			u = fmt.Sprintf("%s?page=%d", base, page)
		}
		body, err := c.fetch(ctx, u)
		if err != nil {
			if len(all) == 0 {
				return nil, nil, err
			}
			return all, &scan.Issue{Kind: scan.Classify(err), Message: fmt.Sprintf("страница %d: %v", page, err)}, nil
		}
		ls, err := ParseListingPage(body, origin)
		if err != nil {
			if len(all) == 0 {
				return nil, &scan.Issue{Kind: scan.FailLayoutChange, Message: err.Error()}, nil
			}
			return all, &scan.Issue{Kind: scan.FailLayoutChange, Message: err.Error()}, nil
		}
		for _, l := range ls {
			if !InCZbbox(l.Lat, l.Lng) {
				continue
			}
			if seen[l.ExternalID] {
				continue
			}
			seen[l.ExternalID] = true
			all = append(all, l)
		}
		if len(ls) == 0 {
			break
		}
	}
	if len(all) == 0 {
		return []scan.Listing{}, nil, nil
	}
	return all, nil, nil
}

func (c *Connector) checkRobots(ctx context.Context, path string) error {
	body, err := c.fetch(ctx, origin+"/robots.txt")
	if err != nil {
		return err
	}
	f := robots.Parse(body)
	u := origin + path
	if !f.Allowed(userAgent, u) {
		return fmt.Errorf("bezrealitky: robots.txt запрещает %s", path)
	}
	return nil
}

func (c *Connector) fetch(ctx context.Context, u string) (string, error) {
	if err := ratelimit.WaitSource(ctx); err != nil {
		return "", scan.NewFail(scan.FailNetwork, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", scan.NewFail(scan.FailNetwork, err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", scan.NewFail(scan.FailNetwork, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return "", scan.NewFailf(scan.FailHTTP429, "GET %s: 429", u)
	}
	if resp.StatusCode/100 != 2 {
		return "", scan.NewFailf(scan.FailNetwork, "GET %s: HTTP %d", u, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", scan.NewFailf(scan.FailNetwork, "GET %s: %v", u, err)
	}
	return string(b), nil
}
