package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"time"

	"propertyboss/internal/config"
	"propertyboss/internal/db"
	"propertyboss/internal/scan"
)

// pb scan — прогон сканера по одному источнику и конфигурации поиска (этап 3).
//
// Итог всегда фиксируется в scan_runs (complete/partial/failed с
// failure_kind, ТЗ §8.2.1) — код возврата 0 означает «скан выполнен и
// зафиксирован», а не «выдача была полной». Для автоматизации (этап 11)
// источником истины является таблица scan_runs, а не exit code.
func runScan(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	sourceID := fs.String("source", "", "id источника из таблицы sources (обязательно)")
	configID := fs.Int64("search-config", 0, "id конфигурации поиска (обязательно)")
	list := fs.Bool("list", false, "список зарегистрированных коннекторов")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *list {
		ids := scan.SourceIDs()
		if len(ids) == 0 {
			fmt.Println("Зарегистрированных коннекторов нет.")
			return nil
		}
		fmt.Println("Зарегистрированные коннекторы (id источника):")
		for _, id := range ids {
			fmt.Println("  " + id)
		}
		return nil
	}
	if *sourceID == "" || *configID == 0 {
		return errors.New("scan: нужны -source и -search-config")
	}

	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	conn, ok := scan.Get(*sourceID)
	if !ok {
		return fmt.Errorf("scan: для источника %q не зарегистрирован коннектор (pb scan -list)", *sourceID)
	}
	// Вежливость: пауза между запросами страниц (config: scan.page_delay_ms)
	// — у коннекторов, которые идут по страницам (не часть контракта
	// scan.Connector: у разных коннекторов разные параметры вежливости).
	if setter, ok := conn.(interface{ SetPageDelay(d time.Duration) }); ok {
		setter.SetPageDelay(cfg.ScanPageDelay())
	}
	sc, err := scan.LoadSearchConfig(ctx, pool, *configID)
	if err != nil {
		return err
	}
	if !sc.Active {
		return scan.ErrConfigInactive
	}
	if sc.SourceID != *sourceID {
		return fmt.Errorf("scan: конфигурация %d задана для источника %q, а не %q",
			sc.ID, sc.SourceID, *sourceID)
	}

	runner := scan.NewRunner(pool, cfg.Dedupe.ByCountry)
	rep := runner.Run(ctx, *sourceID, conn, sc)
	log.Printf("scan: run=%d source=%s completeness=%s listings=%d new_objects=%d",
		rep.RunID, rep.SourceID, rep.Completeness, rep.Listings, rep.NewObjects)
	if rep.FailureKind != "" {
		log.Printf("scan: run=%d failure_kind=%s", rep.RunID, rep.FailureKind)
	}
	if rep.Err != nil {
		return fmt.Errorf("scan: %v", rep.Err)
	}
	return nil
}
