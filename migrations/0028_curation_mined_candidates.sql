-- 0028 — ручная ревизия (curation) mined-кандидатов первой волны (ТЗ v4 §4.3,
-- гибридное извлечение; вторая волна арсенала по факту мининга).
--
-- Контекст: sample-прогон `pb extract run --country CZ --limit 200` (2026-09-06)
-- выучил 97 mined-шаблонов (source='llm_mined', status='candidate'); к моменту
-- ревизии (2026-09-07, снапшот id до 288) полный фоновый прогон довёл их
-- число до ~121. Авто-промоушн удержан до ревизии (rules.auto_promote=false,
-- ТЗ §0.4 — догадка запрещена), поэтому отбор ниже — решение оператора.
--
-- Критерии:
--   PROMOTE (→ active, source='seed') — специфичные фразы, у которых совпадение
--     почти наверняка означает, что значение верно;
--   DISABLE — опасные: неверная атрибуция, слишком общие однословники
--     (ложные срабатывания), зависимость от даты;
--   ПРОЧИЕ — остаются candidate: накапливают повторения в фоновом прогоне,
--     следующая волна ревизии разберёт дошедшие до порога.
--
-- Сопоставление по id (точный снапшот ревизии, 2026-09-07): id стабильны
-- (BIGSERIAL); на чистой БД без мининга кандидатов нет — UPDATE заденет 0 строк
-- (no-op), миграция идемпотентна и безвредна. Контроль ROW_COUNT = снапшот:
-- рассинхрон → явная ошибка, а не тихий частичный отбор.
--
-- Закрытые здесь дефекты (см. .handoff/PropertyBoss_TZ_v4_stage1_gate.md):
--   * ownership «Osobní» (авто-промоут, 191 л.с.) — однословник без «vlast»;
--     кандидат «osobní» (id 105) отклонён по той же причине;
--   * floor/floors «…patře» — «на N-м этаже» ≠ «этажей в доме»;
--   * has_elevator «bezbariérovým přístupem» — безбарьерность ≠ лифт;
--   * seller «majitelů»/«zastoupení majitele» → private — за владельца
--     работает именно агентство (неверная атрибуция);
--   * year_built «pouhé dva roky starou» → 2024 — зависит от даты объявления.
DO $$
DECLARE
    total int;
    n     int;
BEGIN
    -- Свежая БД без мининга — нечего ревьюить: честный no-op, а не ошибка.
    SELECT count(*) INTO total
    FROM attribute_templates
    WHERE country = 'CZ' AND source = 'llm_mined';
    IF total = 0 THEN
        RAISE NOTICE '0028: mined-кандидатов нет — ревизия пропускается';
        RETURN;
    END IF;

    -- 1) PROMOTE: 65 безопасных фраз. Только status='candidate': если фоновый
    --    прогон успел поставить одной из них 'conflict' (то же паттерн, другое
    --    значение), снапшот устарел — миграция падает, а не промоутит конфликт.
    --    condition (16): reno/revita-фразы, «úplně/zcela nový», «…stavu»-фразы;
    --    floor (4):      «přízemí», «prvním/třetím nadzemním podlaží», «I.NP»;
    --    floors (3):     «čtyřmi podlažími», «třípodlažní(m)»;
    --    has_gas (1):    «plyn v bytě není» (конкретное отрицание);
    --    new_build (6):  «Ve výstavbě», «zcela nový byt», «nový projekt»,
    --                    «nově vybudovaný byt», «Termín dokončení»,
    --                    «prošel rekonstrukcí»→false;
    --    ownership (5):  только с «vlast/vl.» в фразе (граница слова: «os. vl.»
    --                    не матчит «os. vlast…», «vlastnictví: Osobní» не
    --                    матчит голое «Osobní»);
    --    panel (6):      «revitalizovan…», «modernizovaného»,
    --                    «po celkové rekonstrukci», «původním stavu»→original;
    --    seller (24):    RK-сигналы agency/private (provize RK, Bez RK,
    --                    přímo od majitele, «…nevolat/nekontaktovat» и т.д.).
    UPDATE attribute_templates
    SET status = 'active', source = 'seed', promoted_at = now()
    WHERE country = 'CZ'
      AND source = 'llm_mined'
      AND status = 'candidate'
      AND id = ANY('{18,22,30,39,41,46,47,49,52,55,58,61,64,65,67,71,72,74,78,80,
                    81,89,90,92,96,98,101,114,117,122,128,133,134,137,142,146,
                    150,151,152,157,161,166,168,169,179,182,194,197,215,223,226,
                    229,238,241,242,248,249,251,253,258,262,264,265,268,270}');
    GET DIAGNOSTICS n = ROW_COUNT;
    IF n <> 65 THEN
        RAISE EXCEPTION '0028: промоушн задет % строк, ожидалось 65 (снапшот устарел)', n;
    END IF;

    -- 2) DISABLE: 10 опасных (список и причины — в шапке).
    UPDATE attribute_templates
    SET status = 'disabled'
    WHERE country = 'CZ'
      AND source = 'llm_mined'
      AND id = ANY('{19,42,59,83,105,132,147,211,222,252}');
    GET DIAGNOSTICS n = ROW_COUNT;
    IF n <> 10 THEN
        RAISE EXCEPTION '0028: disable задет % строк, ожидалось 10 (снапшот устарел)', n;
    END IF;
END $$;
