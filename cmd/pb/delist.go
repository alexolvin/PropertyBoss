package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"propertyboss/internal/config"
	"propertyboss/internal/db"
	"propertyboss/internal/scan"
)

// pb delist — прогон маркировки delisted (этап 6, ТЗ §8.2).
//
// По умолчанию — пасс по всем источникам, у которых есть привязанные
// объекты; -source ID — один источник. Итог пасса: без изменений
// (кандидатов нет), delisted с причиной, или АНОМАЛИЯ (доля
// исчезновений выше порога — изменения не применены, уведомление
// оператору в notifications). Автоматически присваиваются только
// причины 'unknown' и 'relisted' (ТЗ §8.2).
func runDelist(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("delist", flag.ExitOnError)
	sourceID := fs.String("source", "", "id источника (по умолчанию — все с привязанными объектами)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	var ids []string
	if *sourceID != "" {
		ids = []string{*sourceID}
	} else {
		rows, err := pool.Query(ctx, `
			SELECT DISTINCT source_id FROM object_listings ORDER BY source_id`)
		if err != nil {
			return fmt.Errorf("delist: источники: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	if len(ids) == 0 {
		log.Printf("delist: источников с привязанными объектами нет — делать нечего")
		return nil
	}
	var anyErr error
	for _, id := range ids {
		rep, err := scan.RunDelistPass(ctx, pool, cfg, id)
		if err != nil {
			log.Printf("delist: source=%s ОШИБКА: %v", id, err)
			anyErr = err
			continue
		}
		log.Printf("delist: source=%s active=%d candidates=%d delisted=%d url_alive=%d url_failed=%d",
			id, rep.Active, rep.Candidates, rep.Delisted, rep.URLAlive, rep.URLFailed)
		if rep.Anomaly {
			log.Printf("delist: source=%s АНОМАЛИЯ (ТЗ §8.2): доля исчезнувших %.1f%% (порог %v%%) — изменения не применены, записано уведомление оператору",
				id, rep.SharePct, cfg.Delist.MaxDelistedSharePct)
		}
	}
	return anyErr
}
