-- C6 (наблюдение из пяти коррекций 07.09): арсенал ЧИСЛОВЫХ шаблонов
-- площади (area_sqm) — шаг 2 цепочки значений начинает покрывать
-- зеркальную колонку. До C6 вклад rule_text в area_sqm = 0 (5697 markup +
-- 208 llm): схема «совпало → фиксированное значение» числа не брала, и
-- динамических шаблонов (value = NULL) в БД не было вовсе.
--
-- Паттерны — в стиле bazos/rules.go reAreaM2 + частые альтернативные
-- формы площадок (čtverečních, кириллица, sqm, m.kv). Capture —
-- именованная группа «val» (конвенция attrules; движок принимает и первую
-- позиционную в наследованных шаблонах). Движок применяет их ТОЛЬКО к
-- пустому зеркалу (enrich: der.Area == nil; rules run: area_sqm IS
-- NULL) — полученное значение не переписывается; markup-значение при
-- появлении детали всегда перетирает (COALESCE в saveObject).
-- Контекстный гвард движка отсекает «чужие» площади (участок/пристройка/
-- «celková plocha» перед числом); границы 5–5000 — как у Go-правил
-- Derive (bazos/rules.go areaRangeMin/Max).
--
-- confidence 0.80 = rule_confidence_description (конфиг): источник —
-- текст, не структурированная разметка.
INSERT INTO attribute_templates
    (country, attr_key, pattern_type, pattern, value, status, confidence, source, promoted_at)
VALUES
    ('CZ', 'area_sqm', 'regex',
     '(?P<val>[0-9][0-9\s]*(?:[,.][0-9]+)?)\s*(?:m[²2]|mkv\b|metry kvadratick\p{L}+)',
     NULL, 'active', 0.80, 'seed', now()),
    ('CZ', 'area_sqm', 'regex',
     '(?P<val>[0-9][0-9\s]*(?:[,.][0-9]+)?)\s*(?:m\p{L}*\s*)?čtverečn\p{L}+',
     NULL, 'active', 0.80, 'seed', now()),
    ('CZ', 'area_sqm', 'regex',
     '(?P<val>[0-9][0-9\s]*(?:[,.][0-9]+)?)\s*м[²2]',
     NULL, 'active', 0.80, 'seed', now()),
    ('CZ', 'area_sqm', 'regex',
     '(?P<val>[0-9][0-9\s]*(?:[,.][0-9]+)?)\s*sq\.?\s*m\b',
     NULL, 'active', 0.80, 'seed', now()),
    ('CZ', 'area_sqm', 'regex',
     '(?P<val>[0-9][0-9\s]*(?:[,.][0-9]+)?)\s*m\.\s*kv',
     NULL, 'active', 0.80, 'seed', now());
