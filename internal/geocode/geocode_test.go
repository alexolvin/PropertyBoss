// Тесты чистой логики geocode: уровни уверенности, расстояние,
// порядок Resolve (заглушка геокодера вместо Nominatim).
package geocode

import (
	"context"
	"testing"

	"propertyboss/internal/config"
)

var zoomLevels = []config.ZoomLevel{
	{ZoomMin: 16, Confidence: "street"},
	{ZoomMin: 13, Confidence: "municipality"},
	{ZoomMin: 1, Confidence: "postcode"},
}

var typeLevels = []config.TypeLevel{
	{Types: []string{"house", "building", "premises", "address"}, Confidence: "building"},
	{Types: []string{"road", "street", "footway", "pedestrian", "pathway", "residential", "living_street", "tertiary"}, Confidence: "street"},
	{Types: []string{"city", "town", "village", "suburb", "hamlet", "city_district", "borough"}, Confidence: "municipality"},
	{Types: []string{"postcode", "postal_code"}, Confidence: "postcode"},
}

func testGeoCfg() config.GeocodeConf {
	return config.GeocodeConf{
		UseSourceCoords:     true,
		RefineBelow:         "street",
		SourceZoomLevels:    zoomLevels,
		NominatimTypeLevels: typeLevels,
	}
}

func q(street, city string) Query {
	return Query{Street: street, City: city, Country: "Czechia", CountryCode: "CZ"}
}

func TestSourceConfidence(t *testing.T) {
	cases := map[int]string{
		17: "street", 16: "street",
		15: "municipality", 13: "municipality",
		12: "postcode", 1: "postcode",
	}
	for zoom, want := range cases {
		if got := SourceConfidence(zoom, zoomLevels); got != want {
			t.Errorf("SourceConfidence(%d) = %q, ожидалось %q", zoom, got, want)
		}
	}
	if got := SourceConfidence(12, nil); got != "" {
		t.Errorf("без правил ожидалось \"\", получено %q", got)
	}
}

func TestTypeConfidence(t *testing.T) {
	cases := map[string]string{
		"house": "building", "road": "street", "residential": "street", "tertiary": "street",
		"city": "municipality", "postcode": "postcode",
	}
	for typ, want := range cases {
		if got := TypeConfidence(typ, typeLevels); got != want {
			t.Errorf("TypeConfidence(%q) = %q, ожидалось %q", typ, got, want)
		}
	}
	if got := TypeConfidence("waterfall", typeLevels); got != "" {
		t.Errorf("неизвестный тип ожидал \"\", получено %q", got)
	}
	if got := TypeConfidence("", typeLevels); got != "" {
		t.Errorf("пустой тип ожидал \"\", получено %q", got)
	}
}

func TestRank(t *testing.T) {
	if Rank("building") <= Rank("street") || Rank("street") <= Rank("postcode") {
		t.Fatalf("порядок ТЗ: building > street > postcode")
	}
	if Rank("") >= 0 {
		t.Errorf("пустой уровень должен быть ниже всех")
	}
}

func TestHaversineKM(t *testing.T) {
	if d := HaversineKM(50.0875, 14.4213, 49.1951, 16.6068); d < 185 || d > 197 {
		t.Errorf("Прага–Брно = %.1f км, ожидалось ≈191", d)
	}
	if d := HaversineKM(50, 14, 51, 14); d < 110.5 || d > 111.5 {
		t.Errorf("1° широты = %.1f км, ожидалось ≈111", d)
	}
	if d := HaversineKM(50, 14, 50, 14); d != 0 {
		t.Errorf("точка сама с собой = %v, ожидалось 0", d)
	}
}

type stubGeocoder struct {
	p     *Point
	err   error
	calls int
	last  Query
}

func (s *stubGeocoder) Resolve(ctx context.Context, q Query) (*Point, error) {
	s.calls++
	s.last = q
	return s.p, s.err
}

var praha5 = &Point{Lat: 50.037352, Lng: 14.382048}

func TestResolveSourceAtThresholdSkipsGeocoder(t *testing.T) {
	gc := &stubGeocoder{p: &Point{Lat: 50.08, Lng: 14.42, Type: "road"}}
	r, err := Resolve(context.Background(), testGeoCfg(), praha5, "", 16, q("Divíšková", "Praha 5"), gc)
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != "source" || r.Confidence != "street" {
		t.Errorf("r = %+v, ожидалось source/street (zoom 16 ≥ refine_below)", r)
	}
	if gc.calls != 0 {
		t.Errorf("геокодер вызван %d раз(а), ожидалось 0", gc.calls)
	}
}

func TestResolveRefinePostcodeToStreet(t *testing.T) {
	// C10: zoom 12 = центроид PSČ. Улица в тексте → Nominatim.
	gc := &stubGeocoder{p: &Point{Lat: 50.04, Lng: 14.39, Type: "road"}}
	r, err := Resolve(context.Background(), testGeoCfg(), praha5, "", 12, q("Divíšková", "Praha 5"), gc)
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != "geocoder" || r.Confidence != "street" {
		t.Errorf("r = %+v, ожидалось geocoder/street", r)
	}
	if gc.calls != 1 {
		t.Errorf("вызовов геокодера: %d, ожидалось 1", gc.calls)
	}
}

func TestResolveRefineKeepsPostcodeIfGeocoderCity(t *testing.T) {
	// Nominatim вернул город — не лучше центроида PSČ, площадку не подменяем.
	gc := &stubGeocoder{p: &Point{Lat: 50.08, Lng: 14.42, Type: "city"}}
	r, err := Resolve(context.Background(), testGeoCfg(), praha5, "", 12, q("Divíšková", "Praha 5"), gc)
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != "source" || r.Confidence != "postcode" {
		t.Errorf("r = %+v, ожидалось source/postcode (geocoder municipality < street)", r)
	}
}

func TestResolveRefineRequiresStreet(t *testing.T) {
	gc := &stubGeocoder{p: &Point{Lat: 50.08, Lng: 14.42, Type: "road"}}
	r, err := Resolve(context.Background(), testGeoCfg(), praha5, "", 12, q("", "Praha 5"), gc)
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != "source" || r.Confidence != "postcode" {
		t.Errorf("r = %+v, ожидалось source/postcode (без улицы геокодер не зовём)", r)
	}
	if gc.calls != 0 {
		t.Errorf("геокодер вызван без улицы")
	}
}

func TestResolveGeocoderFallback(t *testing.T) {
	cfg := testGeoCfg()
	gc := &stubGeocoder{p: &Point{Lat: 50.0875, Lng: 14.4213, Type: "city"}}
	r, err := Resolve(context.Background(), cfg, nil, "", 0, q("Přadlácká", "Brno"), gc)
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != "geocoder" || r.Confidence != "municipality" {
		t.Errorf("r = %+v, ожидалось geocoder/municipality", r)
	}
	gc = &stubGeocoder{p: &Point{Lat: 49.2, Lng: 16.6, Type: "waterfall"}}
	r, _ = Resolve(context.Background(), cfg, nil, "", 0, q("", "Brno"), gc)
	if r.Source != "geocoder" || r.Confidence != "" {
		t.Errorf("r = %+v, ожидалось geocoder/NULL", r)
	}
}

func TestResolveGeocoderMiss(t *testing.T) {
	cfg := testGeoCfg()
	gc := &stubGeocoder{p: nil}
	r, err := Resolve(context.Background(), cfg, nil, "", 0, q("Xyzova", "Brno"), gc)
	if err != nil || r != nil {
		t.Errorf("r = %+v, err = %v; ожидалось nil/nil", r, err)
	}
	r, err = Resolve(context.Background(), cfg, praha5, "", 12, q("Divíšková", "Praha 5"), gc)
	if err != nil {
		t.Fatal(err)
	}
	if r == nil || r.Source != "source" || r.Confidence != "postcode" {
		t.Errorf("r = %+v, ожидалось source/postcode (miss → площадка)", r)
	}
}

func TestResolveSanityConflict(t *testing.T) {
	cfg := testGeoCfg()
	far := &Point{Lat: 49.1951, Lng: 16.6068, Type: "road"} // Брно против Праги
	gc := &stubGeocoder{p: far}
	r, err := Resolve(context.Background(), cfg, praha5, "", 12, q("Divíšková", "Praha 5"), gc)
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != "source" || r.Confidence != "postcode" {
		t.Errorf("r = %+v, ожидалось source/postcode (конфликт >50 км)", r)
	}
}

func TestResolveUseSourceOff(t *testing.T) {
	cfg := testGeoCfg()
	cfg.UseSourceCoords = false
	gc := &stubGeocoder{p: &Point{Lat: 50.08, Lng: 14.42, Type: "road"}}
	r, err := Resolve(context.Background(), cfg, praha5, "", 12, q("Divíšková", "Praha 5"), gc)
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != "geocoder" || r.Confidence != "street" {
		t.Errorf("r = %+v, ожидалось geocoder/street", r)
	}
}

func TestResolveNoGeocoder(t *testing.T) {
	cfg := testGeoCfg()
	r, err := Resolve(context.Background(), cfg, praha5, "", 12, q("Divíšková", "Praha 5"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != "source" || r.Confidence != "postcode" {
		t.Errorf("r = %+v, ожидалось source/postcode", r)
	}
	r, err = Resolve(context.Background(), cfg, nil, "", 0, q("Přadlácká", "Brno"), nil)
	if err != nil || r != nil {
		t.Errorf("r = %+v, err = %v; ожидалось nil/nil", r, err)
	}
}

func TestResolveNoAddressForGeocoder(t *testing.T) {
	cfg := testGeoCfg()
	gc := &stubGeocoder{p: &Point{Lat: 1, Lng: 1, Type: "city"}}
	r, err := Resolve(context.Background(), cfg, nil, "", 0, q("", ""), gc)
	if err != nil {
		t.Fatal(err)
	}
	if r != nil {
		t.Errorf("r = %+v, ожидалось nil", r)
	}
	if gc.calls != 0 {
		t.Errorf("геокодер вызван %d раз(а), ожидалось 0", gc.calls)
	}
}

func TestResolveGeocoderError(t *testing.T) {
	cfg := testGeoCfg()
	gc := &stubGeocoder{err: context.Canceled}
	if _, err := Resolve(context.Background(), cfg, nil, "", 0, q("X", "Y"), gc); err == nil {
		t.Error("ожидалась ошибка геокодера")
	}
}

func TestResolveStoredConfidence(t *testing.T) {
	// Бэкфилл: zoom неизвестен, уровень — из колонки.
	gc := &stubGeocoder{p: &Point{Lat: 50.04, Lng: 14.39, Type: "house"}}
	r, err := Resolve(context.Background(), testGeoCfg(), praha5, "postcode", 0, q("Divíšková", "Praha 5"), gc)
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != "geocoder" || r.Confidence != "building" {
		t.Errorf("r = %+v, ожидалось geocoder/building", r)
	}
}
