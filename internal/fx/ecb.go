// Package fx — загрузка ежедневных референсных курсов ЕЦБ (ТЗ §5).
//
// Источник: публичный XML-фид eurofxref (ежедневные референсные курсы,
// базовая валюта EUR; CZK входит в набор).
//
// Примечание по доступу (проверено 2026-08-25 с vzu5-claw):
//   - SDMX REST API data-api.ecb.europa.eu блокируется WAF с IP сервера
//     (страница «access has been blocked»);
//   - XML-фид www.ecb.europa.eu/stats/eurofxref/eurofxref-daily.xml
//     доступен при наличии заголовка User-Agent.
//
// Курс фиксируется на дату наблюдения из атрибута time фида;
// подмена отсутствующих дат происходит только в функции fx_rate_for()
// с пометкой stale (ТЗ §5, миграция 0003).
package fx

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"
)

// DayRates — референсные курсы одного дня (базовая валюта EUR).
// Значения — строки: NUMERIC в БД разбирается из строки точно, без float.
type DayRates struct {
	Date  time.Time
	Rates map[string]string // код валюты (quote) → курс, например "24.100"
}

// ECBClient — клиент XML-фида евро-курсов.
type ECBClient struct {
	BaseURL   string
	UserAgent string
	HTTP      *http.Client
}

type xmlCube struct {
	Time     string    `xml:"time,attr"`
	Currency string    `xml:"currency,attr"`
	Rate     string    `xml:"rate,attr"`
	Children []xmlCube `xml:"Cube"`
}

type xmlEnvelope struct {
	Cube xmlCube `xml:"Cube"`
}

// ParseXML разбирает фид. Обходит вложенные Cube рекурсивно: любой узел с
// атрибутом time задаёт дату для себя и всех вложенных узлов; узел с
// currency+rate — это курс. Такая схема устойчива к обеим известным формам
// ответа (один день и диапазон from/to).
func ParseXML(r io.Reader) ([]DayRates, error) {
	var env xmlEnvelope
	if err := xml.NewDecoder(r).Decode(&env); err != nil {
		return nil, fmt.Errorf("fx: разбор XML: %w", err)
	}
	out := []DayRates{}
	seen := map[time.Time]int{}

	var walk func(c xmlCube, date time.Time)
	walk = func(c xmlCube, date time.Time) {
		if c.Time != "" {
			d, err := time.Parse("2006-01-02", c.Time)
			if err != nil {
				// Дата в фиде некорректна — фиксируем и пропускаем поддерево,
				// а не молча подставляем (ТЗ §0.4).
				return
			}
			date = d
		}
		if c.Currency != "" && c.Rate != "" {
			if date.IsZero() {
				return
			}
			i, ok := seen[date]
			if !ok {
				out = append(out, DayRates{Date: date, Rates: map[string]string{}})
				i = len(out) - 1
				seen[date] = i
			}
			out[i].Rates[c.Currency] = c.Rate
		}
		for _, ch := range c.Children {
			walk(ch, date)
		}
	}
	walk(env.Cube, time.Time{})

	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, nil
}

// FetchRange получает курсы за диапазон дат (включительно).
// Пустые дни (выходные/праздники) в ответе ЕЦБ отсутствуют — это нормально,
// подставлять их запрещён (ТЗ §5).
func (c *ECBClient) FetchRange(ctx context.Context, from, to time.Time) ([]DayRates, error) {
	u := fmt.Sprintf("%s?from=%s&to=%s", c.BaseURL, from.Format("2006-01-02"), to.Format("2006-01-02"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fx: запрос к ЕЦБ: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("fx: HTTP %d от ЕЦБ: %.200s", resp.StatusCode, body)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("fx: чтение ответа ЕЦБ: %w", err)
	}
	return ParseXML(bytes.NewReader(data))
}
