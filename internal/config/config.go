// Package config — загрузка конфигурации из YAML.
//
// ТЗ §0.1: пороги и коэффициенты живут в конфиге с указанием источника
// значения, а не числом в коде.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Database struct {
		DSN string `yaml:"dsn"`
	} `yaml:"database"`

	FX struct {
		BaseURL      string `yaml:"base_url"`
		UserAgent    string `yaml:"user_agent"`
		BackfillDays int    `yaml:"backfill_days"`
		// Зеркальный API (frankfurter.dev) для окна бэкфила: собственные
		// эндпоинты ЕЦБ с этой сети игнорируют from/to (проверено 2026-08-25).
		// Необязательно: если пусто, синхронизация идёт только прямым каналом.
		FallbackBaseURL string `yaml:"fallback_base_url"`
	} `yaml:"fx"`

	Dashboard struct {
		Listen   string `yaml:"listen"`
		// Целевые рынки этапа 1 (источник: ТЗ §1)
		Countries        []string          `yaml:"countries"`
		MarketCurrencies map[string]string `yaml:"market_currencies"`
		// Типы сделок (источник: ТЗ §7.2 — «продажа / аренда»)
		DealTypes []string `yaml:"deal_types"`
	} `yaml:"dashboard"`
}

// Load читает и валидирует конфиг из файла.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: чтение %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config: разбор YAML: %w", err)
	}
	if c.Database.DSN == "" {
		return nil, fmt.Errorf("config: database.dsn не задан")
	}
	if c.FX.BaseURL == "" {
		return nil, fmt.Errorf("config: fx.base_url не задан")
	}
	if c.FX.UserAgent == "" {
		return nil, fmt.Errorf("config: fx.user_agent не задан (ЕЦБ отклоняет запросы без User-Agent)")
	}
	if c.FX.BackfillDays <= 0 {
		return nil, fmt.Errorf("config: fx.backfill_days должен быть > 0, задано %d", c.FX.BackfillDays)
	}
	if len(c.Dashboard.Countries) == 0 {
		return nil, fmt.Errorf("config: dashboard.countries не задан")
	}
	for _, country := range c.Dashboard.Countries {
		if c.Dashboard.MarketCurrencies[country] == "" {
			return nil, fmt.Errorf("config: dashboard.market_currencies не задан для %s", country)
		}
	}
	if c.Dashboard.Listen == "" {
		return nil, fmt.Errorf("config: dashboard.listen не задан")
	}
	return &c, nil
}
