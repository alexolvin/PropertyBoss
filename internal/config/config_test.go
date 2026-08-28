package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Этап 6 (ТЗ §8.2): валидация блока delist — пороги живут в конфиге
// (ТЗ §0.1), и некорректный конфиг не должен доходить до пасса.

const cfgBase = `
database:
  dsn: "postgres://u:p@127.0.0.1:5432/db?sslmode=disable"
fx:
  base_url: "https://www.ecb.europa.eu"
  user_agent: "PropertyBoss-test"
  backfill_days: 30
dashboard:
  listen: "127.0.0.1:0"
  countries: [CZ]
  market_currencies:
    CZ: CZK
  deal_types: [sale]
dedupe:
  by_country:
    CZ: { radius_m: 50, area_tolerance_pct: 10, address_similarity: 0.9 }
scan:
  page_delay_ms: 100
valuation:
  min_obs_per_param: 10
  min_obs_per_zone: 30
  max_missing_rate: 0.5
  kfold: 5
  lambda_grid: [0.1, 1, 10]
delist:
  min_consecutive_misses: 2
  max_delisted_share_pct: 25
  url_check_timeout_sec: 10
`

const delistBlockDefault = "delist:\n  min_consecutive_misses: 2\n  max_delisted_share_pct: 25\n  url_check_timeout_sec: 10"

func writeCfg(t *testing.T, delistBlock string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(strings.Replace(cfgBase, delistBlockDefault, delistBlock, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDelistValidation(t *testing.T) {
	cases := []struct {
		name  string
		block string
		want  string // ожидаемый фрагмент ошибки; "" — ошибки нет
	}{
		{"valid", delistBlockDefault, ""},
		{"misses_1", "delist:\n  min_consecutive_misses: 1\n  max_delisted_share_pct: 25\n  url_check_timeout_sec: 10", "min_consecutive_misses"},
		{"share_0", "delist:\n  min_consecutive_misses: 2\n  max_delisted_share_pct: 0\n  url_check_timeout_sec: 10", "max_delisted_share_pct"},
		{"share_101", "delist:\n  min_consecutive_misses: 2\n  max_delisted_share_pct: 101\n  url_check_timeout_sec: 10", "max_delisted_share_pct"},
		{"timeout_0", "delist:\n  min_consecutive_misses: 2\n  max_delisted_share_pct: 25\n  url_check_timeout_sec: 0", "url_check_timeout_sec"},
		// Блок delist отсутствует — поля 0 — валидация должна сработать.
		{"missing", "", "min_consecutive_misses"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load(writeCfg(t, c.block))
			if c.want == "" {
				if err != nil {
					t.Fatalf("Load: %v, ждали успех", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Load: %v, ждали ошибку про %q", err, c.want)
			}
		})
	}
}
