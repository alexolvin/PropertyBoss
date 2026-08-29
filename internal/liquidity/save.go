// save.go — запись результатов прогона: строка истории модели
// (liquidity_models) + прогнозы (liquidity_estimates). Вызывается из
// Run в рамках одной транзакции (ТЗ §3.4).
package liquidity

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// saveModelRow — строка в liquidity_models (история прогонов;
// читается только последняя по computed_at).
func saveModelRow(ctx context.Context, tx pgx.Tx, rep *RunReport, params map[string]float64) error {
	var calJSON, decompJSON, paramsJSON []byte
	if len(rep.Calibration) > 0 {
		b, err := json.Marshal(rep.Calibration)
		if err != nil {
			return fmt.Errorf("liquidity: serialization of calibration curve: %w", err)
		}
		calJSON = b
	}
	if rep.BrierDecomp != nil {
		d, err := json.Marshal(*rep.BrierDecomp)
		if err != nil {
			return fmt.Errorf("liquidity: serialization of Brier decomposition: %w", err)
		}
		decompJSON = d
	}
	if len(params) > 0 {
		p, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("liquidity: serialization of parameters: %w", err)
		}
		paramsJSON = p
	}
	var reject *string
	if rep.RejectReason != "" {
		reject = &rep.RejectReason
	}
	var nParams *int
	if rep.Params > 0 {
		nParams = &rep.Params
	}
	var cutoff *time.Time
	if !rep.TrainCutoff.IsZero() {
		cutoff = &rep.TrainCutoff
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO liquidity_models
			(model_version, country, deal_type, status, reject_reason,
			 horizon_days, min_events, n_completed_events, n_person_periods,
			 n_params, train_cutoff_at, n_train, n_test,
			 calibration, max_calib_dev, brier_score, brier_decomp, c_index,
			 params, computed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		rep.ModelVersion, rep.Country, rep.DealType, rep.Status, reject,
		rep.HorizonDays, rep.MinEvents, rep.CompletedEvents, rep.NPeriods,
		nParams, cutoff, rep.NTrain, rep.NTest,
		calJSON, rep.MaxCalibDev, rep.Brier, decompJSON, rep.CIndex,
		paramsJSON, rep.ComputedAt)
	return err
}

// upsertEstimates — прогнозы пачками по 100. Таблица не историческая:
// одна строка на (object, horizon), перезаписывается каждым прогоном.
func upsertEstimates(ctx context.Context, tx pgx.Tx, rep *RunReport, at time.Time, ests []estRow) (int, error) {
	if len(ests) == 0 {
		return 0, nil
	}
	const batchSize = 100
	written := 0
	const cols = "(object_id, horizon_days, hazard_probability, null_reason, model_version, events_in_training, computed_at)"
	for start := 0; start < len(ests); start += batchSize {
		end := start + batchSize
		if end > len(ests) {
			end = len(ests)
		}
		chunk := ests[start:end]
		ph := make([]string, len(chunk))
		args := make([]any, 0, len(chunk)*7)
		for i, e := range chunk {
			base := i * 7
			ph[i] = fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d)",
				base+1, base+2, base+3, base+4, base+5, base+6, base+7)
			var nr *string
			if e.reason != "" {
				nr = &e.reason
			}
			args = append(args, e.objectID, rep.HorizonDays, e.hazard,
				nr, rep.ModelVersion, rep.TrainEvents, at)
		}
		q := "INSERT INTO liquidity_estimates " + cols + " VALUES " +
			strings.Join(ph, ",") + `
		ON CONFLICT (object_id, horizon_days) DO UPDATE SET
			hazard_probability = EXCLUDED.hazard_probability,
			null_reason = EXCLUDED.null_reason,
			model_version = EXCLUDED.model_version,
			events_in_training = EXCLUDED.events_in_training,
			computed_at = EXCLUDED.computed_at`
		if _, err := tx.Exec(ctx, q, args...); err != nil {
			return written, err
		}
		written += len(chunk)
	}
	return written, nil
}
