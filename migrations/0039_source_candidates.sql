-- ТЗ-SRC-CZ-1 §4.1: реестр кандидатов площадок — единственный источник истины.
-- Класс и канал — закрытые перечни ТЗ §4.2 / §5 P5.
CREATE TABLE source_candidates (
    domain TEXT PRIMARY KEY,
    discovered_via TEXT NOT NULL,
    class TEXT NOT NULL DEFAULT 'pending'
        CHECK (class IN (
            'pending', 'direct', 'mirror', 'already_connected',
            'excluded_login', 'excluded_captcha', 'excluded_challenge',
            'excluded_robots', 'excluded_app_only', 'excluded_blocked',
            'excluded_no_data', 'agency_site', 'auction', 'dead'
        )),
    class_reason TEXT,
    evidence_path TEXT,
    probe_run_ids TEXT[] NOT NULL DEFAULT '{}',
    probed_at TIMESTAMPTZ,
    channel TEXT
        CHECK (channel IS NULL OR channel IN (
            'api_json', 'api_graphql', 'embedded_json', 'sitemap_feed', 'html'
        )),
    robots_ok BOOLEAN,
    reported_totals JSONB,
    mirror_of TEXT,
    tos_url TEXT,
    tos_excerpt TEXT,
    field_sample JSONB,
    delist_signature JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
