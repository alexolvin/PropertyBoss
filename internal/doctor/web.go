// Чек web — девятый именованный инвариант (TZ UI v5 U04).
//
// Класс дефекта E3: собранная раздача старше исходников — оператор
// смотрит старый интерфейс и не знает об этом. Однократная пересборка
// (U01) лечит симптом; инвариант лечит класс. Read-only, exit 1 при
// FAIL. Порог (допуск по mtime) — из конфига, как у остальных чеков.
package doctor

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"propertyboss/internal/config"
)

// buildInfo — метаданные сборки, которые пишет сборщик в dist
// (TZ UI v5 U02): тот же хэш, что встроен в бандл через vite define,
// один build — одно значение.
type buildInfo struct {
	Commit  string `json:"commit"`
	BuiltAt string `json:"built_at"`
}

// evalWeb — чистая оценка инварианта раздачи (без БД и git — хэш HEAD
// передаётся вызывающим, тесты детерминированны). FAIL, если:
//  1. каталог dist отсутствует (или бандла .js в нём нет);
//  2. метаданные сборки отсутствуют/не читаются;
//  3. хэш сборки ≠ HEAD;
//  4. dist старше любого файла в src сильнее допуска (grace).
func evalWeb(distDir, srcDir, headHash string, grace time.Duration) Check {
	fail := func(detail string) Check {
		return Check{Name: "web", Status: FAIL, Detail: detail}
	}

	fi, err := os.Stat(distDir)
	if err != nil || !fi.IsDir() {
		return fail(distDir + " is missing")
	}

	// Бандл есть, и самое свежее mtime в dist (время сборки по файлам).
	var distLatest time.Time
	var hasBundle bool
	err = filepath.WalkDir(distDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if strings.HasSuffix(d.Name(), ".js") {
			hasBundle = true
		}
		if info.ModTime().After(distLatest) {
			distLatest = info.ModTime()
		}
		return nil
	})
	if err != nil {
		return fail(distDir + ": " + err.Error())
	}
	if !hasBundle {
		return fail(distDir + ": no bundle (.js)")
	}

	raw, err := os.ReadFile(filepath.Join(distDir, ".build-info.json"))
	if err != nil {
		return fail(distDir + ": .build-info.json missing or unreadable")
	}
	var bi buildInfo
	if err := json.Unmarshal(raw, &bi); err != nil || bi.Commit == "" {
		return fail(distDir + ": .build-info.json missing or unreadable")
	}
	if strings.HasSuffix(bi.Commit, "-dirty") {
		return fail(fmt.Sprintf("раздача собрана из некоммиченного дерева (build %s)", bi.Commit))
	}
	if bi.Commit != headHash {
		return fail(fmt.Sprintf("%s built from commit %s, HEAD is %s", distDir, bi.Commit, headHash))
	}

	sfi, err := os.Stat(srcDir)
	if err != nil || !sfi.IsDir() {
		return fail(srcDir + " is missing")
	}
	var srcLatest time.Time
	var newestSrc string
	err = filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if info.ModTime().After(srcLatest) {
			srcLatest = info.ModTime()
			newestSrc = filepath.Base(p)
		}
		return nil
	})
	if err != nil {
		return fail(srcDir + ": " + err.Error())
	}
	if delta := srcLatest.Sub(distLatest); delta > grace {
		return fail(fmt.Sprintf("%s older than %s: newest source %s is %s newer",
			distDir, srcDir, newestSrc, delta))
	}

	return Check{Name: "web", Status: OK,
		Detail: fmt.Sprintf("build %s = HEAD, dist не старше исходников (grace %s)", headHash, grace)}
}

// gitShortHead — короткий хэш HEAD в каталоге dir (корень репозитория).
func gitShortHead(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// checkWeb — чек web для полного прогона doctor: конфиг → параметры,
// git → HEAD, evalWeb → чек в отчёте. Раздача не включена — INFO.
func checkWeb(rep *Report, cfg *config.Config) {
	if cfg.Dashboard.UI == "" {
		rep.add("web", INFO, "раздача не включена (dashboard.ui) — чек пропущен")
		return
	}
	head, err := gitShortHead(".")
	if err != nil {
		rep.add("web", FAIL, "git HEAD не читается: "+err.Error())
		return
	}
	c := evalWeb(cfg.Dashboard.UI, cfg.Dashboard.WebSrc, head,
		time.Duration(cfg.Dashboard.WebGraceSecs)*time.Second)
	rep.add(c.Name, c.Status, c.Detail)
}

// RunWeb — только чек web, без БД (`pb doctor web`, make ui).
func RunWeb(rootDir string, cfg *config.Config) (*Report, error) {
	rep := &Report{country: "web"}
	checkWeb(rep, cfg)
	return rep, nil
}
