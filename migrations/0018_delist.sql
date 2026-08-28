-- Этап 6 (ТЗ §8.2): защита при маркировке delisted.
--
-- url_check_allowed — разрешён ли источнику прямой URL-чек объявления
-- перед delisted (ТЗ §8.2, защита 4: «если источник позволяет»).
-- По умолчанию FALSE: без явной пометки delist-пасс никаких доп.
-- запросов к площадке не делает.
ALTER TABLE sources ADD COLUMN url_check_allowed BOOLEAN NOT NULL DEFAULT FALSE;

-- Bazoš: страницы объявлений публичные, прямой чек разрешён.
UPDATE sources SET url_check_allowed = TRUE WHERE id = 'bazos-reality';
