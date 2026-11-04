-- Зоны ценовой статистики: полигоны, иерархия country → region → municipality → zone (ТЗ §7.1)
CREATE TABLE zones (
    id BIGSERIAL PRIMARY KEY,
    country CHAR(2) NOT NULL,
    level TEXT NOT NULL CHECK (level IN ('region', 'municipality', 'zone')),
    parent_id BIGINT REFERENCES zones(id),
    external_code TEXT,
    name TEXT NOT NULL,
    geom GEOGRAPHY(MULTIPOLYGON, 4326) NOT NULL,
    source TEXT NOT NULL
);
CREATE INDEX zones_geom_idx ON zones USING GIST (geom);
CREATE INDEX zones_parent_idx ON zones (parent_id);

-- Базовые уровни цены по зонам из внешних справочников (ТЗ §7.1).
-- data_kind: transaction | asking — смешивать без пометки запрещено (ТЗ §7.1).
CREATE TABLE zone_reference_prices (
    zone_id BIGINT NOT NULL REFERENCES zones(id),
    deal_type TEXT NOT NULL,
    property_type TEXT NOT NULL,
    price_min_minor BIGINT,
    price_max_minor BIGINT,
    currency CHAR(3) NOT NULL REFERENCES currencies(code),
    unit TEXT NOT NULL DEFAULT 'per_sqm',
    period_start DATE NOT NULL,
    data_kind TEXT NOT NULL CHECK (data_kind IN ('transaction', 'asking')),
    source TEXT NOT NULL,
    PRIMARY KEY (zone_id, deal_type, property_type, period_start, source)
);
