// pb enrich — детальный проход по объектам (ТЗ v4 §4.1, §4.2, §4.5, §4.7).
//
//	subcommands:
//
//	run [--limit N] [--country XX]   объекты с detail_fetched_at IS NULL:
//		страница объявления → структурированные поля, тип продавца,
//		координаты, фото (ссылки), контроль цены
//
// Ошибка fetch/parse одного объекта не останавливает прогон и не
// помечает его обработанным — следующий прогон повторит. Частичный
// прогон — нормальный исход бэкфила.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"propertyboss/internal/config"
	"propertyboss/internal/db"
	"propertyboss/internal/enrich"
)

func runEnrich(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("pb enrich: нужен subcommand (run)")
	}
	switch args[0] {
	case "run":
		return runEnrichRun(ctx, cfg, args[1:])
	default:
		return fmt.Errorf("pb enrich: неизвестный subcommand %q", args[0])
	}
}

func runEnrichRun(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("enrich run", flag.ContinueOnError)
	limit := fs.Int("limit", 0, "сколько объектов за прогон (дефолт: без лимита)")
	country := fs.String("country", "", "один рынок (CZ|IT|NL); пусто — все")
	objID := fs.Int64("id", 0, "один объект (id в objects)")
	sourceID := fs.String("source", "", "только этот источник (ulovdomov|bazos-reality|…)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	rep, err := enrich.Run(ctx, pool, cfg, *limit, *country, *objID, *sourceID)
	if err != nil {
		return err
	}
	log.Printf("enrich run: кандидатов %d, проверено %d, записано %d, delisted %d, ошибок %d (повторятся), контрольные %d/%d живы, ошибок геокодера %d",
		rep.Objects, rep.Verified, rep.Done, rep.Delisted, rep.Failed,
		rep.ControlsAlive, rep.ControlsChecked, rep.GeocodeErr)
	if rep.RateLimited {
		log.Printf("enrich run: ОСТАНОВЛЕН: источник вернул HTTP 429 — в cooldown (ТЗ §10.4); необработанные кандидаты повторятся следующим прогоном, не ранее конца кулдауна")
	}
	if rep.CollectionFailed {
		log.Printf("enrich run: ОТКАЗ СБОРА: контрольные объявления тоже исчезли — прогон остановлен до обработки пула, delisting не применён (уведомление в notifications)")
	}
	if rep.DelistAnomaly {
		log.Printf("enrich run: АНОМАЛИЯ delisting'а — доля исчезнувших превысила порог, дальнейший delisting не применён (уведомление в notifications)")
	}
	log.Printf("enrich run: координаты %d, тип продавца %d, атрибутов (provenance) %d, фото %d, расхождений цены %d, LLM вызовов %d, LLM значений %d, LLM ошибок %d",
		rep.Geocoded, rep.SellerType, rep.Attrs, rep.Photos, rep.PriceDiff,
		rep.LLMCalls, rep.LLMValid, rep.LLMErr)
	return nil
}
