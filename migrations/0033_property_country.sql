-- C20: страна объекта, отдельная от страны источника.
-- objects.country — страна площадки; property_country — где фактически
-- объект. Корпус ценовой модели — property_country='CZ'; иностранные
-- объявления-заглушки («Zahraničí», «Slovensko» — 480 объектов на двух
-- страновых точках-заглушках: юг Норвегии / центральная Словакия)
-- получают своё значение и выходят из корпуса: они искажают уровни цен
-- и эффекты зон гедонической модели (C20).
--
-- zone_terminal — терминальный «вне всех зон»: объект окончательно
-- без зоны и больше не входит в рабочее множество переобработки
-- (C15: 480 переобрабатывались каждый раунд вечно и всегда впустую;
-- Assign исключает их из кандидатов).
ALTER TABLE objects ADD COLUMN property_country CHAR(2) NOT NULL DEFAULT 'CZ';
ALTER TABLE objects ADD COLUMN zone_terminal BOOLEAN NOT NULL DEFAULT FALSE;

-- Население: терминальный = есть координата вне выпуклой оболочки всех
-- зон CZ (геометрия: ST_Union агрегатом по geography не существует).
-- SK/XX — по адресу, а не по точке: точка-заглушка не является адресом
-- («Slovensko, 987 65» → SK; «Zahraničí, 987 66» → XX — страна
-- неизвестна, «Зарубежье»). Внутри оболочки — CZ (по умолчанию).
WITH hull AS (
    SELECT ST_ConvexHull(ST_Union(z.geom::geometry)) AS h
    FROM zones z
    WHERE z.country = 'CZ'
)
UPDATE objects o
SET zone_terminal = true,
    property_country = CASE
        WHEN o.address ILIKE '%slovensko%' THEN 'SK'
        ELSE 'XX'
    END
FROM hull
WHERE o.country = 'CZ'
  AND o.geom IS NOT NULL
  AND NOT ST_Within(o.geom::geometry, hull.h);
