-- 0026 — LLM-извлечение атрибутов (ТЗ v4 §4.3):
--   1) attribute_registry: допустимые диапазоны числовых значений
--      (value_min/value_max) — программа проверяет ответ модели
--      программно, не «исправляя» его; диапазон — данные реестра,
--      не код (ТЗ §0.10);
--   2) llm_extractions: учёт вызовов модели (идемпотентность +
--      суммарный расход токенов для бюджета).

-- 1) Диапазоны числовых значений.
-- Значения — доменные границы рынка (Чехия), а не «разумные по
-- умолчанию»: за их пределами значение отбрасывается как
-- неправдоподобное (ТЗ v4 §4.3). Оператор уточняет при выявлении
-- допустимых значений за пределами.
ALTER TABLE attribute_registry
    ADD COLUMN value_min NUMERIC,
    ADD COLUMN value_max NUMERIC;

COMMENT ON COLUMN attribute_registry.value_min IS
    'Нижняя граница числового значения (включительно); NULL — нет границы (ТЗ v4 §4.3).';
COMMENT ON COLUMN attribute_registry.value_max IS
    'Верхняя граница числового значения (включительно); NULL — нет границы (ТЗ v4 §4.3).';

-- Чехия: диапазоны числовых атрибутов реестра.
UPDATE attribute_registry SET value_min = 1,    value_max = 5000   WHERE country = 'CZ' AND key = 'area_sqm';
UPDATE attribute_registry SET value_min = 1,    value_max = 5000   WHERE country = 'CZ' AND key = 'total_area';
UPDATE attribute_registry SET value_min = 0,    value_max = 30     WHERE country = 'CZ' AND key = 'rooms';
UPDATE attribute_registry SET value_min = 0,    value_max = 300    WHERE country = 'CZ' AND key = 'floor';
UPDATE attribute_registry SET value_min = 1,    value_max = 300    WHERE country = 'CZ' AND key = 'floors';
UPDATE attribute_registry SET value_min = 1800, value_max = 2026   WHERE country = 'CZ' AND key = 'year_built';
UPDATE attribute_registry SET value_min = 0,    value_max = 100000 WHERE country = 'CZ' AND key = 'monthly_expenses';
UPDATE attribute_registry SET value_min = 1,    value_max = 100000 WHERE country = 'CZ' AND key = 'plot_area';

-- 2) Учёт вызовов LLM при извлечении (ТЗ v4 §4.3):
-- идемпотентность — повтор извлечения того же текста той же моделью
-- той же версией перечня не выполняется; расход — для бюджета
-- (при исчерпании задачи остаются в очереди).
CREATE TABLE llm_extractions (
    object_id         BIGINT NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
    model             TEXT NOT NULL,          -- модель из конфига: стабильная часть ключа идемпотентности; реальное имя, обслужившее запрос, — в attribute_values.model
    attr_list_version TEXT NOT NULL,          -- версия перечня атрибутов (конфиг)
    source_hash       CHAR(64) NOT NULL,      -- sha256 текста объявления
    tokens            INTEGER NOT NULL CHECK (tokens >= 0),
    extracted_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (object_id, model, attr_list_version, source_hash)
);
CREATE INDEX llm_extractions_tokens_idx ON llm_extractions (extracted_at);
