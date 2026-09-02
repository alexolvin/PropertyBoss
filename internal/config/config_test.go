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
liquidity:
  min_events: 100
  max_calib_dev: 0.10
  horizon_days: 30
  holdout_ratio: 0.25
schedule:
  exploration_fraction: 0.10
  ma_window_days: 14
  min_obs_for_tuning: 14
  backoff_base_minutes: 60
  backoff_multiplier: 2
  backoff_max_hours: 72
  recovery_step: 1.5
  min_rate_factor: 0.25
  country_timezones:
    CZ: Europe/Prague
telegram:
  enabled: false
notify:
  flush_limit: 100
  disk_path: ""
  disk_critical_pct: 10
  disk_realert_minutes: 1440
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

// Этап 7 (ТЗ §9): валидация блока liquidity — пороги min_events и
// калибровки живут в конфиге (ТЗ §0.1).

const liquidityBlockDefault = "liquidity:\n  min_events: 100\n  max_calib_dev: 0.10\n  horizon_days: 30\n  holdout_ratio: 0.25"

func writeCfgLiq(t *testing.T, block string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(strings.Replace(cfgBase, liquidityBlockDefault, block, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadLiquidityValidation(t *testing.T) {
	cases := []struct {
		name  string
		block string
		want  string // ожидаемый фрагмент ошибки; "" — ошибки нет
	}{
		{"valid", liquidityBlockDefault, ""},
		{"events_5", "liquidity:\n  min_events: 5\n  max_calib_dev: 0.10\n  horizon_days: 30\n  holdout_ratio: 0.25", "min_events"},
		{"calib_0", "liquidity:\n  min_events: 100\n  max_calib_dev: 0\n  horizon_days: 30\n  holdout_ratio: 0.25", "max_calib_dev"},
		{"calib_2", "liquidity:\n  min_events: 100\n  max_calib_dev: 2\n  horizon_days: 30\n  holdout_ratio: 0.25", "max_calib_dev"},
		{"horizon_3", "liquidity:\n  min_events: 100\n  max_calib_dev: 0.10\n  horizon_days: 3\n  holdout_ratio: 0.25", "horizon_days"},
		{"holdout_0", "liquidity:\n  min_events: 100\n  max_calib_dev: 0.10\n  horizon_days: 30\n  holdout_ratio: 0", "holdout_ratio"},
		{"holdout_05", "liquidity:\n  min_events: 100\n  max_calib_dev: 0.10\n  horizon_days: 30\n  holdout_ratio: 0.5", "holdout_ratio"},
		{"missing", "", "min_events"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load(writeCfgLiq(t, c.block))
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

// Этап 11 (ТЗ §10): валидация блока schedule.

const scheduleBlockDefault = `schedule:
  exploration_fraction: 0.10
  ma_window_days: 14
  min_obs_for_tuning: 14
  backoff_base_minutes: 60
  backoff_multiplier: 2
  backoff_max_hours: 72
  recovery_step: 1.5
  min_rate_factor: 0.25
  country_timezones:
    CZ: Europe/Prague`

func writeCfgSched(t *testing.T, block string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(strings.Replace(cfgBase, scheduleBlockDefault, block, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadScheduleValidation(t *testing.T) {
	cases := []struct {
		name  string
		block string
		want  string // ожидаемый фрагмент ошибки; "" — ошибки нет
	}{
		{"valid", scheduleBlockDefault, ""},
		{"eps_0", schedBlock("exploration_fraction: 0"), "exploration_fraction"},
		{"eps_06", schedBlock("exploration_fraction: 0.6"), "exploration_fraction"},
		{"ma_0", schedBlock("ma_window_days: 0"), "ma_window_days"},
		{"minobs_0", schedBlock("min_obs_for_tuning: 0"), "min_obs_for_tuning"},
		{"base_0", schedBlock("backoff_base_minutes: 0"), "backoff_base_minutes"},
		{"mult_05", schedBlock("backoff_multiplier: 0.5"), "backoff_multiplier"},
		{"max_0", schedBlock("backoff_max_hours: 0"), "backoff_max_hours"},
		{"base_gt_max", schedBlock("backoff_base_minutes: 120", "backoff_max_hours: 1"), "backoff_base_minutes"},
		{"recovery_1", schedBlock("recovery_step: 1"), "recovery_step"},
		{"rate_1", schedBlock("min_rate_factor: 1"), "min_rate_factor"},
		{"rate_0", schedBlock("min_rate_factor: 0"), "min_rate_factor"},
		{"tz_missing", "schedule:\n  exploration_fraction: 0.10\n  ma_window_days: 14\n  min_obs_for_tuning: 14\n  backoff_base_minutes: 60\n  backoff_multiplier: 2\n  backoff_max_hours: 72\n  recovery_step: 1.5\n  min_rate_factor: 0.25", "country_timezones"},
		{"tz_bad", schedBlock("CZ: Mars/Olympus"), "неизвестный IANA-пояс"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load(writeCfgSched(t, c.block))
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

// schedBlock — schedule-блок из scheduleBlockDefault с заменёнными
// строками (по одной или две): schedBlock("backoff_max_hours: 1").
func schedBlock(lines ...string) string {
	out := scheduleBlockDefault
	// Ключ — имя параметра (до «:»), значение — строка блока по умолчанию.
	defaults := map[string]string{
		"exploration_fraction": "exploration_fraction: 0.10",
		"ma_window_days":       "ma_window_days: 14",
		"min_obs_for_tuning":   "min_obs_for_tuning: 14",
		"backoff_base_minutes": "backoff_base_minutes: 60",
		"backoff_multiplier":   "backoff_multiplier: 2",
		"backoff_max_hours":    "backoff_max_hours: 72",
		"recovery_step":        "recovery_step: 1.5",
		"min_rate_factor":      "min_rate_factor: 0.25",
		"CZ":                   "CZ: Europe/Prague",
	}
	for _, line := range lines {
		name := line[:len(line)-len(valueOf(line))]
		old := line
		if d, ok := defaults[name]; ok {
			old = d
		}
		out = strings.Replace(out, old, line, 1)
	}
	return out
}

// valueOf — хвост строки «key: value» после первого ':'.
func valueOf(line string) string {
	if i := strings.IndexByte(line, ':'); i >= 0 {
		return line[i:]
	}
	return ""
}

// Этап 8 (ТЗ §2, §3.2): валидация блоков telegram и notify.

const notifyBlockDefault = `notify:
  flush_limit: 100
  disk_path: ""
  disk_critical_pct: 10
  disk_realert_minutes: 1440`

const telegramBlockDefault = "telegram:\n  enabled: false"

func writeCfgNotify(t *testing.T, notifyBlock, telegramBlock string) string {
	t.Helper()
	out := strings.Replace(cfgBase, notifyBlockDefault, notifyBlock, 1)
	if telegramBlock != "" {
		out = strings.Replace(out, telegramBlockDefault, telegramBlock, 1)
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadNotifyValidation(t *testing.T) {
	telegramEnabledNoToken := "telegram:\n  enabled: true"
	cases := []struct {
		name          string
		notifyBlock   string
		telegramBlock string
		want          string // ожидаемый фрагмент ошибки; "" — ошибки нет
	}{
		{"valid", notifyBlockDefault, "", ""},
		{"flush_0_default", `notify:
  flush_limit: 0
  disk_path: ""
  disk_critical_pct: 10
  disk_realert_minutes: 1440`, "", ""},
		{"crit_0", `notify:
  flush_limit: 100
  disk_path: ""
  disk_critical_pct: 0
  disk_realert_minutes: 1440`, "", "disk_critical_pct"},
		{"crit_100", `notify:
  flush_limit: 100
  disk_path: ""
  disk_critical_pct: 100
  disk_realert_minutes: 1440`, "", "disk_critical_pct"},
		{"realert_neg", `notify:
  flush_limit: 100
  disk_path: ""
  disk_critical_pct: 10
  disk_realert_minutes: -5`, "", "disk_realert_minutes"},
		{"missing", "", "", "disk_critical_pct"},
		{"telegram_no_token", notifyBlockDefault, telegramEnabledNoToken, "telegram.token"},
		{"telegram_bad_url", notifyBlockDefault, "telegram:\n  enabled: false\n  base_url: \"ftp://x\"", "telegram.base_url"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load(writeCfgNotify(t, c.notifyBlock, c.telegramBlock))
			if c.want != "" {
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("Load: %v, ждали ошибку про %q", err, c.want)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v, ждали успех", err)
			}
		})
	}
	t.Run("defaults", func(t *testing.T) {
		cfg, err := Load(writeCfgNotify(t, `notify:
  disk_path: ""
  disk_critical_pct: 10
  disk_realert_minutes: 0`, ""))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Notify.FlushLimit != 100 {
			t.Errorf("flush_limit по умолчанию = %d, ждали 100", cfg.Notify.FlushLimit)
		}
		if cfg.Telegram.BaseURL != "https://api.telegram.org" {
			t.Errorf("base_url по умолчанию = %q", cfg.Telegram.BaseURL)
		}
	})
}
