package main

import (
	"context"
	"fmt"
	"log"

	"propertyboss/internal/config"
)

func runReport(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("report: укажите подкоманду: owner")
	}
	switch args[0] {
	case "owner":
		log.Printf("report: owner report (R4.4 stub)")
		return nil
	default:
		return fmt.Errorf("report: неизвестная подкоманда %q (ожидается owner)", args[0])
	}
}
