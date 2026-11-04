-- C19.3 (D17): namespace очереди уведомлений.
-- Production-флош читает строки queue_ns='prod'; тесты работают в
-- собственных namespace — тест, читающий production-строки, это
-- утечка, а не удачный способ находить баги (C19: live-тест нашёл
-- D17 именно так, но сам по себе негерметичен).
ALTER TABLE notifications ADD COLUMN queue_ns TEXT NOT NULL DEFAULT 'prod';

-- Индекс pending теперь с namespace: флош видит только свою очередь.
DROP INDEX notifications_pending_idx;
CREATE INDEX notifications_pending_idx ON notifications (queue_ns, id)
    WHERE status = 'pending';
