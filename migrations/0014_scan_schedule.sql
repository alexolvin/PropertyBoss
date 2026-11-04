-- Окна сканирования (ТЗ §10.2). Часовой пояс — страны объявления, не сервера.
-- weight вычисляется по scan_yield, не назначается; max_requests_per_hour —
-- жёсткий потолок, который адаптация не имеет права превысить (ТЗ §10.4).
CREATE TABLE scan_windows (
    id BIGSERIAL PRIMARY KEY,
    source_id TEXT NOT NULL REFERENCES sources(id),
    country CHAR(2) NOT NULL,
    day_of_week SMALLINT NOT NULL CHECK (day_of_week BETWEEN 0 AND 6),
    hour_start SMALLINT NOT NULL CHECK (hour_start BETWEEN 0 AND 23),
    hour_end SMALLINT NOT NULL CHECK (hour_end BETWEEN 1 AND 24),
    timezone TEXT NOT NULL,
    weight NUMERIC(8,4) NOT NULL DEFAULT 1.0,
    max_requests_per_hour INTEGER NOT NULL CHECK (max_requests_per_hour > 0),
    UNIQUE (source_id, day_of_week, hour_start)
);

-- Статистика выхода сканов: новые объекты на скан по слоту (источник, час недели)
-- в часовом поясе страны (ТЗ §10.3).
CREATE TABLE scan_yield (
    id BIGSERIAL PRIMARY KEY,
    source_id TEXT NOT NULL REFERENCES sources(id),
    -- Слот в поясе страны: 'dow-hour', например '1-9' = понедельник, 9 часов
    slot_key TEXT NOT NULL,
    day DATE NOT NULL,
    scans INTEGER NOT NULL DEFAULT 0,
    new_objects INTEGER NOT NULL DEFAULT 0,
    UNIQUE (source_id, slot_key, day)
);
CREATE INDEX scan_yield_lookup_idx ON scan_yield (source_id, slot_key, day DESC);
