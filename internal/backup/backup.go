// Package backup — создание резервных копий и верификация восстановления
// (ТЗ-R1 R0.3, R0.4).
package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"propertyboss/internal/config"
	"propertyboss/internal/notify"
)

// Meta — метаданные одного дампа. Хранятся рядом с дампом в <имя>.meta.json.
type Meta struct {
	DumpFile    string           `json:"dump_file"`
	SHA256      string           `json:"sha256"`
	CreatedAt   time.Time        `json:"created_at"`
	TableCounts map[string]int64 `json:"table_counts"`
	Extensions  []string         `json:"extensions"`
	SizeBytes   int64            `json:"size_bytes"`
}

// BackupState — статус последнего запуска pb backup run.
type BackupState struct {
	LastRun     time.Time `json:"last_run"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	DumpFile    string    `json:"dump_file,omitempty"`
	SHA256      string    `json:"sha256,omitempty"`
	SizeBytes   int64     `json:"size_bytes,omitempty"`
	Remote      string    `json:"remote,omitempty"`
}

// RestoreState — статус последнего запуска pb backup verify.
type RestoreState struct {
	LastRun     time.Time        `json:"last_run"`
	LastSuccess time.Time        `json:"last_success,omitempty"`
	LastError   string           `json:"last_error,omitempty"`
	DumpFile    string           `json:"dump_file"`
	Status      string           `json:"status"` // PASS или FAIL
	Counts      map[string]int64 `json:"counts,omitempty"`
	VerifiedAt  time.Time        `json:"verified_at,omitempty"`
}

var CoreTables = []string{
	"objects",
	"raw_listings",
	"object_listings",
	"attribute_values",
	"valuations",
	"object_photos",
}

func parseDBName(dsn string) string {
	u, err := url.Parse(dsn)
	if err == nil && u.Path != "" && u.Host != "" {
		p := strings.TrimPrefix(u.Path, "/")
		if p != "" {
			return p
		}
	}
	return "propertyboss"
}

// RunBackup выполняет полный цикл резервного копирования:
// 1. pg_dump -Fc в ~/backups/propertyboss/pb_<UTC>.dump с SHA-256.
// 2. Проверка pg_restore --list (ненулевой список).
// 3. Подсчёт строк в ключевых таблицах и список расширений (сохраняются в .meta.json).
// 4. Ротация локальных копий (14 последних).
// 5. rsync дампов и локальных фото на backup.remote.
// 6. Ротация копий на приёмнике (30 последних).
func RunBackup(ctx context.Context, cfg *config.Config) (err error) {
	dir := cfg.Backup.Dir
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("backup: создание каталога %s: %w", dir, err)
	}

	now := time.Now().UTC()
	timeStr := now.Format("20060102T150405Z")
	dumpName := fmt.Sprintf("pb_%s.dump", timeStr)
	dumpPath := filepath.Join(dir, dumpName)
	metaPath := filepath.Join(dir, fmt.Sprintf("pb_%s.meta.json", timeStr))
	tmpDumpPath := dumpPath + ".tmp"

	var shaHex string
	var sizeBytes int64

	defer func() {
		statePath := filepath.Join(dir, "backup_state.json")
		var st BackupState
		data, readErr := os.ReadFile(statePath)
		if readErr == nil {
			_ = json.Unmarshal(data, &st)
		}
		st.LastRun = now
		if err != nil {
			st.LastError = err.Error()
			_ = writeJSON(statePath, st)

			// Оповещение в Telegram вне очереди (ТЗ-R1 R0.3 п.3)
			if cfg.Telegram.Enabled && cfg.Telegram.Token != "" && cfg.Telegram.ChatID != "" {
				client := notify.Client{
					BaseURL: cfg.Telegram.BaseURL,
					Token:   cfg.Telegram.Token,
					ChatID:  cfg.Telegram.ChatID,
				}
				msg := fmt.Sprintf("🚨 *PropertyBoss*: сбой резервного копирования (pb backup run)\n\n"+
					"Ошибка: `%v`\n"+
					"Хост: vzu5-wall\n"+
					"Время UTC: %s", err, now.Format(time.RFC3339))
				_ = client.Send(context.Background(), msg)
			}
		} else {
			st.LastSuccess = now
			st.LastError = ""
			st.DumpFile = dumpName
			st.SHA256 = shaHex
			st.SizeBytes = sizeBytes
			st.Remote = cfg.Backup.Remote
			_ = writeJSON(statePath, st)
		}
	}()

	dbName := parseDBName(cfg.Database.DSN)
	log.Printf("backup: старт pg_dump БД %s в %s...", dbName, dumpPath)

	cmdDump := exec.CommandContext(ctx, "pg_dump", "-Fc", "-d", dbName, "-f", tmpDumpPath)
	cmdDump.Env = os.Environ()
	if out, dumpErr := cmdDump.CombinedOutput(); dumpErr != nil {
		_ = os.Remove(tmpDumpPath)
		return fmt.Errorf("backup: pg_dump завершился ошибкой: %w (вывод: %s)", dumpErr, string(out))
	}

	if err := os.Rename(tmpDumpPath, dumpPath); err != nil {
		return fmt.Errorf("backup: rename %s -> %s: %w", tmpDumpPath, dumpPath, err)
	}

	fi, err := os.Stat(dumpPath)
	if err != nil {
		return fmt.Errorf("backup: stat %s: %w", dumpPath, err)
	}
	sizeBytes = fi.Size()

	// Вычисление SHA-256
	h := sha256.New()
	f, err := os.Open(dumpPath)
	if err != nil {
		return fmt.Errorf("backup: чтение %s: %w", dumpPath, err)
	}
	if _, err := io.Copy(h, f); err != nil {
		f.Close()
		return fmt.Errorf("backup: sha256 %s: %w", dumpPath, err)
	}
	f.Close()
	shaHex = hex.EncodeToString(h.Sum(nil))

	// Проверка pg_restore --list
	cmdList := exec.CommandContext(ctx, "pg_restore", "-l", dumpPath)
	outList, err := cmdList.CombinedOutput()
	if err != nil {
		return fmt.Errorf("backup: pg_restore -l %s: %w (вывод: %s)", dumpPath, err, string(outList))
	}
	lines := strings.Split(strings.TrimSpace(string(outList)), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return fmt.Errorf("backup: pg_restore -l вернул пустой список элементов")
	}

	// Снятие счётчиков таблиц и списка расширений из исходной БД
	tableCounts, extensions, err := inspectDatabase(ctx, cfg.Database.DSN)
	if err != nil {
		return fmt.Errorf("backup: инспекция исходной БД: %w", err)
	}

	meta := Meta{
		DumpFile:    dumpName,
		SHA256:      shaHex,
		CreatedAt:   now,
		TableCounts: tableCounts,
		Extensions:  extensions,
		SizeBytes:   sizeBytes,
	}
	if err := writeJSON(metaPath, meta); err != nil {
		return fmt.Errorf("backup: запись метаданных %s: %w", metaPath, err)
	}
	log.Printf("backup: дамп готов, размер %d байт, SHA-256 %s", sizeBytes, shaHex)

	// Ротация локальных копий (14 штук)
	rotateLocal(dir, cfg.Backup.KeepLocal)

	// Rsync на backup.remote
	if cfg.Backup.Remote != "" {
		if err := syncRemote(ctx, cfg, dir); err != nil {
			return fmt.Errorf("backup: rsync на %s: %w", cfg.Backup.Remote, err)
		}
	}

	log.Printf("backup: резервное копирование завершено успешно")
	return nil
}

func inspectDatabase(ctx context.Context, dsn string) (map[string]int64, []string, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close(ctx)

	counts := make(map[string]int64)
	for _, tbl := range CoreTables {
		var cnt int64
		q := fmt.Sprintf("SELECT count(*) FROM %s", tbl)
		if err := conn.QueryRow(ctx, q).Scan(&cnt); err != nil {
			return nil, nil, fmt.Errorf("count %s: %w", tbl, err)
		}
		counts[tbl] = cnt
	}

	rows, err := conn.Query(ctx, "SELECT extname FROM pg_extension ORDER BY extname")
	if err != nil {
		return nil, nil, fmt.Errorf("pg_extension: %w", err)
	}
	defer rows.Close()

	var exts []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, nil, err
		}
		exts = append(exts, name)
	}
	return counts, exts, nil
}

func rotateLocal(dir string, keep int) {
	if keep <= 0 {
		keep = 14
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	var dumps []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "pb_") && strings.HasSuffix(e.Name(), ".dump") {
			dumps = append(dumps, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dumps)))

	if len(dumps) > keep {
		for _, oldDump := range dumps[keep:] {
			dumpFile := filepath.Join(dir, oldDump)
			metaFile := filepath.Join(dir, strings.TrimSuffix(oldDump, ".dump")+".meta.json")
			_ = os.Remove(dumpFile)
			_ = os.Remove(metaFile)
			log.Printf("backup: ротация локального дампа %s", oldDump)
		}
	}
}

func syncRemote(ctx context.Context, cfg *config.Config, localDir string) error {
	remote := cfg.Backup.Remote
	sshKey := cfg.Backup.SSHKey

	parts := strings.SplitN(remote, ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("неверный формат backup.remote (ожидается user@host:path): %s", remote)
	}
	remoteHost, remotePath := parts[0], parts[1]

	// 1. Создать удалённые каталоги
	sshCmd := fmt.Sprintf("mkdir -p %s/dumps %s/photos", remotePath, remotePath)
	cmdMkdir := exec.CommandContext(ctx, "ssh", "-i", sshKey, "-o", "BatchMode=yes", remoteHost, sshCmd)
	if out, err := cmdMkdir.CombinedOutput(); err != nil {
		return fmt.Errorf("ssh mkdir на %s: %w (вывод: %s)", remoteHost, err, string(out))
	}

	// 2. rsync дампов
	sshOpt := fmt.Sprintf("ssh -i %s -o BatchMode=yes", sshKey)
	cmdRsyncDumps := exec.CommandContext(ctx, "rsync", "-avz", "--delete", "-e", sshOpt,
		localDir+"/", fmt.Sprintf("%s:%s/dumps/", remoteHost, remotePath))
	if out, err := cmdRsyncDumps.CombinedOutput(); err != nil {
		return fmt.Errorf("rsync дампов: %w (вывод: %s)", err, string(out))
	}

	// 3. rsync фото (если каталог существует)
	photosDir := cfg.Photos.Dir
	if photosDir != "" {
		if fi, err := os.Stat(photosDir); err == nil && fi.IsDir() {
			cmdRsyncPhotos := exec.CommandContext(ctx, "rsync", "-avz", "-e", sshOpt,
				photosDir+"/", fmt.Sprintf("%s:%s/photos/", remoteHost, remotePath))
			if out, err := cmdRsyncPhotos.CombinedOutput(); err != nil {
				return fmt.Errorf("rsync фото: %w (вывод: %s)", err, string(out))
			}
		}
	}

	// 4. Ротация на приёмнике (хранить 30 копий)
	keepRemote := cfg.Backup.KeepRemote
	if keepRemote <= 0 {
		keepRemote = 30
	}
	remoteRotateCmd := fmt.Sprintf("cd %s/dumps && ls -1t pb_*.dump 2>/dev/null | tail -n +%d | while read f; do rm -f \"$f\" \"${f%%.dump}.meta.json\"; done",
		remotePath, keepRemote+1)
	cmdRotate := exec.CommandContext(ctx, "ssh", "-i", sshKey, "-o", "BatchMode=yes", remoteHost, remoteRotateCmd)
	if out, err := cmdRotate.CombinedOutput(); err != nil {
		return fmt.Errorf("ssh ротация на %s: %w (вывод: %s)", remoteHost, err, string(out))
	}

	log.Printf("backup: rsync дампов на %s завершён", remote)
	return nil
}

// VerifyBackup проверяет дамп восстановлением в тестовую БД pb_restore_test:
// 1. Сравнивает расширения исходной БД и тестовой БД (несовпадение -> FAIL).
// 2. Восстанавливает дамп по списку без EXTENSION (pg_restore -l | grep -v EXTENSION > list).
// 3. Сравнивает count(*) по 6 ключевым таблицам с числами из .meta.json.
func VerifyBackup(ctx context.Context, cfg *config.Config, dumpPath string) (err error) {
	dir := cfg.Backup.Dir
	testDB := cfg.Backup.TestDB
	if testDB == "" {
		testDB = "pb_restore_test"
	}

	if dumpPath == "" {
		// Найти самый свежий дамп в dir
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("verify: чтение %s: %w", dir, err)
		}
		var dumps []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasPrefix(e.Name(), "pb_") && strings.HasSuffix(e.Name(), ".dump") {
				dumps = append(dumps, e.Name())
			}
		}
		if len(dumps) == 0 {
			return fmt.Errorf("verify: в каталоге %s нет файлов pb_*.dump", dir)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(dumps)))
		dumpPath = filepath.Join(dir, dumps[0])
	}

	metaPath := strings.TrimSuffix(dumpPath, ".dump") + ".meta.json"
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		return fmt.Errorf("verify: чтение метаданных %s: %w", metaPath, err)
	}
	var meta Meta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		return fmt.Errorf("verify: разбор %s: %w", metaPath, err)
	}

	now := time.Now().UTC()
	var verifiedCounts map[string]int64

	defer func() {
		statePath := filepath.Join(dir, "restore_state.json")
		st := RestoreState{
			LastRun:  now,
			DumpFile: filepath.Base(dumpPath),
		}
		if err != nil {
			st.LastError = err.Error()
			st.Status = "FAIL"
		} else {
			st.LastSuccess = now
			st.Status = "PASS"
			st.Counts = verifiedCounts
			st.VerifiedAt = now
		}
		_ = writeJSON(statePath, st)
	}()

	log.Printf("verify: проверка дампа %s (создан %s, SHA-256 %.12s...)",
		filepath.Base(dumpPath), meta.CreatedAt.Format(time.RFC3339), meta.SHA256)

	// 1. Сверка расширений: pg_extension исходной БД против тестовой БД
	sourceDBName := parseDBName(cfg.Database.DSN)
	extSource, err := queryExtensions(ctx, cfg.Database.DSN)
	if err != nil {
		return fmt.Errorf("verify: получение расширений исходной БД %s: %w", sourceDBName, err)
	}

	testDSN := replaceDBInDSN(cfg.Database.DSN, testDB)
	extTest, err := queryExtensions(ctx, testDSN)
	if err != nil {
		return fmt.Errorf("verify: получение расширений тестовой БД %s: %w", testDB, err)
	}

	testExtMap := make(map[string]bool)
	for _, e := range extTest {
		testExtMap[e] = true
	}
	var missingExts []string
	for _, e := range extSource {
		if !testExtMap[e] {
			missingExts = append(missingExts, e)
		}
	}
	if len(missingExts) > 0 {
		return fmt.Errorf("verify: FAIL: в тестовой БД %s отсутствуют расширения: %s",
			testDB, strings.Join(missingExts, ", "))
	}

	// 2. Формирование списка объектов pg_restore без EXTENSION
	cmdList := exec.CommandContext(ctx, "pg_restore", "-l", dumpPath)
	outList, err := cmdList.CombinedOutput()
	if err != nil {
		return fmt.Errorf("verify: pg_restore -l: %w (вывод: %s)", err, string(outList))
	}

	var filteredList bytes.Buffer
	for _, line := range strings.Split(string(outList), "\n") {
		if !strings.Contains(line, "EXTENSION") {
			filteredList.WriteString(line + "\n")
		}
	}

	tmpListFile, err := os.CreateTemp("", "restore_list_*.lst")
	if err != nil {
		return fmt.Errorf("verify: создание временного файла списка: %w", err)
	}
	defer os.Remove(tmpListFile.Name())
	if _, err := tmpListFile.Write(filteredList.Bytes()); err != nil {
		tmpListFile.Close()
		return fmt.Errorf("verify: запись временного списка: %w", err)
	}
	tmpListFile.Close()

	// 3. Восстановление в тестовую БД
	log.Printf("verify: восстановление в БД %s...", testDB)
	cmdRestore := exec.CommandContext(ctx, "pg_restore", "-L", tmpListFile.Name(), "--clean", "--if-exists", "-d", testDB, dumpPath)
	if out, err := cmdRestore.CombinedOutput(); err != nil {
		return fmt.Errorf("verify: pg_restore в %s завершился ошибкой: %w (вывод: %s)", testDB, err, string(out))
	}

	// 4. Подсчёт строк в тестовой БД и сравнение с meta.TableCounts
	testConn, err := pgx.Connect(ctx, testDSN)
	if err != nil {
		return fmt.Errorf("verify: подключение к %s: %w", testDB, err)
	}
	defer testConn.Close(ctx)

	verifiedCounts = make(map[string]int64)
	var mismatches []string

	fmt.Printf("\n%-20s %-12s %-12s %-10s\n", "Таблица", "В дампе", "В pb_restore_test", "Статус")
	fmt.Printf("%s\n", strings.Repeat("-", 60))

	for _, tbl := range CoreTables {
		var cnt int64
		q := fmt.Sprintf("SELECT count(*) FROM %s", tbl)
		if err := testConn.QueryRow(ctx, q).Scan(&cnt); err != nil {
			return fmt.Errorf("verify: count %s в %s: %w", tbl, testDB, err)
		}
		verifiedCounts[tbl] = cnt
		expected := meta.TableCounts[tbl]

		st := "PASS"
		if cnt != expected {
			st = "FAIL"
			mismatches = append(mismatches, fmt.Sprintf("%s: ожидалось %d, получено %d", tbl, expected, cnt))
		}
		fmt.Printf("%-20s %-12d %-12d %-10s\n", tbl, expected, cnt, st)
	}
	fmt.Println()

	if len(mismatches) > 0 {
		return fmt.Errorf("verify: расхождение данных: %s", strings.Join(mismatches, "; "))
	}

	log.Printf("verify: тест восстановления успешно завершён, все таблицы совпадают (PASS)")
	return nil
}

func queryExtensions(ctx context.Context, dsn string) ([]string, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)

	rows, err := conn.Query(ctx, "SELECT extname FROM pg_extension ORDER BY extname")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var exts []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		exts = append(exts, name)
	}
	return exts, nil
}

func replaceDBInDSN(dsn, newDB string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	u.Path = "/" + newDB
	return u.String()
}

func writeJSON(path string, val any) error {
	data, err := json.MarshalIndent(val, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}
