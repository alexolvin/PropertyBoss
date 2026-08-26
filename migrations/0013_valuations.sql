-- Оценки отклонения цены (ТЗ §7.2, §7.3).
-- price_deviation = NULL допустим: тогда deviation_null_reason обязателен.
CREATE TABLE valuations (
    id BIGSERIAL PRIMARY KEY,
    object_id BIGINT NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
    model_version TEXT NOT NULL,
    price_deviation NUMERIC(8,5),
    deviation_null_reason TEXT,
    predicted_price_minor BIGINT,
    interval_low_minor BIGINT,
    interval_high_minor BIGINT,
    sample_size INTEGER NOT NULL,
    r_squared NUMERIC(6,4),
    zone_fallback BOOLEAN NOT NULL,
    computed_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX valuations_obj_idx ON valuations (object_id, computed_at DESC);

-- Оценки ликвидности (ТЗ §9): вероятность ухода с рынка за T дней.
-- hazard_probability = NULL допустим: тогда null_reason обязателен.
CREATE TABLE liquidity_estimates (
    object_id BIGINT NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
    horizon_days INTEGER NOT NULL,
    hazard_probability NUMERIC(6,5),
    null_reason TEXT,
    model_version TEXT NOT NULL,
    events_in_training INTEGER NOT NULL,
    computed_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (object_id, horizon_days)
);
