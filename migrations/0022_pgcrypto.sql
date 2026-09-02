-- pgcrypto: sha256 для идемпотентности переводов (ТЗ §11).
-- Работник переводчика сравнивает source_hash строк object_translations
-- с хешем description_original на стороне SQL (digest/encode), чтобы
-- выборка «кому нужен перевод» не тянула все объекты. Совпадает с
-- crypto/sha256 + hex в Go (проверено: digest('test') — тот же хеш).
CREATE EXTENSION IF NOT EXISTS pgcrypto;
