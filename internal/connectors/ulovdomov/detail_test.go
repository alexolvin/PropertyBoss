package ulovdomov

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const detailSHA = "b65ba9dd418fb77700188516e77f3e335328af86f817371cb204f21167d582f4"

func TestParseDetail5462328(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "detail_5462328.html"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != detailSHA {
		t.Fatalf("sha256 %s", hex.EncodeToString(sum[:]))
	}
	d, err := ParseDetailHTML(string(b))
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "Prodej domu 57 m2" {
		t.Errorf("title %q", d.Title)
	}
	if d.Address != "Dolní Zálezly" {
		t.Errorf("address %q", d.Address)
	}
	if d.Lat == nil || d.Lng == nil {
		t.Fatal("coords")
	}
	if *d.Lat < 50.5 || *d.Lat > 50.7 {
		t.Errorf("lat %v", *d.Lat)
	}
	if d.PriceMinor == nil || *d.PriceMinor != 3290000*100 {
		t.Errorf("price %v", d.PriceMinor)
	}
	if len(d.Photos) != 3 {
		t.Errorf("photos %d", len(d.Photos))
	}
	if d.Attrs["energy_class"] != "G" {
		t.Errorf("energy %v", d.Attrs["energy_class"])
	}
	if d.Attrs["condition"] != "very_good" {
		t.Errorf("condition %v", d.Attrs["condition"])
	}
	if d.Attrs["has_elevator"] != false {
		t.Errorf("lift %v", d.Attrs["has_elevator"])
	}
	if _, ok := d.Attrs["floor"]; ok {
		t.Errorf("floor 0 must be omitted, got %v", d.Attrs["floor"])
	}
	if d.Unmapped["heating_label"] == nil && d.Unmapped["heating"] == nil {
		t.Errorf("heating missing %v", d.Unmapped)
	}
	if d.SellerType != "agency" {
		t.Errorf("seller %q %q", d.SellerType, d.SellerName)
	}
	if !strings.Contains(d.Description, "Chalupa") {
		t.Errorf("description %q", d.Description[:80])
	}
	label, _ := d.Unmapped["water_label"].(string)
	if !strings.Contains(strings.ToLower(label), "sezónní") && !strings.Contains(strings.ToLower(label), "sezonni") {
		t.Errorf("water_label %q, ждали сезонный водопровод из текста", label)
	}
	if d.Attrs["plot_area"] == nil {
		t.Errorf("plot_area missing %v", d.Attrs)
	}
	if d.Attrs["floors"] != 3 {
		t.Errorf("floors %v, ждали 3 из třípodlažní (форма площадки = 2)", d.Attrs["floors"])
	}
	if d.AreaSqM == nil || *d.AreaSqM != 57 {
		t.Errorf("area_sqm %v (užitná/obytná 57, не total_area)", d.AreaSqM)
	}
	if _, has := d.Attrs["total_area"]; has {
		t.Errorf("total_area из парсера не должна появляться: %v", d.Attrs)
	}
}
