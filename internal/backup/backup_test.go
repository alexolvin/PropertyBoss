package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseDBName(t *testing.T) {
	tests := []struct {
		dsn      string
		expected string
	}{
		{"postgres://user:pass@localhost:5432/propertyboss?sslmode=disable", "propertyboss"},
		{"postgres://localhost/pb_restore_test", "pb_restore_test"},
		{"invalid-url", "propertyboss"},
	}

	for _, tt := range tests {
		got := parseDBName(tt.dsn)
		if got != tt.expected {
			t.Errorf("parseDBName(%q) = %q, want %q", tt.dsn, got, tt.expected)
		}
	}
}

func TestReplaceDBInDSN(t *testing.T) {
	dsn := "postgres://user:pass@localhost:5432/propertyboss?sslmode=disable"
	got := replaceDBInDSN(dsn, "pb_restore_test")
	want := "postgres://user:pass@localhost:5432/pb_restore_test?sslmode=disable"
	if got != want {
		t.Errorf("replaceDBInDSN() = %q, want %q", got, want)
	}
}

func TestRotateFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Создаём 5 файлов дампов и state
	for i := 1; i <= 5; i++ {
		dumpName := filepath.Join(tmpDir, time.Date(2026, 11, i, 0, 0, 0, 0, time.UTC).Format("pb_20060102T150405Z.dump"))
		metaName := filepath.Join(tmpDir, time.Date(2026, 11, i, 0, 0, 0, 0, time.UTC).Format("pb_20060102T150405Z.meta.json"))
		stateName := filepath.Join(tmpDir, time.Date(2026, 11, i, 0, 0, 0, 0, time.UTC).Format("state_20060102T150405Z.tar.zst"))
		_ = os.WriteFile(dumpName, []byte("dump"), 0644)
		_ = os.WriteFile(metaName, []byte("{}"), 0644)
		_ = os.WriteFile(stateName, []byte("state"), 0644)
	}

	// Оставляем 3
	rotateFiles(tmpDir, "pb_", ".dump", 3, true)
	rotateFiles(tmpDir, "state_", ".tar.zst", 3, false)

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	var dumps, states int
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".dump" {
			dumps++
		}
		if filepath.Ext(e.Name()) == ".zst" {
			states++
		}
	}

	if dumps != 3 {
		t.Errorf("expected 3 dumps, got %d", dumps)
	}
	if states != 3 {
		t.Errorf("expected 3 states, got %d", states)
	}
}
