-- Справочник валют. Источник экспонент: ISO 4217.
-- EUR и CZK — минорные единицы с экспонентой 2 (ТЗ §5).
CREATE TABLE currencies (
    code CHAR(3) PRIMARY KEY,
    exponent SMALLINT NOT NULL
);

INSERT INTO currencies (code, exponent) VALUES
    ('EUR', 2),
    ('CZK', 2);
