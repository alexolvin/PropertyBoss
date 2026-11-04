-- TZ UI v5, Волна 3 (U26, U28): индексы сортировки списка и поиска по адресу.
-- pg_trgm добавляется: в 0001 только postgis.
-- Обоснование каждого индекса (EXPLAIN-замеры на живом корпусе) — в отчёте
-- Волны 3. Деньги — BIGINT минорные, float не вводится (ТЗ §5).
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- U26: дефолтная сортировка date_desc = first_seen DESC, id DESC.
-- Индекс накрывает WHERE country+status и ORDER BY сортировкой не только —
-- index scan. (country, deal_type, status) — другой порядок колонок,
-- запрос country+status его для сортировки не использует.
CREATE INDEX objects_sort_date_idx
    ON objects (country, status, first_seen_at DESC, id DESC);

-- U26: сортировка по цене. По умолчанию ASC имеет NULLS LAST — покрытие
-- price_asc; price_desc — in-memory-сортировка отфильтрованного множества
-- (замер — отчёт Волны 3).
CREATE INDEX objects_sort_price_idx
    ON objects (country, status, current_price_minor);

-- U26: ppsm (цена/м²) — выражение. NULLIF(area_sqm, 0): площадь 0 или
-- отсутствует → NULL → NULLS LAST. Выражение в ORDER BY идентично.
CREATE INDEX objects_sort_ppsm_idx
    ON objects (country, status, ((current_price_minor / NULLIF(area_sqm, 0))));

-- U28: регистронезависимый поиск по адресу — GIN pg_trgm по lower(address).
CREATE INDEX objects_address_trgm_idx
    ON objects USING GIN (lower(address) gin_trgm_ops);
