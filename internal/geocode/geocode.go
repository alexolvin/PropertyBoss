// geocode — координаты объектов и уровень уверенности (ТЗ v4 §4.5).
//
// Порядок:
//  1. Координаты площадки, если use_source_coords и зум дал уровень.
//     Уровень ≥ refine_below (по умолчанию street) — геокодер не зовём:
//     площадка зумит к участку, лимит не тратим.
//  2. Ниже порога (типичный случай C10: zoom 12 = центроид PSČ,
//     confidence=postcode) — Nominatim по улице из текста объявления.
//     Результат принимаем только при уровне ≥ refine_below (street+).
//     Иначе оставляем координаты площадки: городской центроид Nominatim
//     хуже PSČ, подменять его запрещено.
//  3. Санити: оба источника есть и расходятся >50 км — площадка
//     (продавец поставил точку; Nominatim на коротком запросе может
//     взять чужой город).
//  4. Нет площадки — геокодер по улице/городу, любой полученный уровень
//     (в том числе municipality). Нет ни того ни другого — (nil, nil).
//
// Провайдер и лимит — из конфигурации. Координаты не получены —
// не ошибка, объект честно без точек.
package geocode

import (
	"context"
	"math"

	"propertyboss/internal/config"
)

// sanityKilometers — допустимое расхождение «площадка ↔ геокодер».
const sanityKilometers = 50.0

// Point — координата с (опциональным) типом из геокодера.
type Point struct {
	Lat, Lng float64
	Type     string // type результата Nominatim ("" — координаты площадки)
}

// Result — итог определения координат объекта.
type Result struct {
	Lat, Lng   float64
	Confidence string // building|street|municipality|postcode|zone; "" — неизвестен
	Source     string // "source" (площадка) | "geocoder" (Nominatim)
}

// Geocoder — резолвер адресов (клиент Nominatim; в тестах — заглушка).
// (nil, nil) — адрес не резолвится.
type Geocoder interface {
	Resolve(ctx context.Context, q Query) (*Point, error)
}

// SourceConfidence — зум карты площадки → уровень уверенности: первое
// попавшееся правило (zoom >= zoom_min); правил нет — "" (неизвестно).
func SourceConfidence(zoom int, levels []config.ZoomLevel) string {
	for _, l := range levels {
		if zoom >= l.ZoomMin {
			return l.Confidence
		}
	}
	return ""
}

// TypeConfidence — type Nominatim → уровень уверенности; неизвестный
// тип — "" (NULL, догадка запрещена).
func TypeConfidence(typ string, levels []config.TypeLevel) string {
	for _, l := range levels {
		for _, t := range l.Types {
			if typ == t {
				return l.Confidence
			}
		}
	}
	return ""
}

// HaversineKM — расстояние между точками, км.
func HaversineKM(lat1, lng1, lat2, lng2 float64) float64 {
	const r = 6371.0
	dLat := rad(lat2 - lat1)
	dLng := rad(lng2 - lng1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(lat1))*math.Cos(rad(lat2))*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(a)))
}

func rad(deg float64) float64 { return deg * math.Pi / 180 }

func refineThreshold(cfg config.GeocodeConf) string {
	if cfg.RefineBelow != "" {
		return cfg.RefineBelow
	}
	return "street"
}

// Resolve — координаты + уверенность для объекта (ТЗ v4 §4.5, C10/C32).
//
//	src/zoom — карта площадки (nil — нет);
//	srcConf — уже известный уровень (бэкфилл по колонке); пусто — из zoom;
//	q — нормализованный адрес; gc — геокодер (nil — только шаг 1).
func Resolve(ctx context.Context, cfg config.GeocodeConf, src *Point, srcConf string, zoom int, q Query, gc Geocoder) (*Result, error) {
	useSrc := src != nil && cfg.UseSourceCoords
	if !useSrc {
		src = nil
	}

	var srcRes *Result
	if src != nil {
		conf := srcConf
		if conf == "" {
			conf = SourceConfidence(zoom, cfg.SourceZoomLevels)
		}
		srcRes = &Result{Lat: src.Lat, Lng: src.Lng, Confidence: conf, Source: "source"}
	}

	th := refineThreshold(cfg)
	// Площадка уже на пороге (zoom 16 → street) — геокодер не нужен.
	if srcRes != nil && Rank(srcRes.Confidence) >= Rank(th) {
		return srcRes, nil
	}

	// Уточнение существующих координат — только по улице. Город без улицы
	// даст municipality, это не лучше центроида PSČ.
	needStreet := srcRes != nil
	canGeocode := gc != nil && q.hasPlace() && (!needStreet || q.hasStreet())
	if !canGeocode {
		if srcRes != nil {
			return srcRes, nil
		}
		return nil, nil
	}

	p, err := gc.Resolve(ctx, q)
	if err != nil {
		return nil, err
	}
	if p == nil {
		if srcRes != nil {
			return srcRes, nil
		}
		return nil, nil
	}
	geoRes := &Result{
		Lat: p.Lat, Lng: p.Lng,
		Confidence: TypeConfidence(p.Type, cfg.NominatimTypeLevels),
		Source:     "geocoder",
	}

	if src != nil && HaversineKM(src.Lat, src.Lng, p.Lat, p.Lng) > sanityKilometers {
		return srcRes, nil
	}
	// Есть площадка ниже порога: геокодер принимаем только на пороге
	// (street/building). Иначе оставляем PSČ.
	if srcRes != nil && Rank(geoRes.Confidence) < Rank(th) {
		return srcRes, nil
	}
	return geoRes, nil
}
