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

func TestRotateLocal(t *testing.T) {
	tmpDir := t.TempDir()

	// Создаём 5 дампов
	for i := 1; i <= 5; i++ {
		dumpName := filepath.Join(tmpDir, time.Date(2026, 11, i, 0, 0, 0, 0, time.UTC).Format("pb_20060102T150405Z.dump"))
		metaName := filepath.Join(tmpDir, time.Date(2026, 11, i, 0, 0, 0, 0, time.UTC).Format("pb_20060102T150405Z.meta.json"))
		_ = os.WriteFile(dumpName, []byte("dump"), 0644)
		_ = os.WriteFile(metaName, []byte("{}"), 0644)
	}

	// Оставляем 3
	rotateLocal(tmpDir, 3)

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	var dumps []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".dump" {
			dumps = append(dumps, e.Name())
		}
	}

	if len(dumps) != 3 {
		t.Errorf("expected 3 dumps after rotation, got %d", len(dumps))
	}
}
