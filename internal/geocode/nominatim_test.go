// Тесты клиента Nominatim на mock-сервере (httptest).
package geocode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ua — фиксированный User-Agent с контактом (политика Nominatim).
const testUA = "PropertyBoss/1.0 (test)"

// newTestServer — mock Nominatim: записывает запрос, отдаёт body.
func newTestServer(t *testing.T, body string, wantUA *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantUA != nil {
			*wantUA = r.Header.Get("User-Agent")
		}
		t.Logf("GET %s", r.URL.String())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func TestNominatimResolve(t *testing.T) {
	var gotUA string
	srv := newTestServer(t, `[{"lat":"50.0875","lon":"14.4213","type":"city","name":"Praha"}]`, &gotUA)
	defer srv.Close()

	cl := NewClient(srv.URL, testUA, 60, 10)
	p, err := cl.Resolve(context.Background(), Query{Street: "Divišková", City: "Praha 5", Country: "Czechia", CountryCode: "CZ"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Lat != 50.0875 || p.Lng != 14.4213 || p.Type != "city" {
		t.Errorf("p = %+v", p)
	}
	if gotUA != testUA {
		t.Errorf("User-Agent = %q, ожидался %q (политика Nominatim)", gotUA, testUA)
	}
}

func TestNominatimEmptyResult(t *testing.T) {
	srv := newTestServer(t, `[]`, nil)
	defer srv.Close()

	cl := NewClient(srv.URL, testUA, 60, 10)
	p, err := cl.Resolve(context.Background(), Query{Street: "Неизвестная улица 1", Country: "Czechia", CountryCode: "CZ"})
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Errorf("p = %+v, ожидалось nil (пустой список)", p)
	}
}

func TestNominatimRateLimit(t *testing.T) {
	srv := newTestServer(t, `[{"lat":"50","lon":"14","type":"city"}]`, nil)
	defer srv.Close()

	cl := NewClient(srv.URL, testUA, 60, 10)
	qa := Query{Street: "A", Country: "Czechia", CountryCode: "CZ"}
	if _, err := cl.Resolve(context.Background(), qa); err != nil {
		t.Fatal(err)
	}
	// 60/мин = 1 с интервал: второй запрос должен выждать ~1 с.
	t0 := time.Now()
	if _, err := cl.Resolve(context.Background(), qa); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(t0); waited < 800*time.Millisecond {
		t.Errorf("второй запрос через %v, ожидалось ≥ ~1 с (rate 60/мин)", waited)
	}
}

func TestNominatimHTTP429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`Too many requests`))
	}))
	defer srv.Close()

	cl := NewClient(srv.URL, testUA, 60, 10)
	_, err := cl.Resolve(context.Background(), Query{Street: "A", Country: "Czechia", CountryCode: "CZ"})
	if err == nil {
		t.Fatal("ожидалась ошибка 429")
	}
	if want := "429"; !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, ожидалось упоминание %q", err, want)
	}
}

func TestNominatimHTTP500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cl := NewClient(srv.URL, testUA, 60, 10)
	if _, err := cl.Resolve(context.Background(), Query{Street: "A", Country: "Czechia", CountryCode: "CZ"}); err == nil {
		t.Fatal("ожидалась ошибка 500")
	}
}

func TestNominatimEmptyAddress(t *testing.T) {
	cl := NewClient("https://example.invalid", testUA, 60, 10)
	if _, err := cl.Resolve(context.Background(), Query{Street: "  ", Country: "Czechia", CountryCode: "CZ"}); err == nil {
		t.Fatal("ожидалась ошибка: пустой адрес (страна не является адресом)")
	}
}
