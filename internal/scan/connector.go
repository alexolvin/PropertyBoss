package scan

import "context"

// SearchConfig — снимок конфигурации поиска (search_configs) для одного скана.
// Загружается Runner-ом из БД; коннектор использует его как параметры
// запроса к источнику (страна, тип сделки, диапазоны, фильтры атрибутов).
type SearchConfig struct {
	ID            int64
	SourceID      string // источник, для которого задана конфигурация
	Country       string // CHAR(2)
	DealType      string // "sale" | "rent" (config: dashboard.deal_types)
	PropertyType  *string
	MinPriceMinor *int64
	MaxPriceMinor *int64
	MinAreaSqM    *string // NUMERIC как строка
	MaxAreaSqM    *string
	// FilterAttrs — фильтры по ключам attribute_registry (значения уже
	// валидированы при создании конфигурации, ТЗ §6).
	FilterAttrs map[string]any
	Currency    *string // рыночная валюта страны (currencies)
	Active      bool    // деактивную конфигурацию сканировать нельзя
}

// Issue — причина неполноты скана: часть данных получена, но скан нельзя
// считать полным (completeness='partial', ТЗ §8.2.1).
type Issue struct {
	Kind    FailureKind // captcha | http_429 | layout_change | network
	Message string      // человекочитаемая причина (в лог и scan-отчёт)
}

// Connector — интерфейс источника (simple; protected — этап 9).
//
// Требование ТЗ §13: до написания коннектора у источника должен быть
// зафиксирован sources.access_policy (официальный API / robots.txt / ToS,
// дата проверки). Если у источника есть API — коннектор обязан использовать
// API, а не парсить HTML.
//
// Контракт Scan:
//   - (listings, nil, nil)  — скан выполнен полностью (выдача может быть
//     пустой — Runner честно пометит partial, §8.2.1);
//   - (listings, issue, nil)— скан неполный: часть страниц/разделов не
//     загружена, причина — в issue; данные, что получены, записываются;
//   - (nil, nil, err)       — скан не выполнен (сеть, капча, 429);
//     Classify(err) даёт failure_kind.
type Connector interface {
	// SourceID — id источника в таблице sources (ключ реестра коннекторов).
	SourceID() string
	// Scan — один скан по конфигурации поиска.
	Scan(ctx context.Context, cfg SearchConfig) (listings []Listing, issue *Issue, err error)
}
