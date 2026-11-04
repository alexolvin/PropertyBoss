// run.go — БД-прогон локальных копий фотографий (ТЗ v4 §4.4),
// cron-точка `pb photos run`.
//
// Кандидаты — строки object_photos без локальной копии (file_card
// IS NULL), не помеченные download_error (NULL — не пробовали или
// успех) и с ord <= max_local. Ошибки делятся на постоянные
// (download_error — повтор бесполезен) и временные (не помечаются —
// повторится следующим прогоном).
package photos

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"propertyboss/internal/config"
)

// Report — результат прогона.
type Report struct {
	Objects    int `json:"objects"`    // объектов с кандидатами
	Candidates int `json:"candidates"` // строк-кандидатов
	Done       int `json:"done"`       // строк, получивших локальную копию
	Failed     int `json:"failed"`     // помечено download_error (постоянно)
	Transient  int `json:"transient"`  // временный сбой — повторится
}

// Run — бэкфилл локальных копий.
//
//	limit — сколько фото за прогон (0 — без лимита). Бэкфилл — десятки
//	тысяч файлов: темп задаёт вызывающий (--limit) + диск (ТЗ v4 §4.4:
//	лимиты — из конфига).
func Run(ctx context.Context, pool *pgxpool.Pool, cfg config.PhotosConf, limit int) (*Report, error) {
	rep := &Report{}
	thr := &throttle{interval: time.Duration(cfg.PageDelayMS) * time.Millisecond}

	q := `
SELECT object_id, ord, original_url
FROM object_photos
WHERE file_card IS NULL
  AND download_error IS NULL
  AND ord <= $1
ORDER BY object_id, ord`
	args := []any{cfg.MaxLocal}
	if limit > 0 {
		q += "\nLIMIT $" + fmt.Sprintf("%d", len(args)+1)
		args = append(args, limit)
	}
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("photos: кандидаты: %w", err)
	}
	defer rows.Close()

	var objID int64
	var jobs []Job
	// flush — обработать накопленные фото одного объекта (список
	// упорядочен по object_id — объект меняется редко).
	flush := func() error {
		if len(jobs) == 0 {
			return nil
		}
		rep.Objects++
		ph, err := ProcessObject(ctx, cfg, objID, jobs, thr)
		if err != nil {
			return fmt.Errorf("объект %d: %w", objID, err)
		}
		for _, p := range ph {
			switch {
			case p.FileCard != "" || p.FileList != "":
				if err := updateFiles(ctx, pool, objID, p); err != nil {
					return err
				}
				rep.Done++
			case p.Permanent:
				if _, err := pool.Exec(ctx, `
UPDATE object_photos SET download_error = $3
WHERE object_id = $1 AND ord = $2`, objID, p.Ord, p.Error); err != nil {
					return fmt.Errorf("download_error: %w", err)
				}
				rep.Failed++
			default:
				rep.Transient++ // не помечаем: повторится следующим прогоном
			}
		}
		jobs = nil
		return nil
	}

	for rows.Next() {
		var id int64
		var j Job
		if err := rows.Scan(&id, &j.Ord, &j.URL); err != nil {
			return rep, fmt.Errorf("photos: scan: %w", err)
		}
		rep.Candidates++
		if id != objID {
			if err := flush(); err != nil {
				return rep, err
			}
			objID = id
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return rep, fmt.Errorf("photos: кандидаты: %w", err)
	}
	if err := flush(); err != nil {
		return rep, err
	}
	return rep, nil
}

// updateFiles — сохранить локальные копии строки (то, что записалось;
// не записавшийся размер остаётся NULL).
func updateFiles(ctx context.Context, pool *pgxpool.Pool, objID int64, p Photo) error {
	var fc, fl *string
	var bc, bl *int
	if p.FileCard != "" {
		fc, bc = &p.FileCard, &p.BytesCard
	}
	if p.FileList != "" {
		fl, bl = &p.FileList, &p.BytesList
	}
	if _, err := pool.Exec(ctx, `
UPDATE object_photos
SET file_card = $3, bytes_card = $4, file_list = $5, bytes_list = $6
WHERE object_id = $1 AND ord = $2`, objID, p.Ord, fc, bc, fl, bl); err != nil {
		return fmt.Errorf("photos: строка %d/%d: %w", objID, p.Ord, err)
	}
	return nil
}
