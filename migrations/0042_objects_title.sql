-- Название объявления (заголовок площадки), отдельно от адреса.
ALTER TABLE objects ADD COLUMN IF NOT EXISTS title TEXT;
COMMENT ON COLUMN objects.title IS 'Заголовок объявления на площадке (не адрес).';
