package geocode

import "testing"

func TestExtractStreet(t *testing.T) {
	cases := []struct {
		text, city, want string
	}{
		{"Nabízíme byt v ulici Poděbradská, Praha 9 – Vysočany.", "Praha 9", "Poděbradská"},
		{"na Mladcové v ulici U Hřiště, na vás čeká", "Zlín", "U Hřiště"},
		{"bytové jednotky v ulici Rokycanova na pražském Žižkově", "", "Rokycanova"},
		{"byt 3+1/l v OV v ulici Družstevní . Byt nacházející", "Rakovník", "Družstevní"},
		{"na ulici Vysoká 2 v Brně", "Brno", "Vysoká 2"},
		{"Prodám byt na ul. Na Robinsonce 1643/5, Ostrava", "Ostrava", "Na Robinsonce 1643/5"},
		{"Prodej, byty/2+kk, 49.2 m2, Poděbradova 2842/1, Jižní Předměstí, 30100 Plzeň", "Plzeň", "Poděbradova 2842/1"},
		{"byt o dispozici 1+kk a celkové ploše 45 m²", "Praha", ""},
		{"v ulici Praha 5 hezký byt", "Praha 5", ""}, // город ≠ улица
		{"", "Brno", ""},
		{"Byt 4+kk, Zlín – Pod Tlustou 5054. Ulice Pod Tlustou patří mezi vyhledávané", "Zlín", "Pod Tlustou"},
		{"lokalitě Brno-Královo Pole, ulice Skácelova.", "Brno", "Skácelova"},
		{"PRODEJ 4+kk 104m² – Mníšek pod Brdy", "Praha - západ", ""},
	}
	for _, c := range cases {
		got := ExtractStreet(c.text, c.city)
		if got != c.want {
			t.Errorf("ExtractStreet(%q, %q) = %q, want %q", c.text, c.city, got, c.want)
		}
	}
}

func TestParseCityPostcode(t *testing.T) {
	city, pc := ParseCityPostcode("Praha 5, 150 00")
	if city != "Praha 5" || pc != "150 00" {
		t.Errorf("got %q / %q", city, pc)
	}
	city, pc = ParseCityPostcode("Frýdek - Místek, 738 01")
	if city != "Frýdek - Místek" || pc != "738 01" {
		t.Errorf("got %q / %q", city, pc)
	}
	city, pc = ParseCityPostcode("150 00")
	if city != "" || pc != "150 00" {
		t.Errorf("only postcode: %q / %q", city, pc)
	}
}
