// Клиент публичного API frankfurter.dev: те же референсные курсы ЕЦБ
// (база EUR) для произвольного диапазона дат, JSON:
//
//	{"base":"EUR","rates":{"2026-08-17":{"CZK":24.201,"USD":1.1593},...}}
//
// Причина использования (проверено с vzu5-claw 2026-08-25): собственные
// эндпоинты ЕЦБ (eurofxref-daily.xml и eurofxref.xml) с этой сети игнорируют
// параметры from/to по датам и отдают ровно один день, поэтому окно бэкфила
// через прямой канал невозможно. Данные по-прежнему ЕЦБ; путь явно
// атрибутируется в fx_rates.source (ТЗ §13).
//
// Курсы читаются как json.Number — точные десятичные строки, float не
// используется (ТЗ §5).
package fx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"
)

type FrankfurterClient struct {
	BaseURL string // например "https://api.frankfurter.dev/v1"
	HTTP    *http.Client
}

// FetchRange получает курсы ЕЦБ за диапазон дат (включительно).
// Пустые дни (выходные/праздники) в ответе отсутствуют — подставлять их
// запрещено (ТЗ §5), они и не подставляются.
func (c *FrankfurterClient) FetchRange(ctx context.Context, from, to time.Time) ([]DayRates, error) {
	u := fmt.Sprintf("%s/%s..%s?base=EUR", c.BaseURL, from.Format("2006-01-02"), to.Format("2006-01-02"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("frankfurter: запрос: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("frankfurter: HTTP %d: %.200s", resp.StatusCode, body)
	}
	return parseFrankfurter(io.LimitReader(resp.Body, 8<<20))
}

func parseFrankfurter(r io.Reader) ([]DayRates, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var body struct {
		Base  string                        `json:"base"`
		Rates map[string]map[string]json.Number `json:"rates"`
	}
	if err := dec.Decode(&body); err != nil {
		return nil, fmt.Errorf("frankfurter: разбор JSON: %w", err)
	}
	if body.Base != "EUR" {
		return nil, fmt.Errorf("frankfurter: базовая валюта %q, ожидалась EUR", body.Base)
	}
	out := []DayRates{}
	for dateStr, rates := range body.Rates {
		d, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			// Дата вне формата — не гадать, а зафиксировать (ТЗ §0.4).
			return nil, fmt.Errorf("frankfurter: некорректная дата %q", dateStr)
		}
		m := map[string]string{}
		for cur, n := range rates {
			s := n.String()
			if s == "" {
				continue // отсутствующий курс — это не курс (null в JSON)
			}
			m[cur] = s
		}
		out = append(out, DayRates{Date: d, Rates: m})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, nil
}
