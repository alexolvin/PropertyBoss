-- Бэкофф по капче/429 (ТЗ §10.4, этап 11).
-- cooldown_strikes — количество ПОСЛЕДОВАТЕЛЬНЫХ сканов с captcha/http_429
-- без полного успешного скана между ними; полный скан сбрасывает в 0.
-- Длительность кулдауфа = base * multiplier^(strikes-1), с потолком
-- (значения — в конфиге, ТЗ §0.1).
-- rate_factor — множитель эффективного потолка max_requests_per_hour:
-- падает вдвое при входе в кулдаун, восстанавливается ПОСТЕПЕННО
-- (пошагово) при последующих полных сканах (ТЗ §10.4: «Восстановление
-- — постепенное»).
ALTER TABLE sources ADD COLUMN cooldown_strikes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sources ADD COLUMN rate_factor NUMERIC(4,3) NOT NULL DEFAULT 1.0
    CHECK (rate_factor > 0);
