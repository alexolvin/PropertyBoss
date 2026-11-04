-- Переводы описаний (ТЗ §11): оригинал хранится всегда, переводы в БД.
-- source_hash = sha256(description_original) — идемпотентность перевода.
CREATE TABLE object_translations (
    object_id BIGINT NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
    lang CHAR(2) NOT NULL,
    source_hash CHAR(64) NOT NULL,
    text TEXT NOT NULL,
    model TEXT NOT NULL,
    token_cost INTEGER,
    translated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (object_id, lang)
);
CREATE INDEX translations_hash_idx ON object_translations (source_hash);
