package doctor_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/config"
	"propertyboss/internal/doctor"
)

// TestLiveDoctor — слой инвариантов против реальной БД (PB_TEST_DSN).
//
// Состояние 10.09 / C36: источник bazos-reality в paused (решение
// оператора 09.09 об остановке сканирования сохраняется). lost и
// schedule:bazos-reality — INFO, не FAIL: намеренная пауза, дренажа
// не будет. HasFail = false. Чистые инварианты: mirrors 0/0/0 (C14),
// not_modeled ≤ 2% (C25).
func TestLiveDoctor(t *testing.T) {
	dsn := os.Getenv("PB_TEST_DSN")
	if dsn == "" {
		t.Skip("PB_TEST_DSN не задан — live-тест отключён")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// Поля Config без конфига: doctor использует только два скаляра.
	cfg := &config.Config{}
	cfg.Valuation.MaxNotModeledShare = 0.02 // C25: порог оператора
	cfg.Source.RequestIntervalMS = 2000     // D20: продемонстрированный темп

	rep, err := doctor.Run(ctx, pool, cfg, "CZ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(rep.String())

	status := map[string]doctor.Status{}
	for _, c := range rep.Checks {
		status[c.Name] = c.Status
	}
	expect := map[string]doctor.Status{
		"mirrors":                doctor.OK,   // C14: 0/0/0
		"not_modeled":            doctor.OK,   // доля ≤ 2% (C25)
		"lost":                   doctor.INFO, // C36: источники paused
		"schedule:bazos-reality": doctor.INFO, // C36: paused
	}
	for name, want := range expect {
		if got := status[name]; got != want {
			t.Errorf("%s: ожидается %s (производство на 10.09/C36), получено %s", name, want, got)
		}
	}
	if rep.HasFail() {
		t.Error("HasFail: ожидается false (paused — INFO, не FAIL)")
	}
}
