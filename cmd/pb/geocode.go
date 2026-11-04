// pb geocode — бэкфилл координат по сохранённым описаниям (ТЗ v4 §4.5).
//
//	run [--limit N] [--country XX] [--dry]
//
// Площадку не трогает. Кандидаты: active с уровнем ниже refine_below
// (по умолчанию street). Без улицы в тексте — PSČ оставляем, Nominatim
// не зовём. Кэш — таблица geocode_cache.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"propertyboss/internal/config"
	"propertyboss/internal/db"
	"propertyboss/internal/geocode"
	"propertyboss/internal/zones"
)

func runGeocode(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("pb geocode: нужен subcommand (run)")
	}
	switch args[0] {
	case "run":
		return runGeocodeRun(ctx, cfg, args[1:])
	default:
		return fmt.Errorf("pb geocode: неизвестный subcommand %q", args[0])
	}
}

func runGeocodeRun(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("geocode run", flag.ContinueOnError)
	limit := fs.Int("limit", 0, "сколько объектов за прогон (дефолт: без лимита)")
	country := fs.String("country", "", "один рынок (CZ|IT|NL); пусто — все")
	dry := fs.Bool("dry", false, "только извлечение улицы, без Nominatim")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.Geocode.BaseURL == "" && !*dry {
		return fmt.Errorf("pb geocode: секция geocode не сконфигурирована")
	}
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	rep, err := geocode.Run(ctx, pool, cfg.Geocode, *limit, *country, *dry)
	if err != nil {
		return err
	}
	log.Printf("geocode run: кандидатов %d, без улицы %d, запросов %d, улица+ %d, оставлен PSČ %d, ошибок %d dry=%v",
		rep.Candidates, rep.NoStreet, rep.Tried, rep.Upgraded, rep.Kept, rep.Errors, rep.Dry)
	if !rep.Dry && rep.Upgraded > 0 {
		if _, err := zones.Assign(ctx, pool, *country); err != nil {
			return fmt.Errorf("geocode run: zones assign: %w", err)
		}
	}
	return nil
}
