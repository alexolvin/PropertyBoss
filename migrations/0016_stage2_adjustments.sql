-- Этап 2: конфигурации поиска настраиваются до выбора конкретных площадок
-- (источники регистрируются в этапе 3 по проверке доступа, ТЗ §13).
-- Поэтому source_id пока nullable: конфигурация привязывается к источнику
-- в этапе 3, когда source известен.
ALTER TABLE search_configs ALTER COLUMN source_id DROP NOT NULL;

-- Тип сделки — доменный инвариант ТЗ (§7.2: модели — отдельно по
-- «продажа / аренда»). Фиксируем в схеме, чтобы случайное значение
-- не просочилось в ценовые модели.
ALTER TABLE objects ADD CONSTRAINT objects_deal_type_chk CHECK (deal_type IN ('sale', 'rent'));
ALTER TABLE search_configs ADD CONSTRAINT search_configs_deal_type_chk CHECK (deal_type IN ('sale', 'rent'));
ALTER TABLE zone_reference_prices ADD CONSTRAINT zrps_deal_type_chk CHECK (deal_type IN ('sale', 'rent'));
