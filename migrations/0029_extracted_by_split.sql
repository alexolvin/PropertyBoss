-- 0029 — ТЗ v4-2 §4.2: разделение extracted_by 'rule' на два шага цепочки:
--   rule_markup — разбор разметки страницы (кодовые правила bazos.Derive,
--                 тип продавца по маркерам);
--   rule_text   — текстовый реестр шаблонов (attribute_templates, движок
--                 attrules: enrich + оффлайн-прогон pb rules run).
--
-- Основа переклассификации (аудит 2026-09-07, tmp_audit_rulesplit, БД):
--   1) Единственный писатель 'rule'-строк БЕЗ source_hash — оффлайн-прогон
--      шаблонов pb rules run (internal/attrules/run.go не пишет source_hash);
--      у enrich (saveObject) source_hash всегда = detail_hash. Поэтому
--      'rule' AND source_hash IS NULL (6677 строк: ownership_type 1363,
--      condition 1226, panel_renovation 1184, seller_type 1075, has_gas 997,
--      floor 478, new_build 346, floors 8) — определённо шаблонные → rule_text.
--   2) has_gas/panel_renovation кодовых правил не имеют (bazos/rules.go) —
--      их 'rule'-строки со source_hash тоже шаблонные → rule_text (страховка;
--      фактически все такие строки уже охвачены п.1).
--   3) Двойственный бакет (enrich-строки dual-layer, записанные после
--      промоушна 0028, 2026-09-07 02:11:46 UTC) ПУСТ: 0 строк (аудит).
--      Остальные 'rule' — кодовые правила (Derive/продавец) → rule_markup.
-- Временных списков пар (object_id, attr_key) не требуется.
--
-- Порядок применения: СНАЧАЛА сменить писателей на новые значения
-- (enrich.go, attrules/run.go, bazos/rules.go, cmd/pb/rules.go) и дождаться
-- конца прогона старого бинаря (пишет 'rule') — И ТОЛЬКО ПОТОМ pb migrate:
-- DROP CONSTRAINT снимает 'rule' из CHECK, и старые писатели ошибутся.

-- Сначала разрешаем новые значения (широкий CHECK), переклассифицируем,
-- затем сужаем до целевого набора. Миграция в одной транзакции — писателей
-- 'rule' на момент применения нет (см. условие в шапке).

ALTER TABLE attribute_values DROP CONSTRAINT attribute_values_extracted_by_check;
ALTER TABLE attribute_values ADD CONSTRAINT attribute_values_extracted_by_check
    CHECK (extracted_by IN ('source_field', 'rule', 'rule_markup', 'rule_text', 'llm_extraction'));

UPDATE attribute_values SET extracted_by = 'rule_text'
 WHERE extracted_by = 'rule' AND source_hash IS NULL;

UPDATE attribute_values SET extracted_by = 'rule_text'
 WHERE extracted_by = 'rule' AND attr_key IN ('has_gas', 'panel_renovation');

UPDATE attribute_values SET extracted_by = 'rule_markup'
 WHERE extracted_by = 'rule';

ALTER TABLE attribute_values DROP CONSTRAINT attribute_values_extracted_by_check;
ALTER TABLE attribute_values ADD CONSTRAINT attribute_values_extracted_by_check
    CHECK (extracted_by IN ('source_field', 'rule_markup', 'rule_text', 'llm_extraction'));
