package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"

	"propertyboss/internal/config"
	"propertyboss/internal/db"
	"propertyboss/internal/zones"
)

// pb zones — зоны (этап 4, ТЗ §7.1):
//
//	pb zones import -file Z.geojson -country IT -source "ИМЯ"
//	    импорт полигонов зон из GeoJSON (идемпотентно)
//	pb zones quotazioni -file Q.csv [-country IT] [-source "ИМЯ"]
//	    импорт котировок в zone_reference_prices (идемпотентно)
//	pb zones assign
//	    привязка объектов к зонам через PostGIS (идемпотентно)
//	pb zones link -country IT -level municipality
//	    parent_id по геометрии для источников без родительских ссылок
//	    (идемпотентно; родительский уровень должен быть импортирован)
//	pb zones list [-country XX] [-level L] [-limit N]
//	    просмотр зон (read-only, для ручной проверки)
func runZones(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) < 1 {
		return errors.New("zones: нужна подкоманда (import | quotazioni | assign | link | list)")
	}
	switch args[0] {
	case "import":
		return runZonesImport(ctx, cfg, args[1:])
	case "quotazioni":
		return runZonesQuotazioni(ctx, cfg, args[1:])
	case "assign":
		return runZonesAssign(ctx, cfg)
	case "link":
		return runZonesLink(ctx, cfg, args[1:])
	case "list":
		return runZonesList(ctx, cfg, args[1:])
	default:
		return fmt.Errorf("zones: неизвестная подкоманда %q (import | quotazioni | assign | link | list)", args[0])
	}
}

func runZonesImport(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("zones import", flag.ExitOnError)
	file := fs.String("file", "", "путь к GeoJSON-файлу (FeatureCollection, обязательно)")
	country := fs.String("country", "", "код страны из двух букв (обязательно)")
	source := fs.String("source", "", "источник данных (обязательно; атрибуция в UI, ТЗ §13)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" || *country == "" || *source == "" {
		return errors.New("zones import: нужны -file, -country, -source")
	}

	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	rep, err := zones.Import(ctx, pool, *file, *country, *source)
	if err != nil {
		return err
	}
	log.Printf("zones import: country=%s source=%q фичей=%d создано=%d обновлено=%d без кода=%d нерешено родителей=%d",
		*country, *source, rep.Features, rep.Inserted, rep.Updated, rep.NoCode, rep.Unresolved)
	if rep.NoCode > 0 {
		log.Printf("zones import: ВНИМАНИЕ: %d зон без external_code — при повторном импорте они будут созданы заново", rep.NoCode)
	}
	return nil
}

func runZonesQuotazioni(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("zones quotazioni", flag.ExitOnError)
	file := fs.String("file", "", "путь к CSV с котировками (обязательно)")
	country := fs.String("country", "IT", "код страны (по умолчанию IT — OMI)")
	source := fs.String("source", "", "источник (по умолчанию «"+zones.QuotSource+"»)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("zones quotazioni: нужен -file")
	}

	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	rep, err := zones.Quotazioni(ctx, pool, *file, *country, *source)
	if err != nil {
		return err
	}
	log.Printf("quotazioni: country=%s строк=%d создано=%d обновлено=%d NULL-цен=%d",
		*country, rep.Rows, rep.Inserted, rep.Updated, rep.NullPrices)
	return nil
}

func runZonesAssign(ctx context.Context, cfg *config.Config) error {
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	rep, err := zones.Assign(ctx, pool)
	if err != nil {
		return err
	}
	log.Printf("zones assign: с координатами=%d без координат=%d изменено=%d обнулено=%d вне зон=%d",
		rep.WithGeom, rep.NoGeom, rep.Changed, rep.Cleared, rep.Uncovered)
	return nil
}

func runZonesLink(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("zones link", flag.ExitOnError)
	country := fs.String("country", "", "код страны из двух букв (обязательно)")
	level := fs.String("level", "", "уровень, которому ставится parent: municipality | zone (обязательно)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *country == "" || *level == "" {
		return errors.New("zones link: нужны -country и -level")
	}

	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	rep, err := zones.LinkByGeometry(ctx, pool, *country, *level)
	if err != nil {
		return err
	}
	log.Printf("zones link: country=%s level=%s кандидатов=%d связано=%d без родителя=%d",
		*country, *level, rep.Candidates, rep.Linked, rep.Unlinked)
	return nil
}

func runZonesList(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("zones list", flag.ExitOnError)
	country := fs.String("country", "", "фильтр по стране (необязательно)")
	level := fs.String("level", "", "фильтр по уровню region|municipality|zone (необязательно)")
	limit := fs.Int("limit", 20, "сколько строк показать")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	var conds []string
	var filterArgs []any
	if *country != "" {
		conds = append(conds, fmt.Sprintf("z.country = $%d", len(filterArgs)+1))
		filterArgs = append(filterArgs, *country)
	}
	if *level != "" {
		if *level != "region" && *level != "municipality" && *level != "zone" {
			return errors.New("zones list: -level: region | municipality | zone")
		}
		conds = append(conds, fmt.Sprintf("z.level = $%d", len(filterArgs)+1))
		filterArgs = append(filterArgs, *level)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + conds[0]
		for _, c := range conds[1:] {
			where += " AND " + c
		}
	}

	var total int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM zones z"+where, filterArgs...).Scan(&total); err != nil {
		return err
	}
	listArgs := append(append([]any{}, filterArgs...), *limit)
	q := `
		SELECT z.id, z.country, z.level, z.name, COALESCE(z.external_code, ''),
		       COALESCE(p.name, ''), z.source
		FROM zones z LEFT JOIN zones p ON p.id = z.parent_id` + where +
		" ORDER BY z.country, z.level, z.name LIMIT $" + fmt.Sprint(len(listArgs))
	rows, err := pool.Query(ctx, q, listArgs...)
	if err != nil {
		return err
	}
	defer rows.Close()
	fmt.Printf("Зон: %d (показано до %d)\n", total, *limit)
	for rows.Next() {
		var id int64
		var c, lvl, name, code, parent, source string
		if err := rows.Scan(&id, &c, &lvl, &name, &code, &parent, &source); err != nil {
			return err
		}
		fmt.Printf("%6d  %s  %-13s %-28s code=%-14s parent=%-14s %s\n",
			id, c, lvl, truncate(name, 28), code, parent, source)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
