package money

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	czkc = Currency{Code: "CZK", Exponent: 2}
	eurc = Currency{Code: "EUR", Exponent: 2}
)

func mustParse(t *testing.T, s string, c Currency) Money {
	t.Helper()
	m, err := Parse(s, c)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return m
}

func TestParse(t *testing.T) {
	cases := []struct {
		in       string
		cur      Currency
		minor    int64
		wantErr  bool
	}{
		{"1234.56", czkc, 123456, false},
		{"1234,56", czkc, 123456, false},
		{"1 234.56", czkc, 123456, false},
		{"0.05", eurc, 5, false},
		{".56", czkc, 56, false},
		{"5.", czkc, 500, false},
		{"-12.3", czkc, -1230, false},
		{"0", czkc, 0, false},
		{"", czkc, 0, true},
		{"1.234", czkc, 0, true},   // три знака при экспоненте 2
		{"abc", czkc, 0, true},
		{"99999999999999999", czkc, 0, true}, // переполнение
	}
	for _, tc := range cases {
		_, err := Parse(tc.in, tc.cur)
		if tc.wantErr {
			if err == nil {
				t.Errorf("Parse(%q): ожидалась ошибка", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.in, err)
			continue
		}
		got, _ := Parse(tc.in, tc.cur)
		if got.Minor != tc.minor {
			t.Errorf("Parse(%q) = %d, ожидалось %d", tc.in, got.Minor, tc.minor)
		}
	}
}

func TestStringRoundTrip(t *testing.T) {
	cases := []struct {
		money Money
		want  string
	}{
		{Money{123456, czkc}, "1234.56"},
		{Money{5, eurc}, "0.05"},
		{Money{100, eurc}, "1.00"},
		{Money{-1230, czkc}, "-12.30"},
		{Money{0, czkc}, "0.00"},
	}
	for _, tc := range cases {
		if got := tc.money.String(); got != tc.want {
			t.Errorf("String(%v) = %q, ожидалось %q", tc.money, got, tc.want)
		}
	}
}

// Классическая ловушка float (0.1 + 0.2 ≠ 0.3) обязана пройти точно.
func TestNoFloatClassic(t *testing.T) {
	a := mustParse(t, "0.10", eurc)
	b := mustParse(t, "0.20", eurc)
	sum, err := a.Add(b)
	if err != nil {
		t.Fatal(err)
	}
	want := mustParse(t, "0.30", eurc)
	if sum.Minor != want.Minor {
		t.Fatalf("0.10 + 0.20 = %v, ожидалось %v", sum, want)
	}
}

func TestAddSubDifferentCurrenciesFail(t *testing.T) {
	a := mustParse(t, "1", eurc)
	b := mustParse(t, "1", czkc)
	if _, err := a.Add(b); err == nil {
		t.Error("Add разных валют: ожидалась ошибка")
	}
	if _, err := a.Sub(b); err == nil {
		t.Error("Sub разных валют: ожидалась ошибка")
	}
}

// 24.100 CZK за 1 EUR (курс из реального XML ЕЦБ от 2026-08-24).
func TestConvertExact(t *testing.T) {
	rate, err := ParseRate(eurc, czkc, "24.100")
	if err != nil {
		t.Fatal(err)
	}
	// 1 EUR → ровно 24.10 CZK
	got, err := rate.Apply(mustParse(t, "1.00", eurc))
	if err != nil {
		t.Fatal(err)
	}
	if got.Minor != 2410 {
		t.Fatalf("1.00 EUR = %v CZK, ожидалось 24.10", got)
	}
	// 3.10 EUR → ровно 74.71 CZK (3.1 × 24.1 = 74.71, float дал бы 74.71000000000001)
	got, err = rate.Apply(mustParse(t, "3.10", eurc))
	if err != nil {
		t.Fatal(err)
	}
	if got.Minor != 7471 {
		t.Fatalf("3.10 EUR = %v CZK, ожидалось 74.71", got)
	}
	// 207.47 EUR → CZK: 207.47 × 24.1 = 5000.027 → 5000.03 CZK
	czk, err := rate.Apply(mustParse(t, "207.47", eurc))
	if err != nil {
		t.Fatal(err)
	}
	if czk.Minor != 500003 {
		t.Fatalf("207.47 EUR = %v CZK, ожидалось 5000.03", czk)
	}
}

func TestConvertRoundingHalfAwayFromZero(t *testing.T) {
	// Курс 1.0001: 1.00 → 1.0001 → 1.00 (округление до 2 знаков)
	rate, err := ParseRate(eurc, czkc, "1.0001")
	if err != nil {
		t.Fatal(err)
	}
	got, err := rate.Apply(mustParse(t, "1.00", eurc))
	if err != nil {
		t.Fatal(err)
	}
	if got.Minor != 100 {
		t.Fatalf("ожидалось 100, получилось %d", got.Minor)
	}
	// 0.01 CZK × 0.5 (CZK→EUR) = 0.005 EUR = 0.5 цента → half away: 1
	rate2, err := ParseRate(czkc, eurc, "0.5")
	if err != nil {
		t.Fatal(err)
	}
	got, err = rate2.Apply(Money{Minor: 1, Currency: czkc})
	if err != nil {
		t.Fatal(err)
	}
	if got.Minor != 1 {
		t.Fatalf("0.01 CZK при курсе 0.5: ожидалось 1 (round half away), получилось %d", got.Minor)
	}
}

// Критерий этапа 1 ТЗ: «тест на отсутствие float в денежном пути проходит».
// Денежный путь = пакеты money и fx: ни float32, ни float64.
// Проверка точная, по AST: ловим фактическое использование типов float
// (объявления, конверсии, литералы-идентификаторы), а не упоминания в
// комментариях. Дополнительно запрещён импорт «math» (плоской математики
// с плавающей точкой) — math/big разрешён и используется для точной арифметики.
func TestNoFloatInMoneyPath(t *testing.T) {
	fileSet := token.NewFileSet()
	for _, dir := range []string{".", "../fx"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir(%s): %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			f, err := parser.ParseFile(fileSet, path, nil, 0)
			if err != nil {
				t.Fatalf("%s: разбор: %v", path, err)
			}
			for _, imp := range f.Imports {
				if imp.Path.Value == `"math"` {
					t.Errorf("%s: импорт math (float-математика) в денежном пути — нарушение ТЗ §5", path)
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && (id.Name == "float32" || id.Name == "float64") {
					t.Errorf("%s:%d: тип %s в денежном пути — нарушение ТЗ §5",
						path, fileSet.Position(id.Pos()).Line, id.Name)
				}
				// big.Float — тоже плавающая точка (хотя и произвольной точности).
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if x, ok2 := sel.X.(*ast.Ident); ok2 && x.Name == "big" && sel.Sel.Name == "Float" {
						t.Errorf("%s:%d: big.Float в денежном пути — нарушение ТЗ §5",
							path, fileSet.Position(sel.Pos()).Line)
					}
				}
				return true
			})
		}
	}
}
