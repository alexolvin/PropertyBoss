-- Объекты (дедуплицированные объявления). Схема — ТЗ §12 + §14.5.1.
-- Деньги: BIGINT в минорных единицах + currency, float запрещён (ТЗ §5).
CREATE TABLE objects (
    id BIGSERIAL PRIMARY KEY,
    country CHAR(2) NOT NULL,
    deal_type TEXT NOT NULL,
    zone_id BIGINT REFERENCES zones(id),
    geom GEOGRAPHY(POINT, 4326),
    address TEXT,
    area_sqm NUMERIC(10,2),
    rooms SMALLINT,
    property_type TEXT,
    attributes JSONB NOT NULL DEFAULT '{}',
    attributes_unmapped JSONB NOT NULL DEFAULT '{}',
    current_price_minor BIGINT,
    currency CHAR(3) REFERENCES currencies(code),
    description_original TEXT,
    language_original CHAR(2),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'delisted')),
    delisted_reason TEXT CHECK (delisted_reason IN ('unknown', 'relisted', 'withdrawn_by_owner', 'scan_gap')),
    match_confidence TEXT NOT NULL DEFAULT 'high' CHECK (match_confidence IN ('high', 'low')),
    -- §14.5.1: дата размещения площадки ненадёжна (обновляется при редактировании).
    -- TRUE — объект исключается из обучающей выборки модели дожития.
    posted_date_unreliable BOOLEAN NOT NULL DEFAULT FALSE,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    delisted_at TIMESTAMPTZ
);
CREATE INDEX objects_geom_idx ON objects USING GIST (geom);
CREATE INDEX objects_attributes_idx ON objects USING GIN (attributes);
CREATE INDEX objects_status_idx ON objects (status);
CREATE INDEX objects_country_deal_idx ON objects (country, deal_type, status);
