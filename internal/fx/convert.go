package fx

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/money"
)

var currencyEUR = money.Currency{Code: "EUR", Exponent: 2}

// RateLookup — курс 1 EUR → quote по функции fx_rate_for():
// точный курс на дату либо последний известный с пометкой stale (ТЗ §5).
type RateLookup struct {
	Rate     string // десятичная строка, ровно как в NUMERIC
	RateDate time.Time
	Stale    bool
}

// LookupEURRate берёт курс 1 EUR → quote на дату onDate из fx_rates.
func LookupEURRate(ctx context.Context, pool *pgxpool.Pool, quote string, onDate time.Time) (*RateLookup, error) {
	var rate string
	var rateDate time.Time
	var stale bool
	err := pool.QueryRow(ctx,
		"SELECT rate, rate_date, stale FROM fx_rate_for('EUR', $1, $2)",
		quote, onDate).Scan(&rate, &rateDate, &stale)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("fx: нет курса EUR/%s (fx_rates пуст или данных нет до %s)", quote, onDate.Format("2006-01-02"))
	}
	if err != nil {
		return nil, fmt.Errorf("fx: запрос курса EUR/%s: %w", quote, err)
	}
	return &RateLookup{Rate: rate, RateDate: rateDate, Stale: stale}, nil
}

// ConvertMinor переводит целую сумму в минорных единицах валюты from в
// минорные единицы to через референс EUR. Точная арифметика (big.Rat),
// float не используется (ТЗ §5). Конвертация — только для отображения:
// в БД и в API сумма всегда живёт в валюте рынка.
//
// Метаданные ответа: показывается более старая из дат использованных курсов
// (слабое звено) и stale, если хотя бы один курс подставлен из прошлого.
func ConvertMinor(minor int64, from, to money.Currency, lookup func(quote string) (*RateLookup, error)) (int64, *RateLookup, error) {
	if from.Code == to.Code {
		return minor, nil, nil
	}

	m := money.Money{Minor: minor, Currency: from}
	var used []*RateLookup

	if from.Code != "EUR" {
		rl, err := lookup(from.Code)
		if err != nil {
			return 0, nil, err
		}
		used = append(used, rl)
		r, err := money.ParseRate(currencyEUR, from, rl.Rate)
		if err != nil {
			return 0, nil, err
		}
		m, err = r.Inverse().Apply(m) // from → EUR
		if err != nil {
			return 0, nil, err
		}
	}
	if to.Code != "EUR" {
		rl, err := lookup(to.Code)
		if err != nil {
			return 0, nil, err
		}
		used = append(used, rl)
		r, err := money.ParseRate(currencyEUR, to, rl.Rate)
		if err != nil {
			return 0, nil, err
		}
		m, err = r.Apply(m) // EUR → to
		if err != nil {
			return 0, nil, err
		}
	}

	meta := &RateLookup{RateDate: used[0].RateDate, Stale: false}
	for _, rl := range used {
		if rl.RateDate.Before(meta.RateDate) {
			meta.RateDate = rl.RateDate
		}
		meta.Stale = meta.Stale || rl.Stale
	}
	return m.Minor, meta, nil
}
