// pb photos — локальные копии фотографий объявлений (ТЗ v4 §4.4).
//
//	subcommands:
//
//	run [--limit N]   WebP-копии (два размера) первых max_local фото
//		каждого объекта; мёртвые ссылки помечаются download_error
//		и не ретраятся, временные сбои повторяются следующим прогоном
//
// ПРАВОВОЕ ОГРАНИЧЕНИЕ: копии — только для владельца системы, в личных
// целях (ТЗ v4 §4.4, ТЗ v2 §13).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"propertyboss/internal/config"
	"propertyboss/internal/db"
	"propertyboss/internal/photos"
)

func runPhotos(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("pb photos: нужен subcommand (run)")
	}
	switch args[0] {
	case "run":
		return runPhotosRun(ctx, cfg, args[1:])
	default:
		return fmt.Errorf("pb photos: неизвестный subcommand %q", args[0])
	}
}

func runPhotosRun(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("photos run", flag.ContinueOnError)
	limit := fs.Int("limit", 0, "сколько фотографий за прогон (дефолт: без лимита)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	rep, err := photos.Run(ctx, pool, cfg.Photos, *limit)
	if err != nil {
		return err
	}
	log.Printf("photos run: объектов %d, кандидатов %d, записано %d, мёртвых ссылок %d, временных сбоев %d (повторятся)",
		rep.Objects, rep.Candidates, rep.Done, rep.Failed, rep.Transient)
	return nil
}
