package fx

import (
	"context"
	"os"
	"testing"
	"time"

	"propertyboss/internal/db"
)

// testDSN читает DSN тестовой БД из PB_TEST_DSN.
// Без переменной тест пропускается — `go test ./...` не требует живого Postgres.
// Регрессия: Load обязан писать именно переданный source, а не константу
// (первый запуск двухканальной синхронизации 2026-08-25 показал, что все
// строки frankfurter-канала были ошибочно атрибутированы «ECB eurofxref daily XML»).
func TestLoadWritesGivenSource(t *testing.T) {
	dsn := os.Getenv("PB_TEST_DSN")
	if dsn == "" {
		t.Skip("PB_TEST_DSN не задан — тест требует живого Postgres")
	}
	ctx := t.Context()
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("подключение к тестовой БД: %v", err)
	}
	unlock, err := db.LiveTestLock(ctx, pool)
	if err != nil {
		t.Fatalf("live lock: %v", err)
	}
	t.Cleanup(pool.Close)
	t.Cleanup(unlock)

	const (
		quote  = "CZK"
		date   = "2001-01-01" // дата вне реальных фидов; строка удаляется в Cleanup
		rate   = "1.005"
		source = "test-source-label"
	)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			"DELETE FROM fx_rates WHERE base = 'EUR' AND quote = $1 AND rate_date = $2",
			quote, date)
	})

	days := []DayRates{{
		Date:  time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC),
		Rates: map[string]string{quote: rate},
	}}
	loaded, skipped, err := Load(ctx, pool, days, source)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded != 1 {
		t.Fatalf("записано строк: %d, ожидалось 1", loaded)
	}
	if skipped != 0 {
		t.Fatalf("пропущено: %d, ожидалось 0", skipped)
	}

	// Сравниваем курс численно в SQL: Postgres сохраняет NUMERIC(20,10)
	// со шкалой колонки («1.005» отобразится как «1.0050000000» — это точно,
	// а не искажение). Атрибуцию источника проверяем по строке.
	var gotSource string
	var rateExact bool
	err = pool.QueryRow(ctx,
		"SELECT source, rate = $3::numeric FROM fx_rates WHERE base = 'EUR' AND quote = $1 AND rate_date = $2",
		quote, date, rate).Scan(&gotSource, &rateExact)
	if err != nil {
		t.Fatalf("чтение fx_rates: %v", err)
	}
	if gotSource != source {
		t.Errorf("source = %q, ожидалось %q — атрибуция источника нарушена", gotSource, source)
	}
	if !rateExact {
		t.Error("курс сохранён не точно (численное сравнение в SQL не совпало)")
	}
}
