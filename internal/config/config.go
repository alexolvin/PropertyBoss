// Package config — загрузка конфигурации из YAML.
//
// ТЗ §0.1: пороги и коэффициенты живут в конфиге с указанием источника
// значения, а не числом в коде.
package config

import (
	"fmt"
	"os"
	"time"

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

	// Дедупликация объявлений (ТЗ §8.1): параметры сопоставления
	// записей из разных источников в один физический объект.
	// ТЗ: радиус «из конфига по стране (плотная европейская застройка
	// и пригород требуют разного)» — значения здесь, не в коде (ТЗ §0.1).
	Dedupe struct {
		ByCountry map[string]DedupeParams `yaml:"by_country"`
	} `yaml:"dedupe"`

	// Scan — параметры прогона сканера (этап 3).
	Scan struct {
		// Вежливость: пауза между запросами страниц категории, мс
		// (коннекторы, идущие по страницам). Источник значения:
		// оператор, вежливый уровень для публичного сайта (ТЗ §0.1).
		PageDelayMS int `yaml:"page_delay_ms"`
	} `yaml:"scan"`

	// Valuation — гедоническая модель (этап 5, ТЗ §7.2–7.3).
	// ТЗ §7.3: «значение min_obs_per_param в конфиге, не в коде».
	Valuation struct {
		// Минимум наблюдений на параметр модели (ТЗ §7.3, «отраслевой
		// ориентир — от 10 наблюдений на параметр»).
		MinObsPerParam int `yaml:"min_obs_per_param"`
		// Зона с числом активных наблюдений меньше этого берёт zone_effect
		// с родительского уровня иерархии, результат помечается
		// zone_fallback=true (ТЗ §7.3). Допущение исполнителя: 30.
		MinObsPerZone int `yaml:"min_obs_per_zone"`
		// Порог доли пропусков по ключевому (участвующему в модели)
		// атрибуту, (0,1]: превышение — модель отклоняется (ТЗ §7.3).
		// Допущение исполнителя: 0.5.
		MaxMissingRate float64 `yaml:"max_missing_rate"`
		// Число складок k-fold кросс-валидации для подбора λ (ТЗ §7.2:
		// «λ подбирается k-fold кросс-валидацией, а не назначается»).
		KFold int `yaml:"kfold"`
		// Кандидаты λ для гребневой регуляризации, по возрастанию.
		// Допущение исполнителя: стандартная логарифмическая сетка.
		LambdaGrid []float64 `yaml:"lambda_grid"`
	} `yaml:"valuation"`
}

// DedupeParams — пороги сопоставления для одной страны.
type DedupeParams struct {
	// Радиус сравнения координат, м. ТЗ §8.1: «радиус 50 м из v1
	// сохраняется» (значения по странам — в конфиге).
	RadiusM int `yaml:"radius_m"`
	// Максимально допустимое различие площади, % (0..100).
	AreaTolerancePct int `yaml:"area_tolerance_pct"`
	// Порог сходства нормализованных адресов (0..1]. Совпадение только
	// по адресу помечается match_confidence='low' (ТЗ §8.1).
	AddressSimilarity float64 `yaml:"address_similarity"`
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
	for _, country := range c.Dashboard.Countries {
		p, ok := c.Dedupe.ByCountry[country]
		if !ok {
			return nil, fmt.Errorf("config: dedupe.by_country не задан для %s (ТЗ §8.1)", country)
		}
		if p.RadiusM <= 0 {
			return nil, fmt.Errorf("config: dedupe.by_country.%s.radius_m должен быть > 0", country)
		}
		if p.AreaTolerancePct <= 0 || p.AreaTolerancePct > 100 {
			return nil, fmt.Errorf("config: dedupe.by_country.%s.area_tolerance_pct должен быть в 1..100", country)
		}
		if p.AddressSimilarity <= 0 || p.AddressSimilarity > 1 {
			return nil, fmt.Errorf("config: dedupe.by_country.%s.address_similarity должен быть в (0, 1]", country)
		}
	}
	if c.Scan.PageDelayMS < 0 {
		return nil, fmt.Errorf("config: scan.page_delay_ms должен быть >= 0, задано %d", c.Scan.PageDelayMS)
	}
	if c.Scan.PageDelayMS == 0 {
		c.Scan.PageDelayMS = 1000 // разумный вежливый уровень по умолчанию
	}
	if c.Valuation.MinObsPerParam < 1 {
		return nil, fmt.Errorf("config: valuation.min_obs_per_param должен быть >= 1, задано %d (ТЗ §7.3)", c.Valuation.MinObsPerParam)
	}
	if c.Valuation.MinObsPerZone < 1 {
		return nil, fmt.Errorf("config: valuation.min_obs_per_zone должен быть >= 1, задано %d (ТЗ §7.3)", c.Valuation.MinObsPerZone)
	}
	if c.Valuation.MaxMissingRate <= 0 || c.Valuation.MaxMissingRate > 1 {
		return nil, fmt.Errorf("config: valuation.max_missing_rate должен быть в (0, 1], задано %v (ТЗ §7.3)", c.Valuation.MaxMissingRate)
	}
	if c.Valuation.KFold < 2 {
		return nil, fmt.Errorf("config: valuation.kfold должен быть >= 2, задано %d (ТЗ §7.2)", c.Valuation.KFold)
	}
	if len(c.Valuation.LambdaGrid) == 0 {
		return nil, fmt.Errorf("config: valuation.lambda_grid не задан (ТЗ §7.2)")
	}
	for i, l := range c.Valuation.LambdaGrid {
		if l <= 0 {
			return nil, fmt.Errorf("config: valuation.lambda_grid[%d] должен быть > 0, задано %v", i, l)
		}
		if i > 0 && l < c.Valuation.LambdaGrid[i-1] {
			return nil, fmt.Errorf("config: valuation.lambda_grid не отсортирован по возрастанию (позиция %d)", i)
		}
	}
	return &c, nil
}

// ScanPageDelay — пауза между запросами страниц (scan.page_delay_ms).
func (c *Config) ScanPageDelay() time.Duration {
	return time.Duration(c.Scan.PageDelayMS) * time.Millisecond
}
