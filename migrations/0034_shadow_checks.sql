-- C8: теневой контроль промоутнутых mined-шаблонов.
-- Доля shadow_sample_pct (конфиг) объектов, обработанных ПРОМОУТНУТЫМ
-- шаблоном, дополнительно уходят в LLM; расхождение (включая пустой
-- ответ LLM) — демоушн шаблона. Это единственная защита от того, что
-- покрытие считает неверное значение покрытым (C12).
-- Seed-шаблоны в тень не идут: они не выучены и не могут «стать»
-- неверными массово (пометка в отчёте).
CREATE TABLE template_shadow_checks (
    id BIGSERIAL PRIMARY KEY,
    template_id BIGINT NOT NULL REFERENCES attribute_templates(id) ON DELETE CASCADE,
    object_id BIGINT NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
    attr_key TEXT NOT NULL,
    template_value JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'done', 'mismatch')),
    llm_value JSONB,
    checked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (template_id, object_id, attr_key)
);
CREATE INDEX template_shadow_checks_pending_idx
    ON template_shadow_checks (id) WHERE status = 'pending';

-- C8: id объектов, на которых шаблон подтверждён, в порядке
-- подтверждения (append-only, дедупликация). Холдаут для
-- авто-промоушна = последние 2 из них (при пороге >= 5 остаётся
-- >= 3 на подгонку); example_object_ids — отдельно, для ручной ревизии.
ALTER TABLE attribute_templates ADD COLUMN occurrence_ids BIGINT[] NOT NULL DEFAULT '{}';

-- C8: счётчик теневых расхождений и момент демоушна (демоушн — в одну
-- сторону, до ручного pb rules set).
ALTER TABLE attribute_templates ADD COLUMN shadow_mismatches INT NOT NULL DEFAULT 0;
ALTER TABLE attribute_templates ADD COLUMN demoted_at TIMESTAMPTZ;

ALTER TABLE attribute_templates DROP CONSTRAINT attribute_templates_status_check;
ALTER TABLE attribute_templates ADD CONSTRAINT attribute_templates_status_check
    CHECK (status IN ('active', 'candidate', 'conflict', 'disabled', 'demoted'));
