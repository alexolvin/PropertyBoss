-- TZ v4 §4: расширение слоя данных.
-- 1) deal_type += 'auction' (ТЗ v4 §4.6: торги — отдельный тип сделки).
-- 2) objects: геокодирование, продавец, детальный проход, аукционные поля.
-- 3) attribute_values: источник (provenance) значений атрибутов (ТЗ v4 §4.2).
--    Сами значения живут в objects.attributes (для area_sqm/rooms — в колонках
--    objects) и не дублируются здесь; таблица хранит, ЧТО ЭТО ЗА ЗНАЧЕНИЕ
--    (метод, уверенность, модель, версия списка атрибутов), — ТЗ v4 §4.2–4.3.
-- 4) object_photos: фотографии (ТЗ v4 §4.4): оригинальная ссылка + два
--    локальных уменьшенных размера (карта-карточка, список).

-- 1) Торги — отдельный тип сделки.
ALTER TABLE objects DROP CONSTRAINT objects_deal_type_chk;
ALTER TABLE objects ADD CONSTRAINT objects_deal_type_chk
    CHECK (deal_type IN ('sale', 'rent', 'auction'));
ALTER TABLE search_configs DROP CONSTRAINT search_configs_deal_type_chk;
ALTER TABLE search_configs ADD CONSTRAINT search_configs_deal_type_chk
    CHECK (deal_type IN ('sale', 'rent', 'auction'));
ALTER TABLE zone_reference_prices DROP CONSTRAINT zrps_deal_type_chk;
ALTER TABLE zone_reference_prices ADD CONSTRAINT zrps_deal_type_chk
    CHECK (deal_type IN ('sale', 'rent', 'auction'));

-- 2) objects: новые колонки.
-- geo_confidence — уровень уверенности координат (ТЗ v4 §4.5):
--   building — здание; street — улица; municipality — населённый пункт;
--   postcode — почтовый индекс; zone — зона (только полигон).
-- geo_source — откуда координаты: source — площадка (ссылка «Přibližná
--   lokalita» с zoom'ом); geocoder — геокодер; zone — центр зоны.
ALTER TABLE objects
    ADD COLUMN geo_confidence TEXT
        CHECK (geo_confidence IN ('building', 'street', 'municipality', 'postcode', 'zone')),
    ADD COLUMN geo_source TEXT
        CHECK (geo_source IN ('source', 'geocoder', 'zone')),
    -- Тип продавца (ТЗ v4 §4.7); NULL — не установлено (угадывать запрещено).
    ADD COLUMN seller_type TEXT
        CHECK (seller_type IN ('private', 'agency', 'developer', 'auction_organizer')),
    ADD COLUMN seller_name TEXT,
    -- Детальный проход: когда страницу объявления последнего раз разобрали
    -- и хэш её содержимого (обнаружение изменений, идемпотентность).
    ADD COLUMN detail_fetched_at TIMESTAMPTZ,
    ADD COLUMN detail_hash CHAR(64),
    -- Обременения (ТЗ v4 §4.1, §4.6): NULL = неизвестно;
    -- has_encumbrances = FALSE — источник явно сообщил «без обременений»;
    -- TRUE — описание в encumbrances.
    ADD COLUMN has_encumbrances BOOLEAN,
    ADD COLUMN encumbrances TEXT,
    -- Аукционные поля (ТЗ v4 §4.6): старт в current_price_minor,
    -- экспертная оценка — отдельно; hammer price — в auction_result_*.
    ADD COLUMN auction_date TIMESTAMPTZ,
    ADD COLUMN auction_status TEXT
        CHECK (auction_status IN ('upcoming', 'finished', 'sold', 'withdrawn')),
    ADD COLUMN auction_expert_valuation_minor BIGINT,
    ADD COLUMN auction_valuation_currency CHAR(3) REFERENCES currencies(code),
    ADD COLUMN auction_result_minor BIGINT,
    ADD COLUMN auction_result_currency CHAR(3) REFERENCES currencies(code);

-- Бэкфилл детального прохода: «ещё не обработаны» — частый запрос.
CREATE INDEX objects_detail_unfetched_idx ON objects (id) WHERE detail_fetched_at IS NULL;
CREATE INDEX objects_geo_confidence_idx ON objects (geo_confidence);
CREATE INDEX objects_seller_type_idx ON objects (seller_type);

-- 3) Provenance атрибутов (ТЗ v4 §4.2: источник каждого значения
--    фиксируется; §4.3: value+confidence+model+версия списка+время).
CREATE TABLE attribute_values (
    object_id BIGINT NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
    attr_key TEXT NOT NULL,
    extracted_by TEXT NOT NULL
        CHECK (extracted_by IN ('source_field', 'rule', 'llm_extraction')),
    confidence NUMERIC(4,3) NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
    model TEXT,            -- llm_extraction: имя+версия модели
    attr_list_version TEXT,-- версия списка атрибутов (реестр)
    source_hash CHAR(64),  -- sha256 текста объявления (идемпотентность)
    extracted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (object_id, attr_key)
);
CREATE INDEX attribute_values_key_idx ON attribute_values (attr_key);
CREATE INDEX attribute_values_method_idx ON attribute_values (extracted_by);

-- 4) Фотографии (ТЗ v4 §4.4). ord — порядок на площадке (первый = обложка).
-- file_* — пути относительно data-каталога; NULL — локальная копия не скачана.
CREATE TABLE object_photos (
    id BIGSERIAL PRIMARY KEY,
    object_id BIGINT NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
    ord SMALLINT NOT NULL CHECK (ord >= 1),
    original_url TEXT NOT NULL,
    file_card TEXT,
    bytes_card INTEGER,
    file_list TEXT,
    bytes_list INTEGER,
    UNIQUE (object_id, ord)
);
CREATE INDEX object_photos_object_idx ON object_photos (object_id);
