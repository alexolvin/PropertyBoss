// Package scan — сканирование источников: scan_runs → raw_listings →
// objects/price_history (ТЗ §8).
//
// Каркас не знает о конкретных сайтах: коннектор (Connector) возвращает
// нормализованные записи, а Runner записывает их в БД с честной оценкой
// полноты (ТЗ §8.2.1: complete — только непустая выдача без ошибки/капчи;
// неполный скан не участвует в вычислении исчезновений).
package scan

import "time"

// Listing — одна нормализованная запись из источника на момент скана.
// Нормализует коннектор: raw-поля сайта → эти поля + ключи из
// attribute_registry (ТЗ §6). Каркас не трактует содержимое.
type Listing struct {
	ExternalID   string // идентификатор объявления на источнике
	URL          string // прямой URL объявления
	PriceMinor   *int64 // nil — цены на источнике нет
	Currency     *string // ISO 4217; обязательна, если PriceMinor != nil
	AreaSqM      *string // точная строка (NUMERIC(10,2)), nil — нет
	Rooms        *int
	PropertyType *string // тип объекта по таксономии источника (нормализован коннектором)
	Lat          *float64 // координаты не деньги — float допустим (ТЗ §5 про деньги)
	Lng          *float64
	Address      *string
	PostedAt     *time.Time // дата публикации по данным источника
	// Attributes — значения по ключам из attribute_registry страны;
	// ключи, отсутствующие в реестре, Runner переносит в
	// objects.attributes_unmapped (ТЗ §6: не отбрасывать молча).
	Attributes       map[string]any
	Description      *string
	LanguageOriginal *string // CHAR(2), напр. "cs"
}
