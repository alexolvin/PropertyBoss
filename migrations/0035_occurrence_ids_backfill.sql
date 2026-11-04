-- C8: backfill occurrence_ids для шаблонов, выученных до 0034.
-- До 0034 подтверждения фиксировались только в example_object_ids
-- (до rules.example_objects, distinct). Строка без occurrence_ids не
-- имеет материала для холдаута (len < 2 — «подтверждений 0 < 2»,
-- авто-промоушн невозможен), поэтому пример переносится в
-- occurrence_ids.
--
-- Оговорка о порядке: example_object_ids строится в mine.go как
-- DISTINCT ... ORDER BY id — не в хронологическом порядке
-- подтверждений. «Последние 2» у legacy-строк — два наибольших
-- object id, а не самые свежие подтверждения. Проверка холдаута от
-- этого не искажается (класс бага C12: шаблон срабатывает в Apply и
-- даёт значение, равное LLM-значению, — на двух подлинных прошедших
-- подтверждениях), а хронологический порядок восстанавливается по
-- мере добавления новых подтверждений (append в конец).
--
-- Не трогаем: seed-шаблоны (example_object_ids IS NULL) и строки,
-- подтверждённые после 0034 (occurrence_ids уже непусто — там
-- хронологические id, и дублировать legacy-примеры не нужно).
UPDATE attribute_templates
SET occurrence_ids = example_object_ids
WHERE occurrence_ids = '{}'
  AND example_object_ids IS NOT NULL
  AND example_object_ids <> '{}';
