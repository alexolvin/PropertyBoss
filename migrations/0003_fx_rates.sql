-- Курсы: фиксируются на дату наблюдения, пересчёт истории запрещён (ТЗ §5).
-- rate — точный NUMERIC (не float): 1 единица base = rate единиц quote.
CREATE TABLE fx_rates (
    base CHAR(3) NOT NULL REFERENCES currencies(code),
    quote CHAR(3) NOT NULL REFERENCES currencies(code),
    rate NUMERIC(20,10) NOT NULL CHECK (rate > 0),
    rate_date DATE NOT NULL,
    source TEXT NOT NULL,
    PRIMARY KEY (base, quote, rate_date)
);

-- Разрешение курса на дату: точный курс, иначе последний известный
-- с явной пометкой stale (ТЗ §5: «не молчаливая подстановка»).
CREATE OR REPLACE FUNCTION fx_rate_for(base CHAR(3), quote CHAR(3), on_date DATE)
RETURNS TABLE (rate NUMERIC, rate_date DATE, stale BOOLEAN)
STABLE STRICT AS $$
BEGIN
    RETURN QUERY
    SELECT r.rate, r.rate_date, r.rate_date < fx_rate_for.on_date AS stale
    FROM fx_rates r
    WHERE r.base = fx_rate_for.base
      AND r.quote = fx_rate_for.quote
      AND r.rate_date <= fx_rate_for.on_date
    ORDER BY r.rate_date DESC
    LIMIT 1;
END;
$$ LANGUAGE plpgsql;
