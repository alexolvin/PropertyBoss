package ulovdomov

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

const fixtureSHA = "af32148350d60c5e6ef049c626da1f3825e8c0932bc56f340d416c2728b85de5"

func TestParseFindRentFixture(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "find_rent.json"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != fixtureSHA {
		t.Fatalf("sha256 %s ≠ %s", hex.EncodeToString(sum[:]), fixtureSHA)
	}
	r, err := parseFind(b)
	if err != nil {
		t.Fatal(err)
	}
	ls := listingsFrom(r)
	if len(ls) != 2 {
		t.Fatalf("n=%d", len(ls))
	}
	if ls[0].ExternalID == "" || ls[0].URL == "" || ls[0].PriceMinor == nil || ls[0].AreaSqM == nil {
		t.Fatalf("%+v", ls[0])
	}
	if *ls[0].PriceMinor != 19900*100 {
		t.Fatalf("price minor %d", *ls[0].PriceMinor)
	}
}
