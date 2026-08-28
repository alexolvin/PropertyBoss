package main

import (
	"context"
	"errors"
	"flag"
	"log"

	"propertyboss/internal/config"
	"propertyboss/internal/db"
	"propertyboss/internal/valuation"
)

// pb valuate — гедоническая модель (этап 5, ТЗ §7.2–7.3):
//
//	pb valuate [-country XX] [-deal-type T]
//
// По умолчанию — все страны из dashboard.countries × все deal_types из
// dashboard.deal_types (ТЗ §7.2: модель отдельно для каждой страны и
// типа сделки). При недостатке данных модель не выдаёт число — записывает
// NULL с причиной (ТЗ §7.3).
func runValuate(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("valuate", flag.ExitOnError)
	country := fs.String("country", "", "страна (необязательно; по умолчанию все из конфига)")
	dealType := fs.String("deal-type", "", "тип сделки (необязательно; по умолчанию все из конфига)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var countries, dealTypes []string
	if *country != "" {
		countries = []string{*country}
	} else {
		countries = cfg.Dashboard.Countries
	}
	if *dealType != "" {
		dealTypes = []string{*dealType}
	} else {
		dealTypes = cfg.Dashboard.DealTypes
	}

	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	failed := false
	for _, c := range countries {
		for _, d := range dealTypes {
			rep, err := valuation.Run(ctx, pool, cfg, c, d)
			if err != nil {
				log.Printf("valuate %s/%s: ОШИБКА: %v", c, d, err)
				failed = true
				continue
			}
			if rep.Rejected {
				log.Printf("valuate %s/%s: модель ОТКЛОНЕНА (ТЗ §7.3): %s; записано строк=%d (NULL с причиной), в выборке=%d",
					c, d, rep.Reason, rep.Wrote, rep.InSample)
				continue
			}
			log.Printf("valuate %s/%s: построена: n=%d params=%d lambda=%g r2=%.4f; оценено=%d null=%d записано=%d (version=%s)",
				c, d, rep.SampleSize, rep.Params, rep.Lambda, rep.RSquared, rep.Valued, rep.Nulls, rep.Wrote, rep.ModelVersion)
			if rep.ExcludedCurrency > 0 {
				log.Printf("valuate %s/%s: ВНИМАНИЕ: %d активных объектов без валюты или не рыночной валюты исключены (no_currency / currency_mismatch)",
					c, d, rep.ExcludedCurrency)
			}
		}
	}
	if failed {
		return errors.New("valuate: были ошибки — см. журнал")
	}
	return nil
}
