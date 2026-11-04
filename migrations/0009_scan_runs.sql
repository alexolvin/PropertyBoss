-- Прогон сканера (ТЗ §8.2, §12).
-- completeness: partial/failed прогоны НЕ участвуют в вычислении исчезновений.
CREATE TABLE scan_runs (
    id BIGSERIAL PRIMARY KEY,
    source_id TEXT NOT NULL REFERENCES sources(id),
    search_config_id BIGINT NOT NULL REFERENCES search_configs(id),
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    completeness TEXT NOT NULL DEFAULT 'running' CHECK (completeness IN ('running', 'complete', 'partial', 'failed')),
    failure_kind TEXT CHECK (failure_kind IN ('captcha', 'http_429', 'layout_change', 'network')),
    listings_found INTEGER NOT NULL DEFAULT 0,
    new_objects INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX scan_runs_source_idx ON scan_runs (source_id, started_at DESC);
