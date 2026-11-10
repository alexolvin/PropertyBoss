// pb backup — управление резервными копиями и тест восстановления (ТЗ-R1 R0.3, R0.4).
//
// Использование:
//   pb backup run              создать дамп, проверить, сохранить локально и на приёмник
//   pb backup verify [--dump PATH]  восстановить дамп в pb_restore_test и сверить строки
package main

import (
	"context"
	"flag"
	"fmt"

	"propertyboss/internal/backup"
	"propertyboss/internal/config"
)

func runBackup(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("backup: укажите подкоманду: run | verify")
	}

	sub := args[0]
	subArgs := args[1:]

	switch sub {
	case "run":
		return backup.RunBackup(ctx, cfg)
	case "verify":
		fs := flag.NewFlagSet("backup verify", flag.ExitOnError)
		dumpPath := fs.String("dump", "", "путь к дампу (по умолчанию — последний в backup.dir)")
		if err := fs.Parse(subArgs); err != nil {
			return err
		}
		return backup.VerifyBackup(ctx, cfg, *dumpPath)
	default:
		return fmt.Errorf("backup: неизвестная подкоманда %q (ожидается run | verify)", sub)
	}
}
