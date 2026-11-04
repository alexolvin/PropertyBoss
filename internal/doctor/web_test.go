package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEvalWeb — приёмка TZ UI v5 U04: файл в исходниках правят без
// пересборки — чек FAIL с точной причиной; после «пересборки» — OK.
// Тест обязан падать до реализации чека (evalWeb не существует).
//
// Сценарий изолирован в t.TempDir (реальный web/ не трогаем), mtime
// задаётся явно — детерминированно.
func TestEvalWeb(t *testing.T) {
	const head = "abc1234"
	root := t.TempDir()
	dist := filepath.Join(root, "web", "dist")
	src := filepath.Join(root, "web", "src")
	if err := os.MkdirAll(filepath.Join(dist, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}

	write := func(p, content string, at time.Time) {
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}

	buildTime := time.Date(2026, 9, 10, 1, 0, 0, 0, time.UTC)
	srcTime := buildTime.Add(-1 * time.Hour) // исходники старше сборки
	build := func(at time.Time, commit string) {
		write(filepath.Join(dist, "assets", "index.js"),
			`{"commit":"`+commit+`","builtAt":"x"}`, at)
		write(filepath.Join(dist, ".build-info.json"),
			`{"commit":"`+commit+`","built_at":"x"}`, at)
	}
	srcFile := filepath.Join(src, "main.tsx")
	write(srcFile, "export {}", srcTime)
	build(buildTime, head)

	// 1. Свежая раздача — OK.
	if c := evalWeb(dist, src, head, 0); c.Status != OK {
		t.Fatalf("свежая раздача: ожидаю OK, got %s: %s", c.Status, c.Detail)
	}

	// 2. Источники правят без пересборки — FAIL с точной причиной.
	touch := buildTime.Add(1 * time.Hour)
	write(srcFile, "export {} // изменено", touch)
	if c := evalWeb(dist, src, head, 0); c.Status != FAIL {
		t.Fatalf("src новее dist без пересборки: ожидаю FAIL, got %s: %s", c.Status, c.Detail)
	} else {
		want := dist + " older than " + src + ": newest source main.tsx is 1h0m0s newer"
		if c.Detail != want {
			t.Fatalf("точная причина:\n got: %s\nwant: %s", c.Detail, want)
		}
	}

	// 3. Допуск (порог из конфига): 1 ч < 2 ч — не нарушение.
	if c := evalWeb(dist, src, head, 2*time.Hour); c.Status != OK {
		t.Fatalf("grace 2ч > сдвиг 1ч: ожидаю OK, got %s: %s", c.Status, c.Detail)
	}

	// 4. «Пересборка» — dist снова новее — OK.
	rebuild := touch.Add(1 * time.Hour)
	build(rebuild, head)
	if c := evalWeb(dist, src, head, 0); c.Status != OK {
		t.Fatalf("после пересборки: ожидаю OK, got %s: %s", c.Status, c.Detail)
	}

	// 5. Хэш сборки ≠ HEAD — FAIL.
	build(rebuild.Add(time.Second), "deadbee")
	if c := evalWeb(dist, src, head, 0); c.Status != FAIL {
		t.Fatalf("хэш ≠ HEAD: ожидаю FAIL, got %s: %s", c.Status, c.Detail)
	} else if want := dist + ` built from commit deadbee, HEAD is ` + head; c.Detail != want {
		t.Fatalf("точная причина:\n got: %s\nwant: %s", c.Detail, want)
	}

	// 6. Бандла в dist нет — FAIL.
	build(rebuild, head)
	if err := os.Remove(filepath.Join(dist, "assets", "index.js")); err != nil {
		t.Fatal(err)
	}
	if c := evalWeb(dist, src, head, 0); c.Status != FAIL {
		t.Fatalf("пустой dist: ожидаю FAIL, got %s: %s", c.Status, c.Detail)
	} else if want := dist + ": no bundle (.js)"; c.Detail != want {
		t.Fatalf("точная причина:\n got: %s\nwant: %s", c.Detail, want)
	}

	// 7. .build-info.json нет — FAIL.
	write(filepath.Join(dist, "assets", "index.js"), `{"commit":"`+head+`"}`, rebuild)
	if err := os.Remove(filepath.Join(dist, ".build-info.json")); err != nil {
		t.Fatal(err)
	}
	if c := evalWeb(dist, src, head, 0); c.Status != FAIL {
		t.Fatalf("нет build-info: ожидаю FAIL, got %s: %s", c.Status, c.Detail)
	} else if want := dist + ": .build-info.json missing or unreadable"; c.Detail != want {
		t.Fatalf("точная причина:\n got: %s\nwant: %s", c.Detail, want)
	}

	// 8. dist отсутствует — FAIL.
	if c := evalWeb(dist+"-нет", src, head, 0); c.Status != FAIL {
		t.Fatalf("нет dist: ожидаю FAIL, got %s: %s", c.Status, c.Detail)
	} else if want := dist+"-нет is missing"; c.Detail != want {
		t.Fatalf("точная причина:\n got: %s\nwant: %s", c.Detail, want)
	}

	// 9. Имя чека — «web» (девятый именованный, TZ UI v5 U04).
	if c := evalWeb(dist, src, head, 0); c.Name != "web" {
		t.Fatalf("имя чека: ожидаю web, got %s", c.Name)
	}

	// 10. C33: суффикс -dirty — FAIL, раздача из некоммиченного дерева.
	build(rebuild, head+"-dirty")
	if c := evalWeb(dist, src, head, 0); c.Status != FAIL {
		t.Fatalf("-dirty: ожидаю FAIL, got %s: %s", c.Status, c.Detail)
	} else if !strings.Contains(c.Detail, "раздача собрана из некоммиченного дерева") {
		t.Fatalf("точная причина:\n got: %s\nwant: содержит %q", c.Detail, "раздача собрана из некоммиченного дерева")
	}
}
