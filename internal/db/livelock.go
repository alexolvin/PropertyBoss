package db

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LiveLockKey — ключ advisory-LOCKS живых БД-тестов ("PB_LIVE").
// Все пакеты с live-тестами (PB_TEST_DSN) берут этот лок: go test
// гоняет бинари пакетов параллельно, а БД у них одна. Без лока
// sweep одного пакета разрушает глобальные подсчёты другого
// (например, Assign зон считает ВСЮ таблицу objects — гонка поймана
// на этапе 11: NoGeom съезжал от параллельных вставок скан-тестов).
const LiveLockKey int64 = 0x50424c495645

// LiveTestLock — берёт лок живых тестов на ПИННУТОМ соединении пула
// и возвращает функцию освобождения (в t.Cleanup, ДО регистрации
// sweep'а — LIFO: чистка работает под локом, лок отпущен последним).
//
// pg_advisory_lock сессионный: верни соединение в пул — и лок может
// отпуститься раньше, поэтому соединение держим до освобождения.
// Если процесс теста умирает, Postgres отпускает лок сам по
// закрытию соединения — вечной блокировки не остаётся.
func LiveTestLock(ctx context.Context, pool *pgxpool.Pool) (func(), error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("db: live lock: acquire: %w", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, LiveLockKey); err != nil {
		conn.Release()
		return nil, fmt.Errorf("db: live lock: %w", err)
	}
	return func() {
		uctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := conn.Exec(uctx, `SELECT pg_advisory_unlock($1)`, LiveLockKey); err != nil {
			log.Printf("db: live lock: pg_advisory_unlock: %v", err)
		}
		conn.Release()
	}, nil
}
