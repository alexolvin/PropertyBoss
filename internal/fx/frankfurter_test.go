package fx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Форма ответа frankfurter.dev для диапазона (реальная, 2026-08-25):
// выходные в ответе отсутствуют — это ожидаемо, а не ошибка.
const frankfurterRangeJSON = `{"amount":1.0,"base":"EUR","start_date":"2026-08-17","end_date":"2026-08-21",
"rates":{"2026-08-17":{"CZK":24.201,"USD":1.1593},"2026-08-18":{"CZK":24.179,"USD":1.1576},
"2026-08-19":{"CZK":24.163},"2026-08-20":{"CZK":24.153},"2026-08-21":{"CZK":24.116}}}`

func frankfurterServer(t *testing.T, status int, body string) *FrankfurterClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &FrankfurterClient{BaseURL: srv.URL + "/v1", HTTP: srv.Client()}
}

func TestFrankfurterFetchRange(t *testing.T) {
	c := frankfurterServer(t, http.StatusOK, frankfurterRangeJSON)
	from, _ := time.Parse("2006-01-02", "2026-08-10")
	to, _ := time.Parse("2006-01-02", "2026-08-26")
	days, err := c.FetchRange(t.Context(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 5 {
		t.Fatalf("ожидалось 5 дней, получилось %d", len(days))
	}
	if !days[0].Date.Before(days[4].Date) {
		t.Error("дни не отсортированы по возрастанию")
	}
	// Курс — точная строка из JSON, без float-промежуточного (ТЗ §5).
	if got := days[0].Rates["CZK"]; got != "24.201" {
		t.Errorf("CZK 2026-08-17 = %q, ожидалось %q", got, "24.201")
	}
	if got := days[2].Rates["CZK"]; got != "24.163" {
		t.Errorf("CZK 2026-08-19 = %q, ожидалось %q", got, "24.163")
	}
	// Отсутствующая валюта днём — просто отсутствует, не подставляется.
	if _, ok := days[2].Rates["USD"]; ok {
		t.Error("USD 2026-08-19 отсутствует в ответе — не должен появляться")
	}
}

func TestFrankfurterBadBase(t *testing.T) {
	c := frankfurterServer(t, http.StatusOK, `{"base":"USD","rates":{}}`)
	from, _ := time.Parse("2006-01-02", "2026-08-10")
	to, _ := time.Parse("2006-01-02", "2026-08-26")
	if _, err := c.FetchRange(t.Context(), from, to); err == nil {
		t.Error("ожидалась ошибка: базовая валюта не EUR")
	}
}

func TestFrankfurterBadDate(t *testing.T) {
	c := frankfurterServer(t, http.StatusOK, `{"base":"EUR","rates":{"17.08.2026":{"CZK":1}}}`)
	from, _ := time.Parse("2006-01-02", "2026-08-10")
	to, _ := time.Parse("2006-01-02", "2026-08-26")
	if _, err := c.FetchRange(t.Context(), from, to); err == nil {
		t.Error("ожидалась ошибка: дата вне формата")
	}
}

func TestFrankfurterNullRateSkipped(t *testing.T) {
	c := frankfurterServer(t, http.StatusOK, `{"base":"EUR","rates":{"2026-08-17":{"CZK":null,"USD":1.1}}}`)
	from, _ := time.Parse("2006-01-02", "2026-08-10")
	to, _ := time.Parse("2006-01-02", "2026-08-26")
	days, err := c.FetchRange(t.Context(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 {
		t.Fatalf("ожидалось 1 день, получилось %d", len(days))
	}
	if _, ok := days[0].Rates["CZK"]; ok {
		t.Error("null-курс не должен попадать в Rates (это не курс)")
	}
	if got := days[0].Rates["USD"]; got != "1.1" {
		t.Errorf("USD = %q, ожидалось %q", got, "1.1")
	}
}

func TestFrankfurterHTTPError(t *testing.T) {
	c := frankfurterServer(t, http.StatusNotFound, strings.Repeat("x", 100))
	from, _ := time.Parse("2006-01-02", "2026-08-10")
	to, _ := time.Parse("2006-01-02", "2026-08-26")
	if _, err := c.FetchRange(t.Context(), from, to); err == nil {
		t.Error("ожидалась ошибка на HTTP 404")
	}
}
