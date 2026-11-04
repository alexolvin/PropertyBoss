-- ulovdomov.cz: публичный JSON POST /v1/offer/find (перехват Playwright 2026-09-11).
-- robots.txt Allow: / (testdata ulovdomov.cz.robots.txt). Счётчик extraData.total.
INSERT INTO sources (id, name, domain, country, deal_types, kind, access_policy, state, url_check_allowed)
VALUES (
    'ulovdomov',
    'UlovDomov',
    'ulovdomov.cz',
    'CZ',
    ARRAY['sale', 'rent'],
    'simple',
    '{"channel":"api_json","endpoint":"https://ud.api.ulovdomov.cz/v1/offer/find","captured":"2026-09-11T06:50Z","robots_ok":true,"counter":"extraData.total"}',
    'active',
    FALSE
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO search_configs (source_id, country, deal_type, property_type, active)
VALUES
    ('ulovdomov', 'CZ', 'rent', 'flat', TRUE),
    ('ulovdomov', 'CZ', 'rent', 'house', TRUE),
    ('ulovdomov', 'CZ', 'sale', 'flat', TRUE),
    ('ulovdomov', 'CZ', 'sale', 'house', TRUE);
