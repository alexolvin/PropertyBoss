// Package config — загрузка конфигурации из YAML.
//
// ТЗ §0.1: пороги и коэффициенты живут в конфиге с указанием источника
// значения, а не числом в коде.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// telegramChatIDRe — грамматика telegram.chat_id: числовой id или
// @username, опционально с топиком форума «/N» (разбор — notify.
// chatIDAndThread; Bot API принимает топик только как
// message_thread_id, не как часть chat_id).
var telegramChatIDRe = regexp.MustCompile(`^(?:-?\d+|@[A-Za-z0-9_]+)(?:/\d+)?$`)

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
		// UI — каталог собранного фронтенда (npm run build → web/dist);
		// pb serve раздаёт его тем же портом, что и API. Пусто — только API.
		UI string `yaml:"ui"`
		// WebSrc — каталог исходников фронтенда для чека web (TZ UI v5 U04):
		// dist старше любого файла из него — раздача раздвинута от исходников.
		// Пусто — web/src (заполняется по умолчанию при заданном UI).
		WebSrc string `yaml:"web_src"`
		// WebGraceSecs — допуск «насколько dist может быть старше src», сек.
		// Порог чека web из конфига (C23): 0 — строгое равенство по mtime.
		WebGraceSecs int `yaml:"web_grace_seconds"`
		// Целевые рынки этапа 1 (источник: ТЗ §1)
		Countries        []string          `yaml:"countries"`
		MarketCurrencies map[string]string `yaml:"market_currencies"`
		// Типы сделок (источник: ТЗ §7.2 — «продажа / аренда»)
		DealTypes []string `yaml:"deal_types"`
		// Map — подложка и пороги карты (TZ UI v5 U31, U30 Р4, U40).
		Map MapConf `yaml:"map"`
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

	// Source — общий ограничитель частоты запросов к источнику (D20, C27):
	// один минимальный интервал на ВСЕ исходящие пути — сбор, обогащение,
	// контроль, URL-чеки делистинга, верификацию. Кулдауэн по 429
	// (ТЗ §10.4) остаётся задним ограничителем.
	Source struct {
		// Минимальный интервал между любыми исходящими запросами к
		// источнику, мс; 0 — дефолт 2000 (демонстрированный темп
		// волны 09.08: 0 429 при ~2 с/запрос). Источник значения:
		// оператор (ТЗ §0.1, §12).
		RequestIntervalMS int `yaml:"request_interval_ms"`
		// User-Agent проб и сканов (ТЗ-SRC-CZ-1 A6): одна строка на проект,
		// та же, что у bazos-reality. Пусто → дефолт коннектора bazos.
		UserAgent string `yaml:"user_agent"`
	} `yaml:"source"`

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
		// Порог покрытия ОБЯЗАТЕЛЬНОГО набора признаков модели (цена,
		// площадь, зона, тип недвижимости), (0,1]: покрытие ниже порога —
		// модель не публикуется (ТЗ v4-2 §4.10.1; прежнее значение — порог
		// пропусков по всем атрибутам, ТЗ §7.3). Допущение: 0.5.
		MaxMissingRate float64 `yaml:"max_missing_rate"`
		// Обработка необязательного признака с высокой долей пропусков
		// (ТЗ v4-2 §4.10.2): "level" — признак остаётся в модели, пропуск
		// кодируется явным уровнем «не указано» (onehot — дополнительная
		// колонка, числовой признак — индикатор); рекомендовано ТЗ: пропуск
		// несёт сведения. "exclude" — признак исключается из модели, если
		// доля пропусков выше optional_exclude_rate. Выбор фиксируется в
		// отчёте (документ моделей). Допущение: level.
		OptionalMissing string `yaml:"optional_missing"`
		// (Только режим exclude.) Доля пропусков, выше которой признак
		// исключается из модели, (0,1]. Допущение исполнителя: 0.8.
		OptionalExcludeRate float64 `yaml:"optional_exclude_rate"`
		// Порог уверенности атрибута: значение с уверенностью ниже
		// порога не участвует в оценке коэффициентов (ТЗ v4 §4.3,
		// правило раздела 6 основного ТЗ). Уверенность — из
		// attribute_values; без строки provenance значение считается
		// пригодным (источник — структурированное поле/правило).
		// Допущение исполнителя: 0.5.
		MinConfidence float64 `yaml:"min_confidence"`
		// Число складок k-fold кросс-валидации для подбора λ (ТЗ §7.2:
		// «λ подбирается k-fold кросс-валидацией, а не назначается»).
		KFold int `yaml:"kfold"`
		// Кандидаты λ для гребневой регуляризации, по возрастанию.
		// Допущение исполнителя: стандартная логарифмическая сетка.
		LambdaGrid []float64 `yaml:"lambda_grid"`
		// C21: максимум допустимого числа обусловленности матрицы
		// подгонки (XᵀX + λP): подгонка с числом обусловленности выше
		// порога или рангом ниже числа параметров отвергается
		// (degenerate_matrix) — gonum Solve решает вырожденную систему
		// без ошибки, проверка отсутствия ошибки недостаточна.
		// Допущение исполнителя: 1e12.
		MaxMatCond float64 `yaml:"max_mat_cond"`
		// C25: максимум допустимой доли not_modeled (объекты без
		// уровня геокода — структурно не моделируемы). Критерий —
		// не «уровень у 100%» (недостижим), а последствие: объект с
		// флагом не показывает публикуемых оценок; рост доли выше
		// порога — дефект и разбор. Порог — оператор (регламент §12):
		// 2%. 0 → 0.02.
		MaxNotModeledShare float64 `yaml:"max_not_modeled_share"`
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
		// Controls — сколько контрольных объявлений (заведомо живых по
		// последнему успешному детальному fetch'у) проверить в начале
		// прогона enrich, прежде чем применять делистинг по ПРЯМОМУ
		// свидетельству (301/404/410 при загрузке страницы объявления).
		// Прямое свидетельство — другой класс сигнала, чем вывод по
		// отсутствию (delist-пасс), и max_delisted_share_pct на него не
		// распространяется; его защита — контрольные: живы → 301
		// подлинные; тоже упали → отказ сбора, прогон остановлен.
		// 0 — контрольных нет, делистинг enrich-путём идёт под
		// max_delisted_share_pct (консервативный fallback).
		Controls int `yaml:"controls"`
		// Минимальный знаменатель раунда для доли аномалии (D18).
		// delisted и доля считаются от одного множества — объектов,
		// верифицированных в раунде (enrich) либо активных объектов
		// источника (delist-пасс); если знаменатель меньше этого
		// значения, доля не вычисляется вовсе и аномалий-гвард не
		// срабатывает — на малом раунде большая доля не является
		// сигналом сбоя источника, «0 из 1 (100.0%)» невозможно.
		// Проективное +1 гварда остаётся внутренним и в пейлоад
		// уведомления не попадает никогда.
		MinObjectsForShare int `yaml:"min_objects_for_share"`
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

	// Detail — детальный проход по объявлениям (ТЗ v4 §4): разбор страницы
	// объявления до полного набора структурированных полей (площадь, этаж,
	// материал, энергокласс, фото, продавец). Отдельный от category-скана
	// проход `pb enrich`: category-скан остаётся быстрым (новые/delisted/
	// цена), детальный проход идёт по новым и изменённым объявлениям.
	Detail struct {
		// Вежливость: пауза между запросами страниц объявлений, мс.
		PageDelayMS int `yaml:"page_delay_ms"`
		// Таймаут одного запроса страницы объявления, с.
		TimeoutSec int `yaml:"timeout_sec"`
		// Верхний предел фотографий на объявление; лишние не скачиваются.
		// Допущение исполнителя: 30.
		MaxPhotos int `yaml:"max_photos"`
		// Уверенность правил извлечения из текста (ТЗ v4 §4.2: «уверенность
		// из конфига правил»). Структурированный блок источника —
		// source_field, уверенность всегда 1.0 (значок площадки, не правило).
		RuleConfidenceTitle       float64 `yaml:"rule_confidence_title"`
		RuleConfidenceDescription float64 `yaml:"rule_confidence_description"`
	} `yaml:"detail"`

	// Extract — LLM-извлечение атрибутов из описаний (ТЗ v4 §4.3).
	// Используется ТОЛЬКО для атрибутов, не покрытых source_field/правилами;
	// каждое значение программно валидируется по реестру (перечислимость,
	// числовые диапазоны), догадки запрещены. Ключ пуст — извлечение не
	// настроено: `pb extract run` возвращает явную ошибку (ТЗ §0.4).
	Extract struct {
		APIKey string `yaml:"api_key"`
		// Базовый URL OpenAI-совместимого API; по умолчанию
		// https://api.openai.com/v1.
		BaseURL string `yaml:"base_url"`
		// Название модели — выбирает оператор (ТЗ §0.1).
		Model string `yaml:"model"`
		// Таймаут запроса к LLM, с.
		TimeoutSec int `yaml:"timeout_sec"`
		// Потолок уверенности LLM-значений (ТЗ v4 §4.3): модель сообщает
		// уверенность, выше потолка не поднимается; значения ниже порога
		// реестра не входят в обучающую выборку. (0, 1].
		MaxConfidence float64 `yaml:"max_confidence"`
		// Версия списка атрибутов (состав реестра): часть ключа
		// идемпотентности наряду с sha256 описания и моделью.
		AttrListVersion string `yaml:"attr_list_version"`
		// Описание длиннее этого числа символов не отправляется в LLM.
		MaxChars int `yaml:"max_chars"`
		// Суммарный бюджет токенов (prompt + completion) за всё время:
		// расход — SUM(llm_extractions.tokens); при исчерпании прогон
		// останавливается, необработанные задачи остаются в очереди, а
		// не помечаются выполненными (ТЗ v4 §4.3). 0 — без лимита.
		BudgetTokens int `yaml:"budget_tokens"`
	} `yaml:"extract"`

	// Geocode — координаты и уровень их уверенности (ТЗ v4 §4.5).
	// Координаты площадки с уровнем ≥ refine_below — финал; ниже
	// (C10: PSČ-центроид) — Nominatim по улице из текста объявления.
	Geocode GeocodeConf `yaml:"geocode"`

	// Photos — скачивание и ресайз фотографий (ТЗ v4 §4.4): оригинальная
	// ссылка + два локальных размера (карта-карточка, список), WebP.
	// Юридически: только для личного использования (авторские права
	// площадок), redistribution запрещён.
	// Именованный тип (PhotosConf): его использует пакет internal/photos.
	Photos PhotosConf `yaml:"photos"`

	// SellerRules — правила определения типа продавца (ТЗ v4 §4.7):
	// структурированное поле → правило по признакам объявления → LLM.
	// Перечни маркеров — в конфиге, не в коде (ТЗ v2 §0.10).
	SellerRules SellerRulesConf `yaml:"seller_rules"`

	// Rules — параметры шаблонного слоя (ТЗ v4 §4.2, гибридное извлечение):
	// пороги авто-промоушна mined-шаблонов. Арсенал — данные в БД
	// (attribute_templates); пороги — в конфиге, не в коде (ТЗ §0.10).
	Rules RulesConf `yaml:"rules"`

	// Backup — резервное копирование и тест восстановления (ТЗ-R1 R0.3, R0.4).
	Backup BackupConf `yaml:"backup"`
}

// BackupConf — параметры резервного копирования и верификации (ТЗ-R1 R0.3, R0.4).
type BackupConf struct {
	Dir        string `yaml:"dir"`         // локальный каталог дампов (~/backups/propertyboss)
	Remote     string `yaml:"remote"`      // admin@100.126.222.23:/srv/backup/propertyboss
	SSHKey     string `yaml:"ssh_key"`     // ~/.ssh/pb_backup
	KeepLocal  int    `yaml:"keep_local"`  // 14
	KeepRemote int    `yaml:"keep_remote"` // 30
	TestDB     string `yaml:"test_db"`     // pb_restore_test
}

// RulesConf — тип секции rules (именованный: её используют internal/extract
// и cmd/pb).
type RulesConf struct {
	// Повторов (объектов с согласованным значением) mined-шаблона до
	// авто-промоушна candidate → active.
	MinMinedOccurrences int `yaml:"min_mined_occurrences"`
	// Потолок уверенности mined-шаблонов (0,1]: как у llm_extraction
	// (ТЗ v4 §4.3) — извлечённая из LLM-успеха уверенность выше потолка не
	// поднимается.
	MinedConfidenceMax float64 `yaml:"mined_confidence_max"`
	// Сколько id объектов-примеров хранить в mined-шаблоне для ревизии
	// (pb rules list).
	ExampleObjects int `yaml:"example_objects"`
	// Авто-промоушн mined-шаблонов в конце прогона extract. Значение по
	// умолчанию (false, нулевое для bool) — держать промоушн до ревизии:
	// кандидат остаётся candidate и не применяется к корпусу, пока оператор
	// вручную не включит подходящий (pb rules set <id> --status active).
	// true — авто-промоушн при пороге повторений, нулевых конфликтах и
	// 100% совпадении с LLM на холдауте (C8: последние 2 подтверждения;
	// поэтому порог не может быть < 5).
	AutoPromote bool `yaml:"auto_promote"`
	// Доля объектов, хиты ПРОМОУТНУТЫХ шаблонов которых дополнительно
	// уходят в LLM-проверку (теневой контроль, C8); расхождение —
	// автоматический демоушн. Детерминированная выборка по id объекта.
	// Допущение: 2 (решение по C8).
	ShadowSamplePct int `yaml:"shadow_sample_pct"`
}

// SellerRulesConf — тип секции seller_rules (именованный: его использует
// internal/enrich).
type SellerRulesConf struct {
	// Подстроки в имени продавца (без учёта регистра) → agency.
	AgencyMarkers []string `yaml:"agency_markers"`
	// Явные заявления частного лица (имя/описание) → private.
	PrivateMarkers []string `yaml:"private_markers"`
	// Подстроки в имени → developer (застройщик).
	DeveloperMarkers []string `yaml:"developer_markers"`
	// Уверенность правила (0..1); не задано — 0.8 (0.9 для private:
	// это прямое заявление, а не маркер).
	ConfidenceAgency    float64 `yaml:"confidence_agency"`
	ConfidencePrivate   float64 `yaml:"confidence_private"`
	ConfidenceDeveloper float64 `yaml:"confidence_developer"`
}

// ZoomLevel — правило «zoom площадки → уровень уверенности координат»
// (ТЗ v4 §4.5; порог — из конфига, не в коде).
type ZoomLevel struct {
	ZoomMin    int    `yaml:"zoom_min"`
	Confidence string `yaml:"confidence"`
}

// TypeLevel — правило «type результата геокодера → уровень уверенности».
type TypeLevel struct {
	Types      []string `yaml:"types"`
	Confidence string   `yaml:"confidence"`
}

// MapConf — подложка и пороги карты (TZ UI v5 U31/U30/U40, не в коде).
type MapConf struct {
	StyleURL      string  `yaml:"style_url"`
	PointsMax     int     `yaml:"points_max"`
	MaxBBoxDeg    float64 `yaml:"max_bbox_deg"`
	MinZoomWide   int     `yaml:"min_zoom_wide"`
	DefaultLng    float64 `yaml:"default_lng"`
	DefaultLat    float64 `yaml:"default_lat"`
	DefaultZoom   float64 `yaml:"default_zoom"`
	ZoneColorMinN int     `yaml:"zone_color_min_n"`
	SimplifyDeg   float64 `yaml:"simplify_deg"`
}

// GeocodeConf — секция geocode: провайдер, лимит и правила уровней
// уверенности (ТЗ v4 §4.5).
type GeocodeConf struct {
	// Провайдер; реализован только nominatim (другие — явная ошибка).
	Provider string `yaml:"provider"`
	BaseURL  string `yaml:"base_url"`
	// User-Agent для геокодера (Nominatim требует валидный UA).
	UserAgent string `yaml:"user_agent"`
	// Лимит запросов в минуту (политика Nominatim: максимум 1/с).
	RatePerMin int `yaml:"rate_per_min"`
	TimeoutSec int `yaml:"timeout_sec"`
	// Использовать координаты площадки. true — по умолчанию.
	UseSourceCoords bool `yaml:"use_source_coords"`
	// RefineBelow — если уверенность площадки НИЖЕ этого уровня,
	// запрашивать геокодер по улице из текста (C10/C32: zoom 12 =
	// центроид PSČ, street только из адреса). Пусто → street.
	// Геокодер принимается только при результате ≥ этого уровня.
	RefineBelow string `yaml:"refine_below"`
	// zoom площадки → уровень уверенности: правила по порядку,
	// zoom >= zoom_min → confidence (первое попавшее).
	SourceZoomLevels []ZoomLevel `yaml:"source_zoom_levels"`
	// type результата Nominatim → уровень уверенности; неизвестный
	// тип — NULL (догадка запрещена).
	NominatimTypeLevels []TypeLevel `yaml:"nominatim_type_levels"`
}

// PhotosConf — секция photos: размеры, качество и вежливость загрузки
// (ТЗ v4 §4.4).
type PhotosConf struct {
	// Каталог локальных копий относительно каталога проекта.
	Dir string `yaml:"dir"`
	// Ширина локальных копий, px: карта-карточка (больше), список.
	CardWidth int `yaml:"card_width"`
	ListWidth int `yaml:"list_width"`
	// Качество WebP, 1..100. Допущение исполнителя: 82.
	Quality int `yaml:"quality"`
	// Максимальный вес одной локальной копии, байт (ТЗ v4 §4.4:
	// лимит веса — из конфига).
	MaxBytes int `yaml:"max_bytes"`
	// Сколько первых фотографий объявления скачивается локально
	// (в обоих размерах); остальные — только оригинальные ссылки
	// (галерея объекта подгружает их лениво с площадки). Допущение:
	// 4 — обложка, список, карта-карточка и запас для галереи;
	// полный локальный архив всех фото всех объектов — десятки ГБ.
	MaxLocal int `yaml:"max_local"`
	// Параллельных загрузчиков (вежливость: для бэкфила 2–3, для
	// штатного прогона 1).
	Workers    int `yaml:"workers"`
	TimeoutSec int `yaml:"timeout_sec"`
	// Минимальный интервал между запросами файлов, мс (вежливость к
	// серверу изображений площадки).
	PageDelayMS int `yaml:"page_delay_ms"`
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
	if err := applySecrets(&c, path); err != nil {
		return nil, err
	}
	if c.Database.DSN == "" {
		return nil, fmt.Errorf("config: database.dsn не задан (секрет — в secrets.yaml рядом с конфигом)")
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
	// Чек web (TZ UI v5 U04) — пороговые значения из конфига (C23):
	// каталог исходников по умолчанию при включённой раздаче, допуск
	// по mtime — неотрицательный.
	if c.Dashboard.UI != "" && c.Dashboard.WebSrc == "" {
		c.Dashboard.WebSrc = "web/src"
	}
	if c.Dashboard.WebGraceSecs < 0 {
		return nil, fmt.Errorf("config: dashboard.web_grace_seconds должен быть >= 0, задано %d", c.Dashboard.WebGraceSecs)
	}
	// Карта (TZ UI v5 U31/U30/U40): нули — дефолты с источником в комментарии yaml.
	m := &c.Dashboard.Map
	if m.PointsMax <= 0 {
		m.PointsMax = 500 // Р4
	}
	if m.MaxBBoxDeg <= 0 {
		m.MaxBBoxDeg = 20 // отсечка U30, Волна 3
	}
	if m.MinZoomWide <= 0 {
		m.MinZoomWide = 10
	}
	if m.DefaultZoom <= 0 {
		m.DefaultZoom = 7
	}
	if m.ZoneColorMinN <= 0 {
		m.ZoneColorMinN = 30 // тот же ориентир, что valuation.min_obs_per_zone
	}
	if m.SimplifyDeg <= 0 {
		m.SimplifyDeg = 0.001
	}
	if m.StyleURL != "" {
		if u, err := url.Parse(m.StyleURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("config: dashboard.map.style_url должен быть http(s) URL, задано %q", m.StyleURL)
		}
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
	if c.Source.RequestIntervalMS < 0 {
		return nil, fmt.Errorf("config: source.request_interval_ms должен быть >= 0, задано %d (D20)", c.Source.RequestIntervalMS)
	}
	if c.Source.RequestIntervalMS == 0 {
		// Демонстрированно безопасный темп волны 09.08: 0 429 при
		// ~2 с/запрос (55-верификация @2000мс: 0/55; 09.06 enrich
		// @~1.9 с/объект: 0/5974); при 1000мс — 2/319 (D20).
		c.Source.RequestIntervalMS = 2000
	}
	if c.Source.UserAgent == "" {
		// A6: та же строка, что internal/connectors/bazos (проверено 2026-08-27).
		c.Source.UserAgent = "PropertyBoss/0.3 (real-estate price monitoring)"
	}
	if c.Valuation.MinObsPerParam < 1 {
		return nil, fmt.Errorf("config: valuation.min_obs_per_param должен быть >= 1, задано %d (ТЗ §7.3)", c.Valuation.MinObsPerParam)
	}
	if c.Valuation.MinObsPerZone < 1 {
		return nil, fmt.Errorf("config: valuation.min_obs_per_zone должен быть >= 1, задано %d (ТЗ §7.3)", c.Valuation.MinObsPerZone)
	}
	if c.Valuation.MaxMissingRate <= 0 || c.Valuation.MaxMissingRate > 1 {
		return nil, fmt.Errorf("config: valuation.max_missing_rate должен быть в (0, 1], задано %v (ТЗ v4-2 §4.10.1)", c.Valuation.MaxMissingRate)
	}
	if c.Valuation.OptionalMissing == "" {
		c.Valuation.OptionalMissing = "level" // рекомендация ТЗ v4-2 §4.10.3
	}
	if c.Valuation.OptionalMissing != "level" && c.Valuation.OptionalMissing != "exclude" {
		return nil, fmt.Errorf("config: valuation.optional_missing: level|exclude, задано %q (ТЗ v4-2 §4.10.2)", c.Valuation.OptionalMissing)
	}
	if c.Valuation.OptionalExcludeRate == 0 {
		c.Valuation.OptionalExcludeRate = 0.8 // допущение исполнителя
	}
	if c.Valuation.OptionalExcludeRate <= 0 || c.Valuation.OptionalExcludeRate > 1 {
		return nil, fmt.Errorf("config: valuation.optional_exclude_rate должен быть в (0, 1], задано %v (ТЗ v4-2 §4.10.2)", c.Valuation.OptionalExcludeRate)
	}
	if c.Valuation.MinConfidence == 0 {
		c.Valuation.MinConfidence = 0.5 // допущение исполнителя (ТЗ v4 §4.3)
	}
	if c.Valuation.MinConfidence < 0 || c.Valuation.MinConfidence > 1 {
		return nil, fmt.Errorf("config: valuation.min_confidence должен быть в [0, 1], задано %v (ТЗ v4 §4.3)", c.Valuation.MinConfidence)
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
	if c.Valuation.MaxMatCond == 0 {
		c.Valuation.MaxMatCond = 1e12 // допущение исполнителя (C21)
	}
	if c.Valuation.MaxMatCond < 0 {
		return nil, fmt.Errorf("config: valuation.max_mat_cond должен быть >= 0, задано %v", c.Valuation.MaxMatCond)
	}
	if c.Valuation.MaxNotModeledShare == 0 {
		c.Valuation.MaxNotModeledShare = 0.02 // оператор (C25, регламент §12)
	}
	if c.Valuation.MaxNotModeledShare < 0 || c.Valuation.MaxNotModeledShare > 1 {
		return nil, fmt.Errorf("config: valuation.max_not_modeled_share должен быть в [0, 1], задано %v (C25)", c.Valuation.MaxNotModeledShare)
	}
	// Delist (ТЗ §8.2).
	if c.Delist.MinConsecutiveMisses < 2 {
		return nil, fmt.Errorf("config: delist.min_consecutive_misses должен быть >= 2, задано %d (ТЗ §8.2: минимум 2)", c.Delist.MinConsecutiveMisses)
	}
	if c.Delist.MaxDelistedSharePct <= 0 || c.Delist.MaxDelistedSharePct > 100 {
		return nil, fmt.Errorf("config: delist.max_delisted_share_pct должен быть в (0, 100], задано %v (ТЗ §8.2)", c.Delist.MaxDelistedSharePct)
	}
	if c.Delist.MinObjectsForShare < 1 {
		return nil, fmt.Errorf("config: delist.min_objects_for_share должен быть >= 1, задано %d (D18)", c.Delist.MinObjectsForShare)
	}
	if c.Delist.URLCheckTimeoutSec < 1 {
		return nil, fmt.Errorf("config: delist.url_check_timeout_sec должен быть >= 1, задано %d (ТЗ §8.2)", c.Delist.URLCheckTimeoutSec)
	}
	// controls: 0 — валидное явное значение (fallback на
	// max_delisted_share_pct), авто-дефолта по нулю ставить нельзя.
	if c.Delist.Controls < 0 {
		return nil, fmt.Errorf("config: delist.controls должен быть >= 0, задано %d", c.Delist.Controls)
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
			return nil, fmt.Errorf("config: telegram.enabled=true, но не заданы telegram.token / telegram.chat_id (токен — у @BotFather, хранится в secrets.yaml рядом с конфигом)")
		}
		if !telegramChatIDRe.MatchString(c.Telegram.ChatID) {
			return nil, fmt.Errorf("config: telegram.chat_id %q: ожидается числовой id или @username, опционально с топиком «/N» (напр. -1001234567890/26)", c.Telegram.ChatID)
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
	// Detail — детальный проход по объявлениям (ТЗ v4 §4).
	if c.Detail.PageDelayMS < 0 {
		return nil, fmt.Errorf("config: detail.page_delay_ms должен быть >= 0, задано %d", c.Detail.PageDelayMS)
	}
	if c.Detail.PageDelayMS == 0 {
		c.Detail.PageDelayMS = 1000 // вежливый уровень по умолчанию (как scan)
	}
	if c.Detail.TimeoutSec <= 0 {
		c.Detail.TimeoutSec = 30
	}
	if c.Detail.MaxPhotos <= 0 {
		c.Detail.MaxPhotos = 30 // допущение: защита от разрастания диска
	}
	if c.Detail.RuleConfidenceTitle <= 0 || c.Detail.RuleConfidenceTitle > 1 {
		c.Detail.RuleConfidenceTitle = 0.9
	}
	if c.Detail.RuleConfidenceDescription <= 0 || c.Detail.RuleConfidenceDescription > 1 {
		c.Detail.RuleConfidenceDescription = 0.8
	}
	// Extract — LLM-извлечение атрибутов (ТЗ v4 §4.3). Блок опциональный,
	// как translate: без ключа — явная ошибка на `pb extract run`, а не на
	// загрузке конфига (ТЗ §0.4 — честная ошибка, не заглушка). Проверки
	// обязательных полей (max_confidence, attr_list_version) — только если
	// блок задан хоть частично.
	if c.Extract.BaseURL != "" || c.Extract.Model != "" || c.Extract.APIKey != "" ||
		c.Extract.MaxConfidence != 0 || c.Extract.AttrListVersion != "" ||
		c.Extract.TimeoutSec != 0 || c.Extract.MaxChars != 0 {
		if c.Extract.MaxConfidence <= 0 || c.Extract.MaxConfidence > 1 {
			return nil, fmt.Errorf("config: extract.max_confidence должен быть в (0, 1], задано %v (ТЗ v4 §4.3: потолок уверенности LLM)", c.Extract.MaxConfidence)
		}
		if c.Extract.AttrListVersion == "" {
			return nil, fmt.Errorf("config: extract.attr_list_version не задан (ТЗ v4 §4.3: версия списка атрибутов — часть ключа идемпотентности)")
		}
		if c.Extract.MaxChars > 0 && c.Extract.MaxChars < 100 {
			return nil, fmt.Errorf("config: extract.max_chars должен быть >= 100, задано %d", c.Extract.MaxChars)
		}
		if c.Extract.BudgetTokens < 0 {
			return nil, fmt.Errorf("config: extract.budget_tokens должен быть >= 0, задано %d (0 — без лимита)", c.Extract.BudgetTokens)
		}
		if c.Extract.APIKey != "" && c.Extract.Model == "" {
			return nil, fmt.Errorf("config: extract.model не задан, но extract.api_key задан (ТЗ v4 §4.3)")
		}
	}
	if c.Extract.BaseURL == "" {
		c.Extract.BaseURL = "https://api.openai.com/v1"
	}
	if u, err := url.Parse(c.Extract.BaseURL); err != nil ||
		(u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("config: extract.base_url должен быть http(s) URL, задано %q", c.Extract.BaseURL)
	}
	if c.Extract.TimeoutSec <= 0 {
		c.Extract.TimeoutSec = 60
	}
	if c.Extract.MaxChars <= 0 {
		c.Extract.MaxChars = 4000
	}
	// Geocode — геокодирование (ТЗ v4 §4.5). Блок опциональный, как extract:
	// без ключа — явная ошибка на `pb enrich`, а не на загрузке конфига.
	if c.Geocode.Provider != "" || c.Geocode.BaseURL != "" || c.Geocode.UserAgent != "" ||
		c.Geocode.RatePerMin != 0 || c.Geocode.TimeoutSec != 0 ||
		len(c.Geocode.SourceZoomLevels) > 0 || len(c.Geocode.NominatimTypeLevels) > 0 {
		if c.Geocode.Provider == "" {
			c.Geocode.Provider = "nominatim"
		}
		if c.Geocode.Provider != "nominatim" {
			return nil, fmt.Errorf("config: geocode.provider: реализован только nominatim, задано %q (другие провайдеры — отдельная работа)", c.Geocode.Provider)
		}
		if c.Geocode.BaseURL == "" {
			c.Geocode.BaseURL = "https://nominatim.openstreetmap.org"
		}
		if u, err := url.Parse(c.Geocode.BaseURL); err != nil ||
			(u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("config: geocode.base_url должен быть http(s) URL, задано %q", c.Geocode.BaseURL)
		}
		if c.Geocode.UserAgent == "" {
			// Nominatim требует валидный User-Agent с контактом (политика использования).
			c.Geocode.UserAgent = "PropertyBoss/1.0 (personal research project; data: OpenStreetMap contributors, ODbL 1.0)"
		}
		if c.Geocode.RatePerMin <= 0 {
			c.Geocode.RatePerMin = 50 // политика Nominatim: максимум 1 запрос/с
		}
		if c.Geocode.RatePerMin > 60 {
			return nil, fmt.Errorf("config: geocode.rate_per_min должен быть <= 60 (политика Nominatim: максимум 1 запрос/с), задано %d", c.Geocode.RatePerMin)
		}
		if c.Geocode.TimeoutSec <= 0 {
			c.Geocode.TimeoutSec = 30
		}
		if len(c.Geocode.SourceZoomLevels) == 0 {
			return nil, fmt.Errorf("config: geocode.source_zoom_levels не задан (ТЗ v4 §4.5: zoom площадки → уровень уверенности; без правил координаты площадки не использовать)")
		}
		for i, zl := range c.Geocode.SourceZoomLevels {
			if zl.ZoomMin < 1 || zl.ZoomMin > 22 {
				return nil, fmt.Errorf("config: geocode.source_zoom_levels[%d].zoom_min должен быть в 1..22, задано %d", i, zl.ZoomMin)
			}
			if !validGeoConfidence(zl.Confidence) {
				return nil, fmt.Errorf("config: geocode.source_zoom_levels[%d].confidence: %q, допустимы building|street|municipality|postcode|zone", i, zl.Confidence)
			}
		}
		if c.Geocode.RefineBelow == "" {
			c.Geocode.RefineBelow = "street" // C10/C32: PSČ-центроид не финал
		}
		if !validGeoConfidence(c.Geocode.RefineBelow) {
			return nil, fmt.Errorf("config: geocode.refine_below: %q, допустимы building|street|municipality|postcode|zone", c.Geocode.RefineBelow)
		}
		if len(c.Geocode.NominatimTypeLevels) == 0 {
			return nil, fmt.Errorf("config: geocode.nominatim_type_levels не задан (ТЗ v4 §4.5: тип геокодера → уровень уверенности)")
		}
		for i, tl := range c.Geocode.NominatimTypeLevels {
			if len(tl.Types) == 0 {
				return nil, fmt.Errorf("config: geocode.nominatim_type_levels[%d].types пуст", i)
			}
			if !validGeoConfidence(tl.Confidence) {
				return nil, fmt.Errorf("config: geocode.nominatim_type_levels[%d].confidence: %q, допустимы building|street|municipality|postcode|zone", i, tl.Confidence)
			}
		}
	}
	// Photos — скачивание и ресайз (ТЗ v4 §4.4). Блок опциональный, как geocode.
	if c.Photos.Dir != "" || c.Photos.CardWidth != 0 || c.Photos.ListWidth != 0 ||
		c.Photos.Quality != 0 || c.Photos.MaxBytes != 0 || c.Photos.MaxLocal != 0 ||
		c.Photos.Workers != 0 || c.Photos.TimeoutSec != 0 || c.Photos.PageDelayMS != 0 {
		if c.Photos.Dir == "" {
			c.Photos.Dir = "data/photos"
		}
		if c.Photos.CardWidth <= 0 {
			c.Photos.CardWidth = 640
		}
		if c.Photos.ListWidth <= 0 {
			c.Photos.ListWidth = 200
		}
		if c.Photos.ListWidth > c.Photos.CardWidth {
			return nil, fmt.Errorf("config: photos.list_width (%d) не может быть больше photos.card_width (%d)", c.Photos.ListWidth, c.Photos.CardWidth)
		}
		if c.Photos.Quality <= 0 || c.Photos.Quality > 100 {
			return nil, fmt.Errorf("config: photos.quality должен быть в 1..100, задано %d", c.Photos.Quality)
		}
		if c.Photos.MaxBytes <= 0 {
			return nil, fmt.Errorf("config: photos.max_bytes должен быть > 0, задано %d (ТЗ v4 §4.4: лимит веса — из конфига)", c.Photos.MaxBytes)
		}
		if c.Photos.MaxLocal <= 0 {
			c.Photos.MaxLocal = 4 // допущение: см. комментарий к полю
		}
		if c.Photos.Workers < 1 {
			c.Photos.Workers = 1
		}
		if c.Photos.Workers > 4 {
			return nil, fmt.Errorf("config: photos.workers должен быть <= 4 (вежливость к площадке), задано %d", c.Photos.Workers)
		}
		if c.Photos.TimeoutSec <= 0 {
			c.Photos.TimeoutSec = 60
		}
		if c.Photos.PageDelayMS < 0 {
			return nil, fmt.Errorf("config: photos.page_delay_ms должен быть >= 0, задано %d", c.Photos.PageDelayMS)
		}
		if c.Photos.PageDelayMS == 0 {
			c.Photos.PageDelayMS = 500
		}
	}
	// SellerRules — правила типа продавца (ТЗ v4 §4.7). Блок опциональный.
	if c.SellerRules.ConfidenceAgency != 0 || c.SellerRules.ConfidencePrivate != 0 ||
		c.SellerRules.ConfidenceDeveloper != 0 || len(c.SellerRules.AgencyMarkers) > 0 ||
		len(c.SellerRules.PrivateMarkers) > 0 || len(c.SellerRules.DeveloperMarkers) > 0 {
		if c.SellerRules.ConfidenceAgency <= 0 || c.SellerRules.ConfidenceAgency > 1 {
			c.SellerRules.ConfidenceAgency = 0.8
		}
		if c.SellerRules.ConfidencePrivate <= 0 || c.SellerRules.ConfidencePrivate > 1 {
			c.SellerRules.ConfidencePrivate = 0.9 // явные заявления («přímý majitel»)
		}
		if c.SellerRules.ConfidenceDeveloper <= 0 || c.SellerRules.ConfidenceDeveloper > 1 {
			c.SellerRules.ConfidenceDeveloper = 0.8
		}
	}
	// Rules — параметры шаблонного слоя (ТЗ v4 §4.2). Блок опциональный:
	// пороги в конфиге, не в коде (ТЗ §0.10).
	if c.Rules.MinMinedOccurrences != 0 || c.Rules.MinedConfidenceMax != 0 || c.Rules.ExampleObjects != 0 || c.Rules.ShadowSamplePct != 0 {
		if c.Rules.MinedConfidenceMax <= 0 || c.Rules.MinedConfidenceMax > 1 {
			return nil, fmt.Errorf("config: rules.mined_confidence_max должен быть в (0, 1], задано %v", c.Rules.MinedConfidenceMax)
		}
		if c.Rules.MinMinedOccurrences < 0 {
			return nil, fmt.Errorf("config: rules.min_mined_occurrences должен быть >= 0, задано %d", c.Rules.MinMinedOccurrences)
		}
		if c.Rules.ExampleObjects < 0 {
			return nil, fmt.Errorf("config: rules.example_objects должен быть >= 0, задано %d", c.Rules.ExampleObjects)
		}
		if c.Rules.ShadowSamplePct < 0 || c.Rules.ShadowSamplePct > 100 {
			return nil, fmt.Errorf("config: rules.shadow_sample_pct должен быть в [0, 100], задано %d", c.Rules.ShadowSamplePct)
		}
	}
	// C8: порог ≥5 — решение оператора (117 кандидатов, покрытие очереди
	// 67.4%); <5 исключено: при двух совпадениях холдаут-проверка
	// невозможна, а число повторений не защищает от систематической ошибки.
	if c.Rules.MinMinedOccurrences <= 0 {
		c.Rules.MinMinedOccurrences = 5
	}
	if c.Rules.MinedConfidenceMax <= 0 {
		c.Rules.MinedConfidenceMax = 0.90
	}
	if c.Rules.ExampleObjects <= 0 {
		c.Rules.ExampleObjects = 5
	}
	if c.Rules.ShadowSamplePct == 0 {
		c.Rules.ShadowSamplePct = 2 // решение по C8
	}
	if c.Rules.AutoPromote && c.Rules.MinMinedOccurrences < 5 {
		return nil, fmt.Errorf("config: rules.auto_promote=true требует min_mined_occurrences >= 5 (холдаут C8), задано %d", c.Rules.MinMinedOccurrences)
	}

	// Backup (ТЗ-R1 R0.3, R0.4).
	if c.Backup.Dir == "" {
		home, _ := os.UserHomeDir()
		c.Backup.Dir = filepath.Join(home, "backups", "propertyboss")
	} else if strings.HasPrefix(c.Backup.Dir, "~/") {
		home, _ := os.UserHomeDir()
		c.Backup.Dir = filepath.Join(home, c.Backup.Dir[2:])
	}
	if c.Backup.SSHKey == "" {
		home, _ := os.UserHomeDir()
		c.Backup.SSHKey = filepath.Join(home, ".ssh", "pb_backup")
	} else if strings.HasPrefix(c.Backup.SSHKey, "~/") {
		home, _ := os.UserHomeDir()
		c.Backup.SSHKey = filepath.Join(home, c.Backup.SSHKey[2:])
	}
	if c.Backup.KeepLocal <= 0 {
		c.Backup.KeepLocal = 14
	}
	if c.Backup.KeepRemote <= 0 {
		c.Backup.KeepRemote = 30
	}
	if c.Backup.TestDB == "" {
		c.Backup.TestDB = "pb_restore_test"
	}

	return &c, nil
}

// SecretsPath — путь к файлу секретов: secrets.yaml в том же каталоге,
// что и основной конфиг (для config/config.yaml — config/secrets.yaml).
// Конфиг из другого пути (--config / PB_CONFIG) — секреты ищутся рядом
// с ним, а не в config/.
func SecretsPath(path string) string {
	return filepath.Join(filepath.Dir(path), "secrets.yaml")
}

// secretsFile — секретные поля, вынесенные из общего конфига (решение
// оператора, 09.09): дублирует структуру секций Config, но содержит
// только секреты. Пустые поля secrets.yaml ничего не перезаписывают;
// если секрет задан и в config.yaml, и в secrets.yaml — побеждает
// secrets.yaml (источник секрета — один, общий конфиг чистим от них).
// Секреты, которых здесь нет (chat_id, base_url), секретами не
// считаются и остаются в общем конфиге.
type secretsFile struct {
	Database struct {
		DSN string `yaml:"dsn"`
	} `yaml:"database"`
	Telegram struct {
		Token string `yaml:"token"`
	} `yaml:"telegram"`
	Translate struct {
		APIKey string `yaml:"api_key"`
	} `yaml:"translate"`
	Extract struct {
		APIKey string `yaml:"api_key"`
	} `yaml:"extract"`
}

// applySecrets — подмешивает секреты из secrets.yaml (рядом с основным
// конфигом) поверх значений config.yaml. Файла нет — не ошибка: секреты
// могут оставаться в основном конфиге (обратная совместимость), а
// отсутствие значения в итоге зафиксирует валидация (database.dsn,
// telegram.token) с явным указанием файла секретов.
func applySecrets(c *Config, path string) error {
	sp := SecretsPath(path)
	data, err := os.ReadFile(sp)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("config: чтение секретов %s: %w", sp, err)
	}
	var s secretsFile
	if err := yaml.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("config: разбор секретов %s: %w", sp, err)
	}
	if s.Database.DSN != "" {
		c.Database.DSN = s.Database.DSN
	}
	if s.Telegram.Token != "" {
		c.Telegram.Token = s.Telegram.Token
	}
	if s.Translate.APIKey != "" {
		c.Translate.APIKey = s.Translate.APIKey
	}
	if s.Extract.APIKey != "" {
		c.Extract.APIKey = s.Extract.APIKey
	}
	return nil
}

// validGeoConfidence — допустимые уровни уверенности координат (ТЗ v4 §4.5).
func validGeoConfidence(s string) bool {
	switch s {
	case "building", "street", "municipality", "postcode", "zone":
		return true
	}
	return false
}

// ScanPageDelay — пауза между запросами страниц (scan.page_delay_ms).
func (c *Config) ScanPageDelay() time.Duration {
	return time.Duration(c.Scan.PageDelayMS) * time.Millisecond
}
