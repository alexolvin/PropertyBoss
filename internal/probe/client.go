package probe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"

	"propertyboss/internal/ratelimit"
)

const (
	maxBody    = 2 << 20 // 2 MiB на артефакт
	MaxBudget  = 80      // ТЗ-SRC-CZ-1 §5
	DefaultUA  = "PropertyBoss/0.3 (real-estate price monitoring)"
)

// Client — один HTTP-клиент пробы: UA из конфига, cookie jar сессии,
// 1 соединение, WaitSource, circuit breaker (A5–A8).
type Client struct {
	UA      string
	Budget  int // 0 → MaxBudget
	http    *http.Client
	breakN  int
	stopped bool
	n       int
	hops    []string
}

func NewClient(ua string, timeout time.Duration) *Client {
	if ua == "" {
		ua = DefaultUA
	}
	c := &Client{UA: ua, Budget: MaxBudget}
	jar, _ := cookiejar.New(nil)
	tr := &http.Transport{MaxConnsPerHost: 1, DisableCompression: false}
	c.http = &http.Client{
		Timeout:   timeout,
		Jar:       jar,
		Transport: tr,
		// Редиректы анонимного посетителя — да; куки сессии сохраняет jar.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("слишком много редиректов")
			}
			c.hops = append(c.hops, req.URL.String())
			return nil
		},
	}
	return c
}

func (c *Client) Requests() int { return c.n }
func (c *Client) Remain() int {
	b := c.Budget
	if b <= 0 {
		b = MaxBudget
	}
	if c.n >= b {
		return 0
	}
	return b - c.n
}

// Rec — одна строка requests.jsonl (имена cookies, не значения).
type Rec struct {
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	Status      int               `json:"status"`
	FinalURL    string            `json:"final_url"`
	Redirects   []string          `json:"redirects,omitempty"`
	Headers     map[string]string `json:"headers"`
	CookieNames []string          `json:"set_cookie_names"`
	Bytes       int               `json:"bytes"`
	SHA256      string            `json:"sha256"`
	DurationMS  int64             `json:"duration_ms"`
	Kind        Kind              `json:"kind,omitempty"`
	Err         string            `json:"err,omitempty"`
}

func (c *Client) Stopped() bool { return c.stopped }

func (c *Client) Get(ctx context.Context, rawURL string) ([]byte, Rec, Kind, error) {
	return c.do(ctx, http.MethodGet, rawURL, "text/html,application/xhtml+xml,application/json,application/xml;q=0.9,*/*;q=0.8", nil)
}

// PostJSON — read-only GraphQL/JSON, найденный во фронтенде (A3).
func (c *Client) PostJSON(ctx context.Context, rawURL string, payload []byte) ([]byte, Rec, Kind, error) {
	return c.do(ctx, http.MethodPost, rawURL, "application/json", payload)
}

func (c *Client) do(ctx context.Context, method, rawURL, accept string, payload []byte) (body []byte, rec Rec, kind Kind, err error) {
	rec = Rec{Method: method, URL: rawURL}
	if c.stopped {
		rec.Err = "circuit breaker: проба остановлена"
		return nil, rec, KindNone, fmt.Errorf("%s", rec.Err)
	}
	bgt := c.Budget
	if bgt <= 0 {
		bgt = MaxBudget
	}
	if c.n >= bgt {
		rec.Err = fmt.Sprintf("бюджет пробы исчерпан (%d)", bgt)
		return nil, rec, KindNone, fmt.Errorf("%s", rec.Err)
	}
	if err := ratelimit.WaitSource(ctx); err != nil {
		rec.Err = err.Error()
		return nil, rec, KindNone, err
	}
	c.n++
	c.hops = nil
	var rdr io.Reader
	if payload != nil {
		rdr = strings.NewReader(string(payload))
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		rec.Err = err.Error()
		return nil, rec, KindNone, err
	}
	req.Header.Set("User-Agent", c.UA)
	req.Header.Set("Accept", accept)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	t0 := time.Now()
	resp, err := c.http.Do(req)
	rec.DurationMS = time.Since(t0).Milliseconds()
	if err != nil {
		rec.Err = err.Error()
		return nil, rec, KindNone, err
	}
	defer resp.Body.Close()
	rec.Status = resp.StatusCode
	rec.FinalURL = resp.Request.URL.String()
	if len(c.hops) > 0 {
		rec.Redirects = append([]string(nil), c.hops...)
	}
	rec.Headers = map[string]string{
		"server":       resp.Header.Get("Server"),
		"content-type": resp.Header.Get("Content-Type"),
		"retry-after":  resp.Header.Get("Retry-After"),
		"cf-ray":       resp.Header.Get("Cf-Ray"),
		"cf-mitigated": resp.Header.Get("Cf-Mitigated"),
	}
	if xd := resp.Header.Get("X-Datadome"); xd != "" {
		rec.Headers["x-datadome"] = "present"
	}
	for _, ck := range resp.Header.Values("Set-Cookie") {
		name := ck
		if i := strings.IndexByte(ck, '='); i > 0 {
			name = ck[:i]
		}
		rec.CookieNames = append(rec.CookieNames, name)
	}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		d := parseRetryAfter(ra)
		if d > 0 && d < 5*time.Minute {
			select {
			case <-ctx.Done():
			case <-time.After(d):
			}
		}
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	ct := resp.Header.Get("Content-Type")
	red := Redact(raw, ct)
	rec.Bytes = len(red)
	sum := sha256.Sum256(red)
	rec.SHA256 = hex.EncodeToString(sum[:])
	kind = Detect(resp.StatusCode, resp.Header, raw)
	rec.Kind = kind
	trip := resp.StatusCode == 403 || resp.StatusCode == 429 || kind == KindChallenge || kind == KindCaptcha
	if trip {
		c.breakN++
		if c.breakN >= 3 {
			c.stopped = true
		}
	} else {
		c.breakN = 0
	}
	return red, rec, kind, nil
}

func parseRetryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if d, err := time.ParseDuration(h + "s"); err == nil && d > 0 {
		return d
	}
	if t, err := time.Parse(time.RFC1123, h); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}
	return 0
}
