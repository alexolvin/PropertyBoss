-- C36: намеренная остановка источника — отдельное состояние paused,
-- не active без плана. Чек doctor даёт INFO с датой и причиной;
-- FAIL остаётся только для настоящего противоречия (active без плана).
-- Имя sources_state_check — авто от inline CHECK в 0006_sources.sql.
ALTER TABLE sources DROP CONSTRAINT sources_state_check;
ALTER TABLE sources ADD CONSTRAINT sources_state_check
    CHECK (state IN ('active', 'cooldown', 'disabled', 'blocked', 'paused'));
ALTER TABLE sources ADD COLUMN paused_at TIMESTAMPTZ;
ALTER TABLE sources ADD COLUMN pause_reason TEXT;
