-- Версии модели ликвидности (ТЗ §9.4): метрики валидации сохраняются
-- вместе с версией модели. Таблица историческая: каждый прогон pb liquidity
-- добавляет строку; читается только последняя (по computed_at).
CREATE TABLE liquidity_models (
    id BIGSERIAL PRIMARY KEY,
    model_version TEXT NOT NULL,
    country CHAR(2) NOT NULL,
    deal_type TEXT NOT NULL,
    -- published — модель прошла порог калибровки, прогноз выдаётся;
    -- uncalibrated — обучилась, но калибровка хуже порога (ТЗ §9.4):
    --   прогноз NULL с причиной calibration_failed, оповещения не отправляются;
    -- insufficient_history — меньше min_events завершённых наблюдений (ТЗ §9.3):
    --   прогноз NULL с причиной insufficient_history.
    status TEXT NOT NULL CHECK (status IN ('published', 'uncalibrated', 'insufficient_history')),
    -- Причина непригодности при status != 'published' (детали:
    -- no_events_in_train, test_too_small, fit_not_converged и т.п.).
    reject_reason TEXT,
    horizon_days INTEGER NOT NULL,
    min_events INTEGER NOT NULL,
    -- Завершённых наблюдений (объектов, ушедших с рынка) в выборке.
    n_completed_events INTEGER NOT NULL,
    -- person-period строк (недельных интервалов) в выборке.
    n_person_periods INTEGER NOT NULL,
    n_params INTEGER,
    -- T: обучение — интервалы до T, проверка — после T (ТЗ §9.4:
    -- случайное разбиение запрещено).
    train_cutoff_at TIMESTAMPTZ,
    n_train INTEGER NOT NULL,
    n_test INTEGER NOT NULL,
    -- Калибровочная кривая по децилям предсказанной вероятности:
    -- [{decile, predicted, actual, n}, …] (ТЗ §9.4 — выводится в дашборде).
    calibration JSONB,
    max_calib_dev NUMERIC(6,4),
    brier_score NUMERIC(8,5),
    -- Разложение Brier: {reliability, resolution, uncertainty}.
    brier_decomp JSONB,
    c_index NUMERIC(6,4),
    -- Коэффициенты модели (name → coef) для аудита.
    params JSONB,
    computed_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX liquidity_models_latest_idx ON liquidity_models (country, deal_type, computed_at DESC);
