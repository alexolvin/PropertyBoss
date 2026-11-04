// nominatim.go — клиент Nominatim (OpenStreetMap, данные под ODbL 1.0).
//
// Политика использования: валидный User-Agent с контактом и не более
// 1 запроса/с. Клиент держит минимальный интервал между запросами из
// rate_per_min конфигурации (по умолчанию 50/мин = 1.2 с).
package geocode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client — поиск адресов на Nominatim. HTTP подменяется в тестах.
type Client struct {
	base        string
	userAgent   string
	http        *http.Client
	minInterval time.Duration // 60 c / rate_per_min
	mu          sync.Mutex
	last        time.Time
}

// NewClient — base (без хвостового /), ratePerMin в [1..60].
func NewClient(base, userAgent string, ratePerMin, timeoutSec int) *Client {
	return &Client{
		base:        strings.TrimRight(base, "/"),
		userAgent:   userAgent,
		http:        &http.Client{Timeout: time.Duration(timeoutSec) * time.Second},
		minInterval: 60 * time.Second / time.Duration(ratePerMin),
	}
}

// nominatimResp — элемент jsonv2-ответа (только нужные поля).
type nominatimResp struct {
	Lat  string `json:"lat"`
	Lon  string `json:"lon"`
	Type string `json:"type"`
}

// langByCountry — ISO 639 для accept-language Nominatim.
var langByCountry = map[string]string{
	"CZ": "cs", "IT": "it", "NL": "nl",
}

// Resolve — адрес → первый результат поиска Nominatim. Пустой список —
// (nil, nil): адрес не резолвится (не ошибка). Только страна — ошибка:
// «Czechia» резолвится в центроид страны, для объекта это ничего не
// значит, а лимит тратится (вызывающий уже проверяет street/city).
func (c *Client) Resolve(ctx context.Context, q Query) (*Point, error) {
	if !q.hasPlace() {
		return nil, fmt.Errorf("geocode: пустой адрес (улица и город пустые)")
	}
	parts := q.parts()

	vals := url.Values{}
	vals.Set("format", "jsonv2")
	vals.Set("limit", "1")
	vals.Set("q", strings.Join(parts, ", "))
	lang := "cs"
	if q.CountryCode != "" {
		if l, ok := langByCountry[strings.ToUpper(q.CountryCode)]; ok {
			lang = l
		}
		vals.Set("countrycodes", strings.ToLower(q.CountryCode))
	}
	vals.Set("accept-language", lang)

	if err := c.waitSlot(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/search?"+vals.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("geocode: запрос: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("geocode: сеть: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("geocode: чтение ответа: %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("geocode: Nominatim 429 (лимит)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("geocode: Nominatim HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var res []nominatimResp
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, fmt.Errorf("geocode: ответ Nominatim не JSON: %w", err)
	}
	if len(res) == 0 {
		return nil, nil
	}
	lat, err := strconv.ParseFloat(res[0].Lat, 64)
	if err != nil {
		return nil, fmt.Errorf("geocode: Nominatim lat %q: %w", res[0].Lat, err)
	}
	lng, err := strconv.ParseFloat(res[0].Lon, 64)
	if err != nil {
		return nil, fmt.Errorf("geocode: Nominatim lon %q: %w", res[0].Lon, err)
	}
	return &Point{Lat: lat, Lng: lng, Type: res[0].Type}, nil
}

// waitSlot — выдерживает минимальный интервал между запросами (политика
// Nominatim). Ожидание учитывает контекст.
func (c *Client) waitSlot(ctx context.Context) error {
	c.mu.Lock()
	now := time.Now()
	wait := c.last.Add(c.minInterval).Sub(now)
	if wait > 0 {
		c.last = now.Add(wait)
	} else {
		c.last = now
	}
	c.mu.Unlock()
	if wait > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return nil
}
