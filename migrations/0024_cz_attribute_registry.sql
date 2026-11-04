-- TZ v4 §4.1 + ТЗ v2 §6: наполнение CZ-секции реестра атрибутов ПО ФАКТУ
-- разбора реальных страниц reality.bazos.cz (проверено 2026-09-06, 4 детали
-- страниц: 222871645, 222453921, 223071667, 223028684).
-- source_evidence — страница источника, где атрибут реально присутствует.
-- used_in_pricing — попадает ли атрибут в гедоническую модель (ТЗ v2 §7.2).

INSERT INTO attribute_registry
    (country, key, data_type, allowed_values, used_in_pricing, label_ru, label_en, source_evidence)
VALUES
    ('CZ', 'area_sqm', 'float', NULL, false,
     'Площадь (утильная/подложная), м²', 'Living/floor area, m²',
     'Площадка публикует площадь: блок «Plocha: Užitná plocha 80 m²» — https://reality.bazos.cz/inzerat/222871645/; «82,01 m2» в заголовке — https://reality.bazos.cz/inzerat/222453921/; «28 m²» — https://reality.bazos.cz/inzerat/223071667/; «84 m2» в описании — https://reality.bazos.cz/inzerat/223028684/. В модели — непрерывная переменная f(площадь) в коде, не one-hot. Проверено 2026-09-06.'),
    ('CZ', 'rooms', 'int', NULL, true,
     'Комнат', 'Rooms',
     'Заголовок объявления кодирует комнаты: «Prodej bytu 3kk» — https://reality.bazos.cz/inzerat/222871645/; «BYT 2+KK+ZAHRÁDKA» — https://reality.bazos.cz/inzerat/222453921/; «byt OV 1+kk» — https://reality.bazos.cz/inzerat/223071667/. Числовая переменная модели. Проверено 2026-09-06.'),
    ('CZ', 'floor', 'int', NULL, false,
     'Этаж', 'Floor',
     'Блок «Stavba: … 2. podlaží» — https://reality.bazos.cz/inzerat/222871645/; «ve 4. nadzemním podlaží» — https://reality.bazos.cz/inzerat/223071667/; «ve 4. patře» — https://reality.bazos.cz/inzerat/223028684/. Проверено 2026-09-06.'),
    ('CZ', 'floors', 'int', NULL, false,
     'Этажей в доме', 'Floors in building',
     '«Stavba: … Jednopodlažní» — https://reality.bazos.cz/inzerat/222871645/; «čtyřpodlažního panelového domu» (4 этажа) — https://reality.bazos.cz/inzerat/223028684/; «šestipodlažního bytového domu» (6) — https://reality.bazos.cz/inzerat/222453921/. Проверено 2026-09-06.'),
    ('CZ', 'building_material', 'enum',
     '["cihlova","panelova","cihlopanelova","zdena","kamenna","drevena","jina"]', true,
     'Материал здания', 'Building material',
     'Блок «Stavba: Cihlová…» — https://reality.bazos.cz/inzerat/222871645/; «panelového domu» — https://reality.bazos.cz/inzerat/223028684/. Значения-перечень: cihlová/zděná (кирпич/камень), panelová, cihlopanelová, dřevěná. Проверено 2026-09-06.'),
    ('CZ', 'energy_class', 'enum',
     '["A","B","C","D","E","F","G","usporne","nesporne"]', true,
     'Энергокласс', 'Energy class',
     'Блок «Energetická náročnost: Úsporná» — https://reality.bazos.cz/inzerat/222871645/. Старая система (Úsporná/Neúsporná) и новая (A–G) сосуществуют на площадке. Проверено 2026-09-06.'),
    ('CZ', 'condition', 'enum',
     '["very_good","good","average","poor","renovated"]', true,
     'Состояние', 'Condition',
     '«Ve velmi dobrém stavu» (блок Stavba) — https://reality.bazos.cz/inzerat/222871645/; «ve velmi dobrém stavu» — https://reality.bazos.cz/inzerat/223071667/; «kompletně zrekonstruovaný byt» — https://reality.bazos.cz/inzerat/223028684/. Проверено 2026-09-06.'),
    ('CZ', 'has_elevator', 'bool', NULL, false,
     'Лифт', 'Elevator',
     '«bytového domu s výtahem» — https://reality.bazos.cz/inzerat/223071667/, https://reality.bazos.cz/inzerat/223028684/. Отсутствует на странице — значение не выставляется (догадка запрещена). Проверено 2026-09-06.'),
    ('CZ', 'new_build', 'bool', NULL, true,
     'Новостройка', 'New build',
     '«úplně nový moderní byt» — https://reality.bazos.cz/inzerat/222453921/; «novostavba» в заголовках объявлений того же раздела — страница https://reality.bazos.cz/inzerat/222871645/ (раздел «Podobné inzeráty»). Проверено 2026-09-06.'),
    ('CZ', 'total_area', 'float', NULL, false,
     'Общая площадь, м²', 'Total area, m²',
     '«celkové výměře 53m²», «o celkové ploše 99 m²», «o celkové výměře 51,2» — описания объявлений, страница https://reality.bazos.cz/inzerat/222871645/ (раздел «Podobné inzeráty»). Проверено 2026-09-06.'),
    ('CZ', 'monthly_expenses', 'float', NULL, false,
     'Ежемесячные расходы, CZK/мес', 'Monthly expenses, CZK/month',
     '«předběžně budou stanoveny na 2000-3000 Kč/měsíc» — https://reality.bazos.cz/inzerat/222453921/. Диапазоны записываются нижней границей (допускение, см. отчёт). Проверено 2026-09-06.');

-- Существующие строки: расширяем доказательства, включаем в ценовую модель,
-- где атрибутивно оправдано (документировано в отчёте этапа).
UPDATE attribute_registry
SET used_in_pricing = true,
    source_evidence = source_evidence || ' Ценно-значим для модели: форма собственности влияет на цену и ликвидность.'
WHERE country = 'CZ' AND key = 'ownership_type';

UPDATE attribute_registry
SET used_in_pricing = true
WHERE country = 'CZ' AND key = 'panel_renovation';
