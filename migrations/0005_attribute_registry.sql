-- Реестр национальных атрибутов: в БД, не в коде (ТЗ §6).
-- source_evidence — ссылка на страницу источника, где атрибут реально присутствует.
CREATE TABLE attribute_registry (
    id BIGSERIAL PRIMARY KEY,
    country CHAR(2) NOT NULL,
    key TEXT NOT NULL,
    data_type TEXT NOT NULL CHECK (data_type IN ('bool', 'enum', 'int', 'float')),
    allowed_values JSONB,
    used_in_pricing BOOLEAN NOT NULL DEFAULT FALSE,
    label_ru TEXT NOT NULL,
    label_en TEXT NOT NULL,
    source_evidence TEXT NOT NULL,
    UNIQUE (country, key)
);
