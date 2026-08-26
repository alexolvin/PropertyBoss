package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Migrate применяет .sql-файлы из dir поимённо (порядок = лексикографический,
// отсюда префиксы 0001, 0002, …). Каждое применение — отдельная транзакция
// с записью в schema_migrations; повторный запуск идемпотентен.
func Migrate(ctx context.Context, pool *pgxpool.Pool, dir string) (applied []string, err error) {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return nil, fmt.Errorf("db: таблица schema_migrations: %w", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("db: каталог миграций %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		var exists bool
		err := pool.QueryRow(ctx,
			"SELECT 1 FROM schema_migrations WHERE name = $1", name).Scan(&exists)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return applied, fmt.Errorf("db: проверка %s: %w", name, err)
		}
		if err == nil && exists {
			continue // уже применено
		}

		sqlText, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return applied, fmt.Errorf("db: чтение %s: %w", name, err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return applied, fmt.Errorf("db: транзакция для %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(sqlText)); err != nil {
			tx.Rollback(ctx)
			return applied, fmt.Errorf("db: применение %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (name) VALUES ($1)", name); err != nil {
			tx.Rollback(ctx)
			return applied, fmt.Errorf("db: запись %s в schema_migrations: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return applied, fmt.Errorf("db: коммит %s: %w", name, err)
		}
		applied = append(applied, name)
	}
	return applied, nil
}
