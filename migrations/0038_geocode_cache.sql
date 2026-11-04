-- Кэш ответов геокодера (ТЗ v4 §4.5, политика Nominatim: один адрес —
-- один запрос). hit=false — адрес не резолвится, повтор в сеть не идёт.
-- 429/сеть в таблицу не пишутся.
CREATE TABLE geocode_cache (
    query_norm TEXT PRIMARY KEY,
    lat DOUBLE PRECISION,
    lng DOUBLE PRECISION,
    nominatim_type TEXT,
    hit BOOLEAN NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
