-- Этап 4: идемпотентный импорт зон (pb zones import).
-- Уникальность (country, external_code): повторный импорт того же файла
-- обновляет строки, а не дублирует их. external_code NULL — уникальности не
-- имеет (Postgres рассматривает NULL как различающиеся), такие зоны при
-- повторном импорте будут созданы заново — предупреждение в отчёте импорта.
ALTER TABLE zones ADD CONSTRAINT zones_country_code_uq UNIQUE (country, external_code);
