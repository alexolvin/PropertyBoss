// Package config — загрузка конфигурации из YAML.
//
// ТЗ §0.1: пороги и коэффициенты живут в конфиге с указанием источника
// значения, а не числом в коде.
package config

import (
	"fmt"
	"net/url"
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
		Listen string `yaml:"listen"`
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

	// Delist — маркировка объектов delisted (этап 6, ТЗ §8.2).
	Delist struct {
		// Сколько ПОСЛЕДОВАТЕЛЬНЫХ полных сканов без объекта нужно,
		// чтобы считать его исчезнувшим. ТЗ §8.2: «минимум 2».
		MinConsecutiveMisses int `yaml:"min_consecutive_misses"`
		// Защита от катастрофы (ТЗ §8.2): если доля исчезнувших за один
		// прогон объектов источника (в % от его активных) превышает это
		// значение, прогон аномален — изменения не применяются,
		// оператор уведомляется.
		MaxDelistedSharePct float64 `yaml:"max_delisted_share_pct"`
		// Таймаут прямого URL-чека объявления, с (ТЗ §8.2, защита 4).
		URLCheckTimeoutSec int `yaml:"url_check_timeout_sec"`
	} `yaml:"delist"`

	// Liquidity — модель ликвидности (этап 7, ТЗ §9): дискретная модель
	// дожития на недельных person-period интервалах.
	Liquidity struct {
		// Минимум завершённых наблюдений (объектов, ушедших с рынка),
		// накопившихся до обучения модели. ТЗ §9.3: «до этого поле
		// hazard_probability равно NULL с причиной insufficient_history».
		// Допущение исполнителя: 100.
		MinEvents int `yaml:"min_events"`
		// Порог калибровки (ТЗ §9.4): модель публикуется, только если
		// максимальное отклонение калибровочной кривой (по децилям
		// предсказанной вероятности) не превышает этого значения.
		// Допущение исполнителя: 0.10 (10 процентных пунктов).
		MaxCalibDev float64 `yaml:"max_calib_dev"`
		// Горизонт прогноза T, дней: выход модели — вероятность ухода
		// с рынка в ближайшие T дней (ТЗ §9.2). Недельная дискретизация:
		// P(T) = 1 − ∏(1 − h_w) по первым ceil(T/7) неделям.
		HorizonDays int `yaml:"horizon_days"`
		// Доля хвоста окна наблюдений, используемого как отложенная по
		// времени проверка (ТЗ §9.4: обучение до T, проверка после T;
		// случайное разбиение запрещено). Допущение исполнителя: 0.25.
		HoldoutRatio float64 `yaml:"holdout_ratio"`
	} `yaml:"liquidity"`

	// Telegram — доставка уведомлений (этап 8, ТЗ §2). Бот — только
	// уведомления, диалога нет. enabled=false — очередь notifications
	// продолжает накапливаться, `pb notify send` явно сообщает, что
	// доставка не настроена.
	Telegram struct {
		Enabled bool   `yaml:"enabled"`
		Token   string `yaml:"token"`
		ChatID  string `yaml:"chat_id"`
		// Базовый URL Bot API; по умолчанию https://api.telegram.org.
		// Подменяется в тестах (mock-сервер) — в конфиге это локальный
		// адрес не нужен, но поле оставляем: зеркало API допустимо.
		BaseURL string `yaml:"base_url"`
	} `yaml:"telegram"`

	// Notify — триггеры уведомлений (этап 8, ТЗ §3.2).
	Notify struct {
		// Сколько сообщений доставить за один `pb notify send`
		// (cron-точка, ТЗ §3.4). 0 — разумный дефолт 100.
		FlushLimit int `yaml:"flush_limit"`
		// Каталог с данными для проверки свободного места (ТЗ §3.2);
		// пусто — проверка отключена.
		DiskPath string `yaml:"disk_path"`
		// Свободного места меньше этого процента — алерт ДО критической
		// точки (ТЗ §3.2), а не после. Должно быть в (0, 100).
		DiskCriticalPct float64 `yaml:"disk_critical_pct"`
		// Минимальный интервал между повторными алертами о диске, мин
		// (состояние диска не меняется за минуты — повторный алерт
		// чаще — шум). >= 0; 0 — алерт при каждом прогоне.
		DiskRealertMinutes int `yaml:"disk_realert_minutes"`
	} `yaml:"notify"`

	// Schedule — адаптивное расписание сканирования (этап 11, ТЗ §10).
	// ТЗ §0.1: все пороги/коэффициенты — в конфиге, не в коде.
	Schedule struct {
		// Доля бюджета на исследование (ε, ТЗ §10.3): слот с нулевым
		// выходом не отключается — ему всегда достаётся ε доли веса.
		ExplorationFraction float64 `yaml:"exploration_fraction"`
		// Окно скользящего среднего выхода, дней (ТЗ §10.3).
		MAWindowDays int `yaml:"ma_window_days"`
		// Минимум накопленных полных сканов по источнику, ниже которого
		// расписание работает на консервативных равных весах и помечается
		// warming_up (ТЗ §10.5: первые недели оценки выхода ненадёжны,
		// выдавать раннюю адаптацию за «настроенную по статистике»
		// запрещено).
		MinObsForTuning int `yaml:"min_obs_for_tuning"`
		// Базовая длительность кулдауна, мин (ТЗ §10.4): капча/429 →
		// немедленно в cooldown на base * multiplier^(strikes-1).
		BackoffBaseMinutes int `yaml:"backoff_base_minutes"`
		// Множитель экспоненциального отката (>= 1).
		BackoffMultiplier float64 `yaml:"backoff_multiplier"`
		// Потолок кулдауна, часов.
		BackoffMaxHours int `yaml:"backoff_max_hours"`
		// Коэффициент ПОВЫШЕНИЯ rate_factor при каждом полном скане
		// (постепенное восстановление, ТЗ §10.4; > 1).
		RecoveryStep float64 `yaml:"recovery_step"`
		// Нижний предел rate_factor (0,1]: после серии капчей источник
		// сканирует не быстрее этой доли max_requests_per_hour.
		MinRateFactor float64 `yaml:"min_rate_factor"`
		// Часовой пояс страны объявления (IANA), по странам (ТЗ §10.2:
		// «часовой пояс целевой страны, не сервера»). Обязателен для
		// каждой страны из dashboard.countries.
		CountryTimezones map[string]string `yaml:"country_timezones"`
	} `yaml:"schedule"`

	// Translate — асинхронный переводчик описаний (этап 10, ТЗ §11).
	// Переводы ru/en хранятся в object_translations, чтение в UI — из БД,
	// без обращения к LLM. Идемпотентность — по sha256(description_original).
	// Асинхронность: перевод — отдельная cron-точка `pb translate run`,
	// дедупликация/оценка/уведомления на неё не ждут.
	Translate struct {
		// API-ключ LLM (OpenAI-совместимый API). Пусто — переводчик не
		// настроен: `pb translate run` возвращает явную ошибку, переводы
		// не выполняются (ТЗ §0.4 — честная ошибка, не заглушка). Реальный
		// ключ не коммитится (config.yaml в .gitignore).
		APIKey string `yaml:"api_key"`
		// Базовый URL OpenAI-совместимого API; по умолчанию
		// https://api.openai.com/v1. Подменяется в тестах (mock-сервер).
		BaseURL string `yaml:"base_url"`
		// Название модели. ТЗ модель не предписывает — выбирает оператор
		// (ТЗ §0.1: то, что должно быть значением конфига, не хардкодится).
		Model string `yaml:"model"`
		// Таймаут запроса к LLM, с.
		TimeoutSec int `yaml:"timeout_sec"`
		// Описание длиннее этого числа символов не отправляется в LLM
		// (стоимость в токенах); перевод остаётся NULL, в UI — «перевод
		// недоступен» (ТЗ §11). Допущение исполнителя: 4000 — типичная
		// длина описания объявления с запасом.
		MaxChars int `yaml:"max_chars"`
	} `yaml:"translate"`
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
	// Delist (ТЗ §8.2).
	if c.Delist.MinConsecutiveMisses < 2 {
		return nil, fmt.Errorf("config: delist.min_consecutive_misses должен быть >= 2, задано %d (ТЗ §8.2: минимум 2)", c.Delist.MinConsecutiveMisses)
	}
	if c.Delist.MaxDelistedSharePct <= 0 || c.Delist.MaxDelistedSharePct > 100 {
		return nil, fmt.Errorf("config: delist.max_delisted_share_pct должен быть в (0, 100], задано %v (ТЗ §8.2)", c.Delist.MaxDelistedSharePct)
	}
	if c.Delist.URLCheckTimeoutSec < 1 {
		return nil, fmt.Errorf("config: delist.url_check_timeout_sec должен быть >= 1, задано %d (ТЗ §8.2)", c.Delist.URLCheckTimeoutSec)
	}
	// Liquidity (ТЗ §9).
	if c.Liquidity.MinEvents < 10 {
		return nil, fmt.Errorf("config: liquidity.min_events должен быть >= 10, задано %d (ТЗ §9.3: прогноз только при достаточном числе событий)", c.Liquidity.MinEvents)
	}
	if c.Liquidity.MaxCalibDev <= 0 || c.Liquidity.MaxCalibDev > 1 {
		return nil, fmt.Errorf("config: liquidity.max_calib_dev должен быть в (0, 1], задано %v (ТЗ §9.4)", c.Liquidity.MaxCalibDev)
	}
	if c.Liquidity.HorizonDays < 7 {
		return nil, fmt.Errorf("config: liquidity.horizon_days должен быть >= 7 (недельная дискретизация), задано %d (ТЗ §9.2)", c.Liquidity.HorizonDays)
	}
	if c.Liquidity.HoldoutRatio <= 0 || c.Liquidity.HoldoutRatio >= 0.5 {
		return nil, fmt.Errorf("config: liquidity.holdout_ratio должен быть в (0, 0.5), задано %v (ТЗ §9.4)", c.Liquidity.HoldoutRatio)
	}
	// Schedule (ТЗ §10).
	if c.Schedule.ExplorationFraction <= 0 || c.Schedule.ExplorationFraction > 0.5 {
		return nil, fmt.Errorf("config: schedule.exploration_fraction должен быть в (0, 0.5], задано %v (ТЗ §10.3)", c.Schedule.ExplorationFraction)
	}
	if c.Schedule.MAWindowDays < 1 {
		return nil, fmt.Errorf("config: schedule.ma_window_days должен быть >= 1, задано %d (ТЗ §10.3)", c.Schedule.MAWindowDays)
	}
	if c.Schedule.MinObsForTuning < 1 {
		return nil, fmt.Errorf("config: schedule.min_obs_for_tuning должен быть >= 1, задано %d (ТЗ §10.5)", c.Schedule.MinObsForTuning)
	}
	if c.Schedule.BackoffBaseMinutes < 1 {
		return nil, fmt.Errorf("config: schedule.backoff_base_minutes должен быть >= 1, задано %d (ТЗ §10.4)", c.Schedule.BackoffBaseMinutes)
	}
	if c.Schedule.BackoffMultiplier < 1 {
		return nil, fmt.Errorf("config: schedule.backoff_multiplier должен быть >= 1, задано %v (ТЗ §10.4)", c.Schedule.BackoffMultiplier)
	}
	if c.Schedule.BackoffMaxHours < 1 {
		return nil, fmt.Errorf("config: schedule.backoff_max_hours должен быть >= 1, задано %d (ТЗ §10.4)", c.Schedule.BackoffMaxHours)
	}
	if time.Duration(c.Schedule.BackoffBaseMinutes)*time.Minute > time.Duration(c.Schedule.BackoffMaxHours)*time.Hour {
		return nil, fmt.Errorf("config: schedule.backoff_base_minutes (%d) больше backoff_max_hours (%d ч) (ТЗ §10.4)", c.Schedule.BackoffBaseMinutes, c.Schedule.BackoffMaxHours)
	}
	if c.Schedule.RecoveryStep <= 1 {
		return nil, fmt.Errorf("config: schedule.recovery_step должен быть > 1, задано %v (ТЗ §10.4: восстановление постепенное)", c.Schedule.RecoveryStep)
	}
	if c.Schedule.MinRateFactor <= 0 || c.Schedule.MinRateFactor >= 1 {
		return nil, fmt.Errorf("config: schedule.min_rate_factor должен быть в (0, 1), задано %v (ТЗ §10.4)", c.Schedule.MinRateFactor)
	}
	for _, country := range c.Dashboard.Countries {
		tz := c.Schedule.CountryTimezones[country]
		if tz == "" {
			return nil, fmt.Errorf("config: schedule.country_timezones не задан для %s (ТЗ §10.2: часовой пояс страны объявления)", country)
		}
		if _, err := time.LoadLocation(tz); err != nil {
			return nil, fmt.Errorf("config: schedule.country_timezones.%s: неизвестный IANA-пояс %q: %w", country, tz, err)
		}
	}
	// Telegram (этап 8, ТЗ §2).
	if c.Telegram.BaseURL == "" {
		c.Telegram.BaseURL = "https://api.telegram.org"
	}
	if u, err := url.Parse(c.Telegram.BaseURL); err != nil ||
		(u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("config: telegram.base_url должен быть http(s) URL, задано %q", c.Telegram.BaseURL)
	}
	if c.Telegram.Enabled {
		if c.Telegram.Token == "" || c.Telegram.ChatID == "" {
			return nil, fmt.Errorf("config: telegram.enabled=true, но не заданы telegram.token / telegram.chat_id (токен — у @BotFather)")
		}
	}
	// Notify (этап 8, ТЗ §3.2).
	if c.Notify.FlushLimit <= 0 {
		c.Notify.FlushLimit = 100
	}
	if c.Notify.DiskCriticalPct <= 0 || c.Notify.DiskCriticalPct >= 100 {
		return nil, fmt.Errorf("config: notify.disk_critical_pct должен быть в (0, 100), задано %v (ТЗ §3.2)", c.Notify.DiskCriticalPct)
	}
	if c.Notify.DiskRealertMinutes < 0 {
		return nil, fmt.Errorf("config: notify.disk_realert_minutes должен быть >= 0, задано %d", c.Notify.DiskRealertMinutes)
	}
	// Translate (этап 10, ТЗ §11). Блок опциональный: без ключа переводчик
	// не настроен — явная ошибка не на загрузке, а при `pb translate run`
	// (ТЗ §0.4); нули — дефолты (паттерн notify.flush_limit).
	if c.Translate.BaseURL == "" {
		c.Translate.BaseURL = "https://api.openai.com/v1"
	}
	if u, err := url.Parse(c.Translate.BaseURL); err != nil ||
		(u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("config: translate.base_url должен быть http(s) URL, задано %q", c.Translate.BaseURL)
	}
	if c.Translate.TimeoutSec <= 0 {
		c.Translate.TimeoutSec = 60
	}
	if c.Translate.MaxChars <= 0 {
		c.Translate.MaxChars = 4000
	}
	if c.Translate.MaxChars < 100 {
		return nil, fmt.Errorf("config: translate.max_chars должен быть >= 100, задано %d (лимит длины описания для LLM, ТЗ §11)", c.Translate.MaxChars)
	}
	// Ключ без имени модели — конфиг, который не может работать;
	// фиксируем на загрузке, а не в середине прогона.
	if c.Translate.APIKey != "" && c.Translate.Model == "" {
		return nil, fmt.Errorf("config: translate.model не задан, но translate.api_key задан (ТЗ §11)")
	}
	return &c, nil
}

// ScanPageDelay — пауза между запросами страниц (scan.page_delay_ms).
func (c *Config) ScanPageDelay() time.Duration {
	return time.Duration(c.Scan.PageDelayMS) * time.Millisecond
}
