package bezrealitky

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// Фикстура — __NEXT_DATA__ страницы /vypis/nabidka-pronajem/byt
// проба bezrealitky.cz run_id=20260911T041041Z
// sha256 исходного 04-listing.html =
// 99b6b9a8ab0cfd70926d7996bac6fd61d3b527f991d3a6d87ac60b385283961d
const fixtureSHA = "10f3d696e6ec845b5dfe913225b84a047f06e9c4f3932ea7afd4cf0b20e78bdf"

func TestParseListingNextDataSHAAndFields(t *testing.T) {
	p := filepath.Join("testdata", "listing_nextdata.html")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != fixtureSHA {
		t.Fatalf("sha256 фикстуры %s ≠ %s (проба 20260911T041041Z)", hex.EncodeToString(sum[:]), fixtureSHA)
	}
	ls, err := ParseListingPage(string(b), "https://www.bezrealitky.cz")
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) == 0 {
		t.Fatal("0 объявлений — разбор пуст")
	}
	var withPrice, withArea, withType, withURL int
	for _, l := range ls {
		if l.ExternalID == "" {
			t.Fatal("пустой id")
		}
		if l.URL != "" {
			withURL++
		}
		if l.PriceMinor != nil {
			withPrice++
		}
		if l.AreaSqM != nil {
			withArea++
		}
		if l.PropertyType != nil {
			withType++
		}
	}
	if withURL != len(ls) || withPrice != len(ls) {
		t.Fatalf("n=%d url=%d price=%d area=%d type=%d", len(ls), withURL, withPrice, withArea, withType)
	}
}
