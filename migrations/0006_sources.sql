-- Источники объявлений (ТЗ §13).
-- access_policy: результат проверки robots.txt / ToS / API с датой проверки.
CREATE TABLE sources (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    domain TEXT NOT NULL,
    country CHAR(2) NOT NULL,
    deal_types TEXT[] NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('simple', 'protected')),
    access_policy JSONB NOT NULL,
    available_filters JSONB,
    state TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'cooldown', 'disabled', 'blocked')),
    cooldown_until TIMESTAMPTZ
);
