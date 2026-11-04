-- ТЗ-SRC-CZ-1 G2: источник bezrealitky (класс direct, проба 20260911T041041Z).
-- access_policy — факт пробы, не юридический вывод.
INSERT INTO sources (id, name, domain, country, deal_types, kind, access_policy, state, url_check_allowed)
VALUES (
    'bezrealitky',
    'Bezrealitky',
    'bezrealitky.cz',
    'CZ',
    ARRAY['sale', 'rent'],
    'simple',
    '{"channel":"embedded_json","robots_ok":true,"probe_run_id":"20260911T041041Z","tos_url":"https://www.bezrealitky.cz/informace/smluvni-podminky","paths":["/vypis/nabidka-prodej/byt","/vypis/nabidka-pronajem/byt"]}',
    'active',
    FALSE
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO search_configs (source_id, country, deal_type, property_type, active)
VALUES
    ('bezrealitky', 'CZ', 'sale', 'flat', TRUE),
    ('bezrealitky', 'CZ', 'sale', 'house', TRUE),
    ('bezrealitky', 'CZ', 'rent', 'flat', TRUE),
    ('bezrealitky', 'CZ', 'rent', 'house', TRUE);
