-- Конфигурации поиска: что сканировать и с какими фильтрами (ТЗ §2, этап 2).
-- Управляется из дашборда без правки кода.
CREATE TABLE search_configs (
    id BIGSERIAL PRIMARY KEY,
    source_id TEXT NOT NULL REFERENCES sources(id),
    country CHAR(2) NOT NULL,
    deal_type TEXT NOT NULL,
    property_type TEXT,
    -- Фильтры по ключам из attribute_registry + базовым полям
    filter_attributes JSONB NOT NULL DEFAULT '{}',
    min_area_sqm NUMERIC(10,2),
    max_area_sqm NUMERIC(10,2),
    min_price_minor BIGINT,
    max_price_minor BIGINT,
    currency CHAR(3) REFERENCES currencies(code),
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX search_configs_source_idx ON search_configs (source_id) WHERE active;
