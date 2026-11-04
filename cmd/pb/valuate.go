package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"sort"
	"strings"

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
				log.Printf("valuate %s/%s: модель ОТКЛОНЕНА (ТЗ §7.3 / v4-2 §4.10): %s; записано строк=%d (NULL с причиной), в выборке=%d",
					c, d, rep.Reason, rep.Wrote, rep.InSample)
			} else {
				// rank/cond (C21): матрица подгонки XᵀX+λP — ранг полный
				// и обусловленность под порогом MaxMatCond (иначе модель
				// была бы отклонена degenerate_matrix).
				log.Printf("valuate %s/%s: построена: n=%d params=%d lambda=%g r2=%.4f rank=%d cond=%.3e; оценено=%d null=%d записано=%d (version=%s)",
					c, d, rep.SampleSize, rep.Params, rep.Lambda, rep.RSquared, rep.MatRank, rep.MatCond, rep.Valued, rep.Nulls, rep.Wrote, rep.ModelVersion)
				if len(rep.LambdaCVMSE) > 0 {
					var parts []string
					for i, mse := range rep.LambdaCVMSE {
						lab := fmt.Sprintf("%.4g", rep.LambdaCVMSE[i])
						if i < len(rep.LambdaGrid) {
							lab = fmt.Sprintf("%.4g", rep.LambdaGrid[i])
						}
						parts = append(parts, fmt.Sprintf("λ=%s:%.4g", lab, mse))
					}
					log.Printf("valuate %s/%s: CV-MSE по сетке λ (выбор λ — кросс-валидация, ТЗ §7.2): %s",
						c, d, strings.Join(parts, " "))
				}
			}
			// C25: доля not_modeled публикуется ВСЕГДА (и при отклонённой
			// модели); критерий — по последствию (объект с флагом не
			// показывает оценок), а не по «уровню у 100%». Превышение
			// порога — дефект и разбор.
			log.Printf("valuate %s/%s: not_modeled (уровень геокода отсутствует): %d/%d = %.2f%% (порог %.2f%%, C25)",
				c, d, rep.NotModeled, rep.Corpus, 100*rep.NotModeledShare, 100*cfg.Valuation.MaxNotModeledShare)
			if rep.NotModeledExceeds {
				log.Printf("valuate %s/%s: ДЕФЕКТ (C25): доля not_modeled %.2f%% выше порога %.2f%% — требуется разбор",
					c, d, 100*rep.NotModeledShare, 100*cfg.Valuation.MaxNotModeledShare)
			}
			// C26: уровни продавца с долями («unknown» — полноценный
			// уровень, а не пропуск) + коэффициенты onehot-уровней
			// seller_type.
			if len(rep.SellerLevels) > 0 {
				line := fmt.Sprintf("valuate %s/%s: seller-уровни в корпусе (C26): %s",
					c, d, fmtLevels(rep.SellerLevels, rep.Corpus, []string{"agency", "private", "developer", "unknown"}))
				if len(rep.SellerCoeffs) > 0 {
					line += "; коэффициенты: " + fmtCoeffs(rep.SellerCoeffs, []string{"agency", "private", "developer", "not_specified"})
				}
				log.Printf("%s", line)
			}
			if len(rep.RequiredCoverage) > 0 {
				log.Printf("valuate %s/%s: покрытие обязательного набора (порог %.2f, ТЗ v4-2 §4.10.1): %s",
					c, d, cfg.Valuation.MaxMissingRate, fmtRates(rep.RequiredCoverage, []string{"price", "area", "zone", "type"}))
			}
			if len(rep.MissingRates) > 0 {
				line := fmt.Sprintf("valuate %s/%s: доли пропусков опциональных признаков в выборке (ТЗ v4-2 §4.10.5): %s",
					c, d, fmtRates(rep.MissingRates, nil))
				if len(rep.ExcludedAttrs) > 0 {
					line += fmt.Sprintf("; исключены из модели: %s", strings.Join(rep.ExcludedAttrs, ", "))
				}
				log.Printf("%s", line)
			}
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

// fmtRates — доли «ключ=0.45», по фиксированному (order) или
// алфавитному (order==nil) порядку; отсутствующий ключ — 0.00.
func fmtRates(rates map[string]float64, order []string) string {
	keys := order
	if keys == nil {
		keys = make([]string, 0, len(rates))
		for k := range rates {
			keys = append(keys, k)
		}
		sort.Strings(keys)
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%.2f", k, rates[k]))
	}
	return strings.Join(parts, " ")
}

// fmtLevels — «level=count (доля%)» по фиксированному (order) или
// алфавитному (order==nil) порядку; уровни вне order не теряются —
// добавляются в конце в алфавитном порядке (C26: developer — четвёртый
// уровень корпуса, публикуется отдельно и флагается оператору).
func fmtLevels(levels map[string]int, corpus int, order []string) string {
	keys := order
	if keys == nil {
		keys = make([]string, 0, len(levels))
		for k := range levels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
	}
	seen := map[string]bool{}
	for _, k := range keys {
		seen[k] = true
	}
	var extra []string
	for k := range levels {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	keys = append(keys, extra...)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		share := 0.0
		if corpus > 0 {
			share = 100 * float64(levels[k]) / float64(corpus)
		}
		parts = append(parts, fmt.Sprintf("%s=%d (%.1f%%)", k, levels[k], share))
	}
	return strings.Join(parts, " ")
}

// fmtCoeffs — «level=β» по фиксированному (order) или алфавитному
// (order==nil) порядку; уровни вне order добавляются в конце в
// алфавитном порядке. β — коэффициент onehot-уровня (log цены/м²).
func fmtCoeffs(coeffs map[string]float64, order []string) string {
	keys := order
	if keys == nil {
		keys = make([]string, 0, len(coeffs))
		for k := range coeffs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
	}
	seen := map[string]bool{}
	for _, k := range keys {
		seen[k] = true
	}
	var extra []string
	for k := range coeffs {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	keys = append(keys, extra...)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%.4f", k, coeffs[k]))
	}
	return strings.Join(parts, " ")
}
