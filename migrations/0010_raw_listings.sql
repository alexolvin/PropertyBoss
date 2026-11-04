-- Сырые наблюдения: что сканер увидел, неизменяемо.
-- Каждое срезное наблюдение хранится отдельно — отсюда price_history
-- и защита от ложных исчезновений (ТЗ §8.2, §9.2).
CREATE TABLE raw_listings (
    id BIGSERIAL PRIMARY KEY,
    scan_run_id BIGINT NOT NULL REFERENCES scan_runs(id),
    source_id TEXT NOT NULL,
    external_id TEXT NOT NULL,           -- идентификатор объявления на площадке
    source_url TEXT,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Нормализованные на момент получения поля
    price_minor BIGINT,
    currency CHAR(3) REFERENCES currencies(code),
    area_sqm NUMERIC(10,2),
    rooms SMALLINT,
    property_type TEXT,
    geom GEOGRAPHY(POINT, 4326),
    address TEXT,
    -- Дата размещения ПО ДАННЫМ ПЛОЩАДКИ. Ненадёжна: часть площадок
    -- обновляет её при любом редактировании (ТЗ §14.5.1).
    posted_at TIMESTAMPTZ,
    attributes JSONB NOT NULL DEFAULT '{}',
    description_original TEXT,
    UNIQUE (scan_run_id, source_id, external_id)
);
CREATE INDEX raw_listings_geom_idx ON raw_listings USING GIST (geom);
CREATE INDEX raw_listings_external_idx ON raw_listings (source_id, external_id, fetched_at DESC);
