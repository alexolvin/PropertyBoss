-- Очередь задач для phone-agent (ТЗ §2: связь по pull).
-- Статусы: queued → pulled (телефон забрал) → running → done | failed.
CREATE TABLE phone_tasks (
    id BIGSERIAL PRIMARY KEY,
    kind TEXT NOT NULL,
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'pulled', 'running', 'done', 'failed', 'expired')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    pulled_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    result JSONB,
    error TEXT
);
CREATE INDEX phone_tasks_pending_idx ON phone_tasks (id)
    WHERE status IN ('queued', 'pulled', 'running');

-- Уведомления (ТЗ: Telegram — только уведомления).
-- payload обязан содержать интервал и размер выборки, а не голое число (этап 8).
CREATE TABLE notifications (
    id BIGSERIAL PRIMARY KEY,
    channel TEXT NOT NULL DEFAULT 'telegram',
    recipient TEXT NOT NULL,
    kind TEXT NOT NULL,
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed')),
    error TEXT,
    sent_at TIMESTAMPTZ
);
CREATE INDEX notifications_pending_idx ON notifications (id) WHERE status = 'pending';
