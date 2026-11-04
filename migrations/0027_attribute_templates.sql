-- TZ v4 §4.2 (шаг 2 цепочки значений) + директива гибридного извлечения:
-- программный парсинг стандартных фраз — быстрым путём (без LLM), арсенал
-- шаблонов — данные в БД, а не хардкод: растёт без пересборки.
--
-- Шаблон = (country, attr_key, pattern, value). Применяется только к
-- атрибутам, которые шаги 1–2 (source_field + Go-правила bazos.Derive)
-- не заполнили; area_sqm/rooms исключены движком (зеркальные колонки).
--
-- pattern_type:
--   regex   — RE2 (стиль internal/connectors/bazos/rules.go); один
--             опциональный числовой capture (группа 1) → динамическое
--             значение (ParseCZNum); без capture — value из строки;
--   literal — дословная фраза (case-insensitive, граница: символ до начала
--             не [A-Za-z0-9]); создаётся ТОЛЬКО минингом из LLM-успехов
--             с дословным доказательством (evidence).
--
-- status:
--   candidate — выучен, накапливает повторения; не применяется;
--   active    — применяется движком;
--   conflict  — встретилось другое значение при том же паттерне;
--               авто-промоушн невозможен, решение за оператором;
--   disabled  — отключён вручную.
--
-- Авто-промоушн candidate → active: occurrences >= rules.min_mined_occurrences
-- и conflicts = 0 (порог — конфиг, не код). Seed-шаблоны сразу active.
CREATE TABLE attribute_templates (
    id BIGSERIAL PRIMARY KEY,
    country CHAR(2) NOT NULL,
    attr_key TEXT NOT NULL,
    pattern_type TEXT NOT NULL CHECK (pattern_type IN ('regex', 'literal')),
    pattern TEXT NOT NULL,
    value JSONB,                      -- NULL — динамическое (числовой capture)
    status TEXT NOT NULL DEFAULT 'candidate'
        CHECK (status IN ('active', 'candidate', 'conflict', 'disabled')),
    confidence NUMERIC(4,3) NOT NULL CHECK (confidence > 0 AND confidence <= 1),
    source TEXT NOT NULL CHECK (source IN ('seed', 'llm_mined')),
    evidence TEXT,                    -- llm_mined: дословная цитата-доказательство
    occurrences INT NOT NULL DEFAULT 0,
    conflicts INT NOT NULL DEFAULT 0,
    example_object_ids BIGINT[],      -- до rules.example_objects id для ревизии
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    promoted_at TIMESTAMPTZ,
    UNIQUE (country, attr_key, pattern)
);
CREATE INDEX attribute_templates_apply_idx
    ON attribute_templates (country, status, id);
CREATE INDEX attribute_templates_mining_idx
    ON attribute_templates (country, attr_key, pattern);

COMMENT ON TABLE attribute_templates IS
    'Арсенал программных шаблонов атрибутов (ТЗ v4 §4.2 шаг 2): seed-регуляры '
    'и паттерны, выученные из LLM-извлечений (source=llm_mined, evidence). '
    'Порядок применения = порядок id (гварды-отрицания ставятся раньше утверждений).';

-- Seed-шаблоны CZ: атрибуты, у которых программных правил не было вовсе
-- (has_gas, panel_renovation — оба used_in_pricing=true). Стиль rules.go:
-- гвард-отрицание раньше утверждения; диакритика — классами [еé]/[íi].
-- confidence 0.80 = detail.rule_confidence_description (свободный текст).
INSERT INTO attribute_templates
    (country, attr_key, pattern_type, pattern, value, status, confidence, source)
VALUES
    -- has_gas: отрицание («bez plynu», «plyn není přiveden») раньше утверждения.
    ('CZ', 'has_gas', 'regex', '(?i)bez\s+plyn\p{L}*', 'false'::jsonb,
     'active', 0.80, 'seed'),
    -- Без \b после [íi]: RE2 \b — ASCII-only, после «í» никогда не
    -- сработает (то же ограничение, что в bazos/rules.go). «plyn není»
    -- как начало другого слова не встречается.
    ('CZ', 'has_gas', 'regex', '(?i)plyn\s+n[ěe]n[íi]', 'false'::jsonb,
     'active', 0.80, 'seed'),
    ('CZ', 'has_gas', 'regex', '(?i)plynov\p{L}*', 'true'::jsonb,
     'active', 0.80, 'seed'),
    ('CZ', 'has_gas', 'regex', '(?i)s\s+plynem\b', 'true'::jsonb,
     'active', 0.80, 'seed'),
    ('CZ', 'has_gas', 'regex', '(?i)plyn[ae]?\s+a\s+tepl\p{L}*', 'true'::jsonb,
     'active', 0.80, 'seed'),
    -- panel_renovation: «bez revitalizace» → original (гвард),
    -- любое «revitalizace/revitalizovaný» → revitalized.
    ('CZ', 'panel_renovation', 'regex', '(?i)bez\s+revitalizac\p{L}*', '"original"'::jsonb,
     'active', 0.80, 'seed'),
    ('CZ', 'panel_renovation', 'regex', '(?i)revitalizac\p{L}*', '"revitalized"'::jsonb,
     'active', 0.80, 'seed');
