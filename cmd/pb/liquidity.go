package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"

	"propertyboss/internal/config"
	"propertyboss/internal/db"
	"propertyboss/internal/liquidity"
)

// pb liquidity — модель ликвидности (этап 7, ТЗ §9):
//
//	pb liquidity [-country XX] [-deal-type T]
//
// Дискретная модель дожития (пуллогированная логистическая регрессия на
// person-period данных). По умолчанию — все страны из dashboard.countries
// × все deal_types из dashboard.deal_types. При недостатке истории или
// провале калибровки прогнозы NULL с причиной, модель не публикуется
// (ТЗ §9.3–9.4).
func runLiquidity(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("liquidity", flag.ExitOnError)
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
			rep, err := liquidity.Run(ctx, pool, cfg, c, d)
			if err != nil {
				log.Printf("liquidity %s/%s: ОШИБКА: %v", c, d, err)
				failed = true
				continue
			}
			switch rep.Status {
			case "insufficient_history":
				log.Printf("liquidity %s/%s: история недостаточна: завершено событий=%d, мин. порог=%d; прогнозы NULL (insufficient_history), записано строк=%d",
					c, d, rep.CompletedEvents, rep.MinEvents, rep.Wrote)
			case "uncalibrated":
				log.Printf("liquidity %s/%s: модель не публикуется: %s; прогнозы NULL (calibration_failed), записано строк=%d",
					c, d, rep.RejectReason, rep.Wrote)
			default: // published
				log.Printf("liquidity %s/%s: опубликована: объектов=%d событий=%d периодов=%d params=%d (train=%d/test=%d, cutoff=%s)",
					c, d, rep.Objects, rep.CompletedEvents, rep.NPeriods, rep.Params,
					rep.NTrain, rep.NTest, rep.TrainCutoff.Format("2006-01-02"))
				log.Printf("liquidity %s/%s: метрики: calib_max=%s brier=%s c_index=%s; оценено=%d null=%d записано=%d (version=%s)",
					c, d, fmtProb(rep.MaxCalibDev), fmtProb(rep.Brier), fmtProb(rep.CIndex),
					rep.Estimated, rep.Nulls, rep.Wrote, rep.ModelVersion)
			}
		}
	}
	if failed {
		return errors.New("liquidity: были ошибки — см. журнал")
	}
	return nil
}

// fmtProb — вероятность для журнала; nil — метрика не определена
// (например, C-index без событий в тестовой части).
func fmtProb(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.4f", *v)
}
