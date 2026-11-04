-- Связь объект ↔ объявления (результат дедупликации, ТЗ §8.1)
CREATE TABLE object_listings (
    object_id BIGINT NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
    raw_listing_id BIGINT NOT NULL REFERENCES raw_listings(id) ON DELETE CASCADE,
    source_id TEXT NOT NULL,
    external_id TEXT NOT NULL,
    match_method TEXT NOT NULL CHECK (match_method IN ('source_external', 'geo', 'address')),
    match_confidence TEXT NOT NULL DEFAULT 'high' CHECK (match_confidence IN ('high', 'low')),
    first_matched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (object_id, source_id, external_id)
);
CREATE INDEX object_listings_raw_idx ON object_listings (raw_listing_id);

-- История цен объекта (ТЗ §9.2: значения берутся на начало недельных интервалов)
CREATE TABLE price_history (
    id BIGSERIAL PRIMARY KEY,
    object_id BIGINT NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
    source_id TEXT NOT NULL,
    price_minor BIGINT NOT NULL,
    currency CHAR(3) NOT NULL REFERENCES currencies(code),
    -- Момент, когда цена стала известной системе (время полного скана).
    change_at TIMESTAMPTZ NOT NULL,
    UNIQUE (object_id, source_id, change_at)
);
CREATE INDEX price_history_obj_idx ON price_history (object_id, change_at DESC);
