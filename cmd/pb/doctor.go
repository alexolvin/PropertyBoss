// pb doctor — слой инвариантов над живой БД (волна 09.08).
//
//	pb doctor [-country CZ]
//
// Read-only: зеркала колонок, покрытие не-моделируемых, очередь
// enrich, сердечник сканера, очередь уведомлений, состояние модели,
// лимитер. Exit code: 0 — нет FAIL, 1 — нарушен хотя бы один
// инвариант. Пороги — из конфига (C23: не плавают).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"propertyboss/internal/config"
	"propertyboss/internal/db"
	"propertyboss/internal/doctor"
)

// pb doctor web — только инвариант раздачи (TZ UI v5 U04): без БД.
// Использует make ui после сборки: БД для этого чека не нужна, а
// exit code 1 на stale-раздаче останавливает выпуск (U03).
func runDoctorWeb(ctx context.Context, cfg *config.Config) error {
	_ = ctx
	rep, err := doctor.RunWeb(".", cfg)
	if err != nil {
		return err
	}
	fmt.Print(rep.String())
	if rep.HasFail() {
		log.Printf("doctor: есть FAIL — см. выше")
		return fmt.Errorf("doctor: нарушен инвариант")
	}
	return nil
}

func runDoctor(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	country := fs.String("country", "CZ", "рабочий корпус (property_country)")
	_ = fs.Bool("live", false, "слой над живой БД (поведение по умолчанию; алиас ТЗ-SRC-CZ-1 G0)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	rep, err := doctor.Run(ctx, pool, cfg, *country)
	if err != nil {
		return err
	}
	fmt.Print(rep.String())
	if rep.HasFail() {
		log.Printf("doctor: есть FAIL — см. выше")
		return fmt.Errorf("doctor: нарушен инвариант")
	}
	return nil
}
