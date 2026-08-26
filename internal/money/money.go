// Package money — деньги в минорных единицах валюты, только целые числа.
//
// ТЗ §5: плавающая точка для денег запрещена на всех уровнях.
// Сумма — int64 в минорных единицах (халержи для CZK, центы для EUR).
// Конвертация между валютами — точная рациональная арифметика (math/big),
// без промежуточного float: 0.1 + 0.2 здесь всегда ровно 0.3.
package money

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// Currency — код ISO 4217 + экспонент из справочника currencies (не из кода).
type Currency struct {
	Code     string // "EUR", "CZK"
	Exponent int    // 2 для EUR и CZK (источник: ISO 4217)
}

// Money — сумма в минорных единицах валюты.
type Money struct {
	Minor    int64
	Currency Currency
}

func (c Currency) minorFactor() *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(c.Exponent)), nil)
}

func pow10(n int) int64 {
	v := int64(1)
	for i := 0; i < n; i++ {
		v *= 10
	}
	return v
}

// Parse разбирает сумму из десятичной строки, не проходя через float:
// "1234.56" → 123456. Допускает '.' или ',' в качестве десятичного разделителя
// и пробел / no-break space как разделители тысяч.
func Parse(s string, c Currency) (Money, error) {
	s = strings.Map(func(r rune) rune {
		if r == ' ' || r == ' ' {
			return -1
		}
		return r
	}, s)
	if s == "" {
		return Money{}, fmt.Errorf("money: пустая сумма")
	}
	neg := false
	if s[0] == '-' {
		neg = true
		s = s[1:]
	} else if s[0] == '+' {
		s = s[1:]
	}
	dot, comma := strings.IndexByte(s, '.'), strings.IndexByte(s, ',')
	sep := -1
	switch {
	case dot >= 0 && comma >= 0:
		sep = max(dot, comma) // более поздний — десятичный
	case dot >= 0:
		sep = dot
	case comma >= 0:
		sep = comma
	}
	intPart, fracPart := s, ""
	if sep >= 0 {
		intPart, fracPart = s[:sep], s[sep+1:]
	}
	if intPart == "" {
		intPart = "0"
	}
	if len(intPart) > 15 {
		return Money{}, fmt.Errorf("money: сумма слишком большая: %s", s)
	}
	if len(fracPart) > c.Exponent {
		return Money{}, fmt.Errorf("money: больше %d знаков после запятой для %s: %s", c.Exponent, c.Code, s)
	}
	intV, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return Money{}, fmt.Errorf("money: некорректная сумма %q: %w", s, err)
	}
	fracV := int64(0)
	if fracPart != "" {
		fracV, err = strconv.ParseInt(fracPart+strings.Repeat("0", c.Exponent-len(fracPart)), 10, 64)
		if err != nil {
			return Money{}, fmt.Errorf("money: некорректная дробная часть %q: %w", s, err)
		}
	}
	minor := intV*pow10(c.Exponent) + fracV
	if neg {
		minor = -minor
	}
	return Money{Minor: minor, Currency: c}, nil
}

// String — десятичная запись в основных единицах: 123456 → "1234.56".
func (m Money) String() string {
	sign := ""
	abs := m.Minor
	if m.Minor < 0 {
		sign = "-"
		abs = -m.Minor
	}
	sep, frac := "", ""
	if m.Currency.Exponent > 0 {
		f := pow10(m.Currency.Exponent)
		sep = "."
		frac = fmt.Sprintf("%0*d", m.Currency.Exponent, abs%f)
	}
	return sign + fmt.Sprintf("%d%s%s", abs/int64(pow10(m.Currency.Exponent)), sep, frac)
}

func (m Money) checkSame(o Money) error {
	if m.Currency.Code != o.Currency.Code || m.Currency.Exponent != o.Currency.Exponent {
		return fmt.Errorf("money: разные валюты %s и %s", m.Currency.Code, o.Currency.Code)
	}
	return nil
}

// Add — сложение в одной валюте.
func (m Money) Add(o Money) (Money, error) {
	if err := m.checkSame(o); err != nil {
		return Money{}, err
	}
	return Money{Minor: m.Minor + o.Minor, Currency: m.Currency}, nil
}

// Sub — вычитание в одной валюте.
func (m Money) Sub(o Money) (Money, error) {
	if err := m.checkSame(o); err != nil {
		return Money{}, err
	}
	return Money{Minor: m.Minor - o.Minor, Currency: m.Currency}, nil
}

// Rate — точный курс: 1 единица From = r единиц To.
// Хранится как big.Rat — без float, без потери точности.
type Rate struct {
	From, To Currency
	r        *big.Rat
}

// ParseRate разбирает курс из десятичной строки: "24.100" → ровно 241/10.
func ParseRate(from, to Currency, s string) (Rate, error) {
	s = strings.ReplaceAll(s, ",", ".")
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return Rate{}, fmt.Errorf("money: некорректный курс %q", s)
	}
	if r.Sign() < 0 {
		return Rate{}, fmt.Errorf("money: отрицательный курс %q", s)
	}
	return Rate{From: from, To: to, r: r}, nil
}

// Inverse — обратный курс (1/r) с перестановкой From/To.
func (rt Rate) Inverse() Rate {
	return Rate{From: rt.To, To: rt.From, r: new(big.Rat).Inv(rt.r)}
}

// Apply переводит сумму из rt.From в rt.To с округлением до минорной единицы
// (half away from zero).
func (rt Rate) Apply(m Money) (Money, error) {
	if m.Currency.Code != rt.From.Code {
		return Money{}, fmt.Errorf("money: сумма в %s, курс из %s", m.Currency.Code, rt.From.Code)
	}
	// major = Minor / 10^exp(From) — точная рациональная величина
	major := new(big.Rat).SetFrac(new(big.Int).SetInt64(m.Minor), rt.From.minorFactor())
	// toMajor = major × rate
	toMajor := new(big.Rat).Mul(major, rt.r)
	// toMinor = toMajor × 10^exp(To) — целое после округления
	toMinor := new(big.Rat).Mul(toMajor, new(big.Rat).SetInt(rt.To.minorFactor()))

	q, rem := new(big.Int).QuoRem(toMinor.Num(), toMinor.Denom(), new(big.Int))
	// Округление half away from zero: |rem| × 2 >= den → +1 по знаку
	remAbs := new(big.Int).Abs(rem)
	if new(big.Int).Lsh(remAbs, 1).Cmp(toMinor.Denom()) >= 0 {
		if toMinor.Sign() >= 0 {
			q.Add(q, big.NewInt(1))
		} else {
			q.Sub(q, big.NewInt(1))
		}
	}
	if !q.IsInt64() {
		return Money{}, fmt.Errorf("money: переполнение при конвертации %s", m)
	}
	return Money{Minor: q.Int64(), Currency: rt.To}, nil
}
