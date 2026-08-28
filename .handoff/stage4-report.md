# Отчёт по этапу 4 — зоны: полигоны Италии, иерархия, привязка объектов, котировки (ТЗ §7.1, §15.4)

Дата: 2026-08-28. Хост: `vzu5-claw`. Исполнитель: Claude (Claude Code).
Критерий готовности этапа: «Объект попадает в зону через PostGIS, зона
видна в UI».

## Реализовано

### Схема и миграция

- Таблицы `zones` и `zone_reference_prices` — миграция `0004_zones`
  (этап 1): `zones` (country CHAR(2), level ∈ region|municipality|zone,
  external_code, name, parent_id, geom GEOGRAPHY(MULTIPOLYGON,4326),
  source, GiST-индекс `zones_geom_idx`); `zone_reference_prices`
  (PK zone_id+deal_type+property_type+period_start+source,
  price_min/max_minor BIGINT, currency, data_kind).
- Миграция `0017_zones_import`: UNIQUE (country, external_code) —
  ключ идемпотентности импорта. Применена.

### Импорт GeoJSON — `pb zones import` (ТЗ §7.1)

- `internal/zones/geojson.go`: FeatureCollection → `zones`. Свойства
  фичи: `level` (region|municipality|zone), `name` (непустое),
  `external_code` (необязательное), `parent_external_code` (только
  для municipality/zone; у region родителя нет — уровень country не
  строка в zones).
- Идемпотентно: повторный импорт того же файла обновляет строки
  (ON CONFLICT по (country, external_code), parent_id при этом
  обнуляется — ссылку восстанавливает `link`). Нерешённые родительские
  ссылки не откатывают импорт: данные записываются, функция возвращает
  и отчёт (Unresolved + до 10 примеров), и явную ошибку (ТЗ §0.2).
- Геометрии передаются в PostGIS как GeoJSON, без GDAL/CGO (чистый Go —
  цель этапа 13, Termux без CGO).
- Вставка пачками по 100; при ошибке пачки — повтор построчно с
  номером фичи в ошибке; если сам повтор провален прерыванием
  транзакции (SQLSTATE 25P02), возвращается причина пачки, а не
  вторичная ошибка (в первой ревизии корневая причина пряталась за
  25P02 — исправлено).
- Весь импорт — одна транзакция.

### Родительские ссылки по геометрии — `pb zones link`

- `internal/zones/link.go`: для источников, не дающих родительских
  ссылок в данных (geoBoundaries), `parent_id` ставится
  пространственно: родителем считается зона требуемого уровня,
  покрывающая точку поверхности полигона ребёнка (ST_PointOnSurface;
  ST_Covers учитывает точку на общей границе); несколько кандидатов —
  наименьшая по площади, при равенстве — меньший id (детерминизм).
- Идемпотентно: работает только над зонами с parent_id IS NULL.
- Пространственные функции считаются в домене geometry
  (geography::geometry): в локальной сборке PostGIS отсутствует
  ST_PointOnSurface(geography) — см. «Особенности окружения».

### Котировки OMI — `pb zones quotazioni` (ТЗ §7.1, §13)

- `internal/zones/quotazioni.go`: CSV → `zone_reference_prices`.
  Заголовок (обязательные колонки): `codzona,tipo,contratto,
  prezzo_min,prezzo_max,periodo` (+ необязательная `nomezona`);
  любая другая колонка — явная ошибка. Разделитель — `,` или `;`
  (автоопределение). `contratto`: vendita→sale, affitto→rent.
  Цены — в минорных (EUR exponent 2), точное преобразование без
  округления; пустая ячейка — честный NULL (ТЗ §0.4).
  `periodo`: `YYYY/1|YYYY/2|YYYY-S1|YYYY-S2|YYYY-H1|YYYY-H2` →
  01.01 / 01.07 (UTC). `data_kind='transaction'` (котировка — базовый
  уровень, не сделка).
- Идемпотентно (upsert по PK). Ошибки строк (до 10) — явная ошибка
  с номерами строк, ничего не записывается.
- `QuotSource = "Agenzia Entrate - OMI"` — атрибуция по умолчанию
  (ТЗ §13), колонка source хранится и видна в UI.

### Привязка объектов к зонам — `pb zones assign` (критерий этапа)

- `internal/zones/assign.go`: объект получает **самую конкретную**
  зону, покрывающую его точку: zone > municipality > region; при
  нескольких зонах уровня — меньшая по площади, затем id.
- Честные NULL (ТЗ §0.4): без координат или вне всех зон —
  zone_id = NULL. Обнуление: записанная зона больше не покрывает
  объект (данные зон обновлены) либо координат больше нет.
- Идемпотентно (повторный прогон: Changed=0, Cleared=0). Статус
  объекта не имеет значения — зона описывает расположение.

### UI: зона объекта и атрибуция (ТЗ §13, критерий «видна в UI»)

- `GET /api/zones?country=&level=&page=&per_page=` + список источников
  (`internal/api/zones.go`); в карточке объекта — join по зонам
  (zone_name, zone_level, zone_source).
- Дашборд: вкладка «Зоны» (список с фильтрами по стране/уровню,
  колонка источника), в таблице объектов — колонка зоны (имя +
  уровень) с атрибуцией источника данных; i18n ru/en; страны
  meta.countries = [CZ, IT, NL].

### Реальные границы Италии (вместо OMI-полигонов как базовый слой)

- Источник: **geoBoundaries gbOpen, ITA, ISTAT (National Statistics
  Institute), год данных 2023, сборка 2023-12-12, лицензия CC BY 3.0**
  (licenseSource www.istat.it/it/note-legali,
  sourceURL www.istat.it/it/archivio/222527). Фиксированный
  commit релиза **9469f09** — данные воспроизводимы.
- Иерархия geoBoundaries для Италии сдвинута относительно
  «названий уровней»: ADM2 = 20 регионов, ADM4 = 7901 комун.
  Использование: ADM2 → level `region`, ADM4 → level `municipality`.
  ADM1 (5 макро-регионов) и ADM3 (107 провинций) не используются —
  не входят в иерархию ТЗ §7.1.
- Файлы (в gitignored `data/zones/`, не в репозитории):
  - `https://github.com/wmgeolab/geoBoundaries/raw/9469f09/releaseData/gbOpen/ITA/ADM2/geoBoundaries-ITA-ADM2_simplified.geojson` (1,26 МБ)
  - `https://github.com/wmgeolab/geoBoundaries/raw/9469f09/releaseData/gbOpen/ITA/ADM4/geoBoundaries-ITA-ADM4_simplified.geojson` (18,35 МБ)
  - Преобразование (jq): `shapeName` → `name`, `shapeID` →
    `external_code`, добавляется `level`. Команда в разделе
    «Требует ручной проверки» (воспроизведение).
- Прогон (2026-08-27/28):
  - `pb zones import -file …IT_regions.geojson -country IT -source "GeoBoundaries / ISTAT (2023)"` — создано 20.
  - `pb zones import -file …IT_municipalities.geojson …` — создано 7901.
  - `pb zones link -country IT -level municipality` — **кандидатов 7901, связано 7901, без родителя 0**.
  - Итог: 7921 зона IT; иерархия проверена выборочно (Abano Terme → Veneto, Abbadia Lariana → Lombardia, Abbasanta → Sardegna — совпадает с реальностью).
- Уровня `zone` (однородные зоны OMI) пока нет — они приходят из
  выгрузки OMI (см. «Не реализовано»). После импорта OMI-зоны
  привязываются к комунам той же командой: `pb zones link -country IT -level zone`.

### Тесты

- `internal/zones/zones_test.go` (против живой БД, PB_TEST_DSN,
  зачистка t.Cleanup; country='TT', source='pb-zones-test'):
  - `TestImportAndAssign`: импорт 3 зон (регион ⊃ муни ⊃ зона),
    иерархия, идемпотентный повтор, 5 объектов (в зоне / в муни /
    в регионе / вне всех / без координат) — assign даёт самую
    конкретную зону, вне → NULL, повторный assign ничего не меняет,
    ушедшая точка обнуляется. Предпосылка проверяется явно: assign
    глобальный, реальных объектов с координатами/зоной быть не
    должно (на текущих этапах коннекторы координаты не публикуют).
  - `TestImportValidation`: 7 сценариев явных ошибок (не
    FeatureCollection, плохой level, region с родителем, дубль кода,
    геометрия-точка, пустой name, пустой features) + «ничего не
    записалось».
  - `TestQuotazioni`: 3 строки CSV (точные минорные 145000/160000,
    850/920; NULL-цена; периоды 2025-01-01 и 2025-07-01),
    идемпотентность, неизвестный код зоны и незнакомая колонка —
    явные ошибки, ничего не записано.
  - `TestParseQuotPriceAndPeriod`: чистые юнит-кейсы цен и периодов
    (без БД).
  - Прогон 2026-08-28 против живой БД: **все 4 теста PASS**
    (первый прогон выявил два дефекта тестов, не кода: ожидания
    искали пустую таблицу objects, а БД содержит 7762 реальных
    объекта — тест переведён на базовые подсчёты с явной
    предпосылкой; строка CSV использовала период `2025-2`, не
    входящий в зафиксированный формат — заменён на `2025-H2`).

## Результаты на реальных данных

- `zones`: 7921 строка IT (20 region + 7901 municipality), все
  муниципалитеты имеют родителя-регион. Других стран нет.
- `zone_reference_prices`: пусто — реальные котировки OMI требуют
  аккаунта (см. ниже).
- `pb zones assign` (2026-08-28): **с координатами=0, без координат=
  7762, изменено=0, обнулено=0, вне зон=0**. Все 7762 объекта CZ не
  имеют координат — reality.bazos.cz публикует только примерную
  локацию (этап 3), честный NULL (ТЗ §0.4). Критерий «объект
  попадает в зону через PostGIS» доказан тестом (точка в зоне →
  zone_id; вне → NULL) и реальной пространственной операцией
  (link: 7901/7901 через ST_Covers на 7921 зоне). Реальные объекты
  попадут в зоны автоматически (`pb zones assign`) после появления
  коннектора, публикующего координаты (IT/NL — этапы 4b-дальнейшие/9).

## Не реализовано и почему

- **Реальные данные OMI (котировки и полигоны однородных зон).**
  Выгрузка «Forniture dati OMI» (CSV котировок + периметры зон)
  доступна только зарегистрированным пользователям
  Fisconline/Entratel и приходит по запросу (ТЗ §7.1). Нужна
  учётная запись пользователя — формат CSV зафиксирован в коде и
  тестах, импорт готов; как только файл получен — `pb zones
  import` (полигоны) + `pb zones link -level zone` + `pb zones
  quotazioni` (котировки).
- **Привязка реальных объектов** — нет объектов с координатами
  (см. выше). Механизм готов и проверен.
- **Зоны для CZ/NL** — полигоны ОМИ/ČSÚ/CBS не описаны ТЗ как
  обязательные на этом этапе (ТЗ §7.1 называет OMI для Италии).
  Механизм стран-независимый (country в PK и во всех запросах).

## Допущения

- **Упрощённые геометрии geoBoundaries** (`_simplified`) — точность
  достаточна для зонирования (метки на уровне сотен метров; зоны
  OMI и границы регионов не требуют кадастровой точности). Полные
  файлы на порядок больше и Termux-переносу (этап 13) на пользу не
  идут.
- **`external_code` = shapeID geoBoundaries** — неопределённый
  23-символьный ID, стабильный в пределах фиксированного commit
  (9469f09); настоящие коды ISTAT лежат в .dbf-справочнике
  (не распарсен, в GeoJSON их нет). Для целей системы (уникальность
  + идемпотентность + join) подходит.
- **Административные полигоны как базовый слой иерархии** до
  появления OMI-зон: region+municipality дают «базовый уровень цены
  по зонам» (ТЗ §7.1) для всей территории Италии сразу.
- **Формат CSV котировок** (колонки, формат периодов,
  auto-detect разделителя) — зафиксирован исполнителем под описание
  выгрузки OMI в ТЗ; при получении реального файла возможно
  расхождение — при импорте оно проявится явной ошибкой (не
  незнакомая колонка, так как период/contratto), формат легко
  подстроится в одном месте (quotazioni.go).
- **Плоская ST_Area(geometry)** в ранжировании link/assign —
  достаточна для выбора «меньше = специфичнее» в пределах одного
  уровня страны (геодезическая разница не меняет выбор
  практически; см. особенности окружения).
- **Атрибуция «GeoBoundaries / ISTAT (2023)»** в колонке source —
  видно в UI (ТЗ §13); лицензия CC BY 3.0 — соблюдена ссылкой на
  источник.

## Заглушки

- Нет. Импорт, link, assign, quotazioni, API и UI — реальные;
  данные — реальные (ISTAT 2023). `ErrNotImplemented` на этом
  этапе не используется.

## Особенности окружения (важно для этапа 13)

Локальная сборка PostgreSQL/PostGIS на vzu5-claw **неполная** —
часть гео-перегрузок отсутствует (проверено эмпирически,
SQLSTATE 42883 + успешные прогоны; функции резолвятся на parse,
успешный прогон = функция существует):

- **Отсутствуют:** `ST_GeogFromGeoJSON` (любые аргументы),
  `ST_Multi(geography)`, `ST_PointOnSurface(geography)`.
- **Существуют:** `ST_GeomFromGeoJSON(json|jsonb|text)`,
  `ST_Multi(geometry)`, `ST_AsGeoJSON`, `ST_GeogFromText`,
  `ST_DWithin(geography)`, KNN `<->`, `ST_Covers(geography,
  geography)`, `ST_Area(geography)`, касты geography↔geometry.

Рабочие паттерны (используются в коде): GeoJSON → geography-колонку:
`ST_Multi(ST_GeomFromGeoJSON($n::jsonb))::geography`; пространственные
предикаты при отсутствии гео-перегрузки — в домене geometry
(`ST_Covers(a::geometry, ST_PointOnSurface(b::geometry))`). На
цельном Termux-PostGIS (этап 13) это может работать иначе —
проверить по pg_proc при переносе. Также на этом хосте: Go
go1.25.13 без `html.Unescape` и с ограничением вариадического
pass-through (см. отчёты этапов 2–3), pgx не сканирует int4→*bool.

## Требует ручной проверки

1. **Дашборд** (`pb serve`, 127.0.0.1:8090): вкладка «Зоны» —
   7921 IT-зона, фильтры по стране/уровню, колонка источника
   «GeoBoundaries / ISTAT (2023)»; «Объекты» — колонка зоны (пока
   NULL у всех — нет координат, это честно).
   **Важно (2026-08-28):** до открытия убедиться, что на 8090
   работает **свежий** `pb serve` — на порту стоял старый процесс
   `go run ./cmd/pb serve` от 2026-08-26 (этап 2/3, без
   `/api/zones` — вкладка «Зоны» даст 404). Диагностика:
   `ss -tlnp | grep :8090` → `kill <pid>` (и родительский
   `go run`), затем `./bin/pb serve` (бинарь собран:
   `go build -o bin/pb ./cmd/pb`). Smoke-тест нового API на
   8091 выполнен 2026-08-28: `/api/zones` total=7921, источники
   «GeoBoundaries / ISTAT (2023)», parent_name у муниципалитетов
   заполнен; `/api/objects` total=7762, `zone_id: null` честно.
2. **WYSIWYG-проверка иерархии** (read-only):
   `pb zones list -country IT -level municipality -limit 10` —
   у каждого муниципалитета стоит parent-регион.
3. **Данные OMI** (нужен аккаунт Fisconline/Entratel пользователя):
   выгрузить периметры зон + котировки для Италии, затем:
   `pb zones import -file OMI-zones.geojson -country IT -source "Agenzia Entrate - OMI"`
   → `pb zones link -country IT -level zone`
   → `pb zones quotazioni -file OMI-quotazioni.csv -country IT`.
   При несовпадении реального CSV с зафиксированным форматом —
   ошибка импорта покажет строки/колонки; подстройка — в
   quotazioni.go.
4. **Воспроизведение импорта гео-границ** (файлы в gitignored
   `data/zones/`):
   ```
   mkdir -p data/zones && cd data/zones
   curl -fsSL -O https://github.com/wmgeolab/geoBoundaries/raw/9469f09/releaseData/gbOpen/ITA/ADM2/geoBoundaries-ITA-ADM2_simplified.geojson
   curl -fsSL -O https://github.com/wmgeolab/geoBoundaries/raw/9469f09/releaseData/gbOpen/ITA/ADM4/geoBoundaries-ITA-ADM4_simplified.geojson
   jq --arg level region '{type:"FeatureCollection",features:[.features[]|{type:"Feature",geometry:.geometry,properties:{level:$level,name:.properties.shapeName,external_code:.properties.shapeID}}]}' \
     geoBoundaries-ITA-ADM2_simplified.geojson > IT_regions.geojson
   jq --arg level municipality '{type:"FeatureCollection",features:[.features[]|{type:"Feature",geometry:.geometry,properties:{level:$level,name:.properties.shapeName,external_code:.properties.shapeID}}]}' \
     geoBoundaries-ITA-ADM4_simplified.geojson > IT_municipalities.geojson
   cd ../..
   pb zones import -file data/zones/IT_regions.geojson -country IT -source "GeoBoundaries / ISTAT (2023)"
   pb zones import -file data/zones/IT_municipalities.geojson -country IT -source "GeoBoundaries / ISTAT (2023)"
   pb zones link -country IT -level municipality
   ```
