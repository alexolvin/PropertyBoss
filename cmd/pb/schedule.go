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
	"propertyboss/internal/schedule"
)

// pb schedule — адаптивное расписание сканирования (этап 11, ТЗ §10).
//
// Подкоманды:
//
//	show          — состояние расписания: источники, кулдауны, веса
//	                окон (вычисленные по scan_yield), warming_up, бюджет
//	run           — выполнить следующий скан по расписанию (cron-точка:
//	                вызывается периодически, делает один скан или ничего)
//	init-windows  — создать окна сканирования источника (ТЗ §10.2)
func runSchedule(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) < 1 {
		return errors.New("schedule: нужна подкоманда: show | run | init-windows")
	}
	switch args[0] {
	case "show":
		return scheduleShow(ctx, cfg, args[1:])
	case "run":
		return scheduleRun(ctx, cfg, args[1:])
	case "init-windows":
		return scheduleInitWindows(ctx, cfg, args[1:])
	default:
		return fmt.Errorf("schedule: неизвестная подкоманда %q (show | run | init-windows)", args[0])
	}
}

// scheduleShow — человекочитаемое состояние расписания.
// warming_up выводится явно (ТЗ §10.5: ранняя адаптация не выдаётся за
// «настроенную по статистике»).
func scheduleShow(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("schedule show", flag.ExitOnError)
	fs.Parse(args)
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	s := schedule.SettingsFrom(cfg)
	now := time.Now()

	sources, err := schedule.LoadSourceStates(ctx, pool)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		fmt.Println("Источников нет.")
		return nil
	}
	for _, ss := range sources {
		fmt.Printf("Источник %s (%s): state=%s strikes=%d rate_factor=%.2f",
			ss.ID, ss.Country, ss.State, ss.Strikes, ss.RateFactor)
		if ss.CooldownUntil != nil {
			fmt.Printf(" cooldown до %s", ss.CooldownUntil.Format("2006-01-02 15:04"))
		}
		fmt.Println()
		if ss.State != "active" {
			continue
		}
		loc, terr := s.TZ(ss.Country)
		if terr != nil {
			fmt.Printf("  %v\n", terr)
			continue
		}
		windows, warming, werr := schedule.ComputeWeights(ctx, pool, s, ss.ID)
		if werr != nil {
			return werr
		}
		if len(windows) == 0 {
			fmt.Println("  окон нет — pb schedule init-windows -source " + ss.ID)
			continue
		}
		t := now.In(loc)
		fmt.Printf("  warming_up=%v (полных сканов накоплено порог %d)\n", warming, s.MinObsForTuning)
		for _, w := range windows {
			avg := "нет данных"
			if w.SlotAvg != nil {
				avg = fmt.Sprintf("%.2f/скан (n=%d)", *w.SlotAvg, w.SlotScans)
			}
			line := fmt.Sprintf("  окно dow=%d %02d:00–%02d:00 %s: weight=%.3f выход=%s",
				w.DOW, w.HourStart, w.HourEnd, w.Timezone, w.Weight, avg)
			if w.DOW == int(t.Weekday()) && t.Hour() >= w.HourStart && t.Hour() < w.HourEnd {
				already, aerr := schedule.ScansThisHour(ctx, pool, ss.ID, loc, now)
				if aerr != nil {
					return aerr
				}
				cap := schedule.EffectiveCap(w.MaxPerHour, ss.RateFactor)
				line += fmt.Sprintf(" [СЕЙЧАС: потолок=%d, в этом часе=%d, остаток=%d]",
					cap, already, cap-already)
			}
			fmt.Println(line)
		}
	}
	return nil
}

// scheduleRun — cron-точка (ТЗ §3.4: самодостаточная пакетная задача):
// выбрать следующий скан по расписанию и выполнить его (или ничего —
// если не время окна или исчерпан потолок). Один вызов — один скан.
func scheduleRun(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("schedule run", flag.ExitOnError)
	dry := fs.Bool("dry", false, "показать выбранный скан, не запуская его")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	s := schedule.SettingsFrom(cfg)

	opts, err := schedule.Plan(ctx, pool, s, time.Now())
	if err != nil {
		return err
	}
	if len(opts) == 0 {
		log.Printf("schedule run: сейчас нечего сканировать (не время окна либо исчерпан потолок max_requests_per_hour) — выход без действия")
		return nil
	}
	top := opts[0]
	log.Printf("schedule run: выбран %s (search-config %d, слот %s, weight=%.3f, остаток бюджета=%d, warming_up=%v)",
		top.SourceID, top.ConfigID, top.SlotKey, top.Weight, top.Remaining, top.WarmingUp)
	if *dry {
		return nil
	}
	return runScanOnce(ctx, cfg, pool, top.SourceID, top.ConfigID)
}

// scheduleInitWindows — создать окна сканирования для источника
// (ТЗ §10.2). Значения — решение оператора: время, когда сканирование
// допустимо/уместно, и жёсткий потолок на час. Окна одного дня недели
// не могут перекрываться (плотность запросов была бы неопределённой).
func scheduleInitWindows(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("schedule init-windows", flag.ExitOnError)
	source := fs.String("source", "", "id источника (обязательно)")
	tzFlag := fs.String("timezone", "", "IANA-пояс (по умолчанию — schedule.country_timezones страны источника)")
	dowFlag := fs.String("dow", "0-6", "дни недели, 0=вс..6=сб (ТЗ §10.2), например 1-5")
	hoursFlag := fs.String("hours", "0-24", "часы окна [старт,конец) в поясе страны, например 8-20")
	maxFlag := fs.Int("max", 4, "max_requests_per_hour — жёсткий потолок (ТЗ §10.4)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *source == "" {
		return errors.New("schedule init-windows: нужен -source")
	}
	dows, err := schedule.ParseSpec(*dowFlag, 0, 6)
	if err != nil {
		return fmt.Errorf("schedule init-windows: -dow: %w", err)
	}
	var hStart, hEnd int
	if _, err := fmt.Sscanf(*hoursFlag, "%d-%d", &hStart, &hEnd); err != nil {
		return fmt.Errorf("schedule init-windows: -hours должен быть вида «старт-конец» (0..24), задано %q", *hoursFlag)
	}
	if hStart < 0 || hEnd > 24 || hEnd <= hStart {
		return fmt.Errorf("schedule init-windows: -hours %q вне допустимого (0 <= старт < конец <= 24)", *hoursFlag)
	}
	if *maxFlag < 1 {
		return fmt.Errorf("schedule init-windows: -max должен быть >= 1, задано %d", *maxFlag)
	}

	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	s := schedule.SettingsFrom(cfg)

	var (
		country  string
		srcState string
	)
	err = pool.QueryRow(ctx,
		`SELECT country, state FROM sources WHERE id = $1`, *source,
	).Scan(&country, &srcState)
	if err != nil {
		return fmt.Errorf("schedule init-windows: источник %q: %w", *source, err)
	}
	tz := *tzFlag
	if tz == "" {
		tz = s.Timezones[country]
	}
	if tz == "" {
		tz = "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return fmt.Errorf("schedule init-windows: неизвестный IANA-пояс %q", tz)
	}

	// Пересечение окон одного дня недели — ошибка: плотность запросов
	// в перекрытии была бы неопределённой (потолок мин./вес макс.).
	rows, err := pool.Query(ctx,
		`SELECT day_of_week, hour_start, hour_end FROM scan_windows WHERE source_id = $1`, *source)
	if err != nil {
		return err
	}
	type span struct{ dow, lo, hi int }
	var existing []span
	for rows.Next() {
		var sp span
		if err := rows.Scan(&sp.dow, &sp.lo, &sp.hi); err != nil {
			rows.Close()
			return err
		}
		existing = append(existing, sp)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range dows {
		for _, sp := range existing {
			if sp.dow == d && hStart < sp.hi && sp.lo < hEnd {
				return fmt.Errorf("schedule init-windows: окно dow=%d %d–%d пересекается с существующим dow=%d %d–%d",
					d, hStart, hEnd, sp.dow, sp.lo, sp.hi)
			}
		}
	}

	created := 0
	for _, d := range dows {
		tag, err := pool.Exec(ctx, `
			INSERT INTO scan_windows (source_id, country, day_of_week, hour_start, hour_end, timezone, max_requests_per_hour)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (source_id, day_of_week, hour_start) DO NOTHING`,
			*source, country, d, hStart, hEnd, tz, *maxFlag)
		if err != nil {
			return err
		}
		created += int(tag.RowsAffected())
	}
	log.Printf("schedule init-windows: источник %s (state=%s): создано окон %d, всего дней %d, %02d:00–%02d:00 %s, max=%d/час",
		*source, srcState, created, len(dows), hStart, hEnd, tz, *maxFlag)
	return nil
}
