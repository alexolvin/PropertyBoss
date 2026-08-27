# PropertyBoss

Мониторинг цен на недвижимость (Чехия, Италия, Нидерланды) с оценкой отклонения
от рынка и вероятности ухода объекта с рынка.

Техническое задание: [`.tasks/PropertyBoss_TZ_v2.md`](.tasks/PropertyBoss_TZ_v2.md).
Handoff-документы: [`./.handoff/`](.handoff/).

## Стек

- Backend: Go 1.25 (ТЗ требует 1.22+)
- БД: PostgreSQL 16 + PostGIS (на `vzu5-claw`)
- Frontend: React + Vite (этап 2)
- Уведомления: Telegram Bot API (этап 8)
- Phone-agent: OpenClaw (этап 9)

## Быстрый старт

```sh
# 1. Конфиг (DSN с паролем пользователя propertyboss)
cp config/config.example.yaml config/config.yaml

# 2. Миграции (каталог migrations/, порядок по именам)
go run ./cmd/pb migrate

# 3. Курсы ЕЦБ → fx_rates (окно backfill_days дней, идемпотентно)
go run ./cmd/pb fx sync

# 4. API-сервер дашборда (порт = dashboard.listen, по умолчанию 127.0.0.1:8090)
go run ./cmd/pb serve
```

Порты: API-сервер — `dashboard.listen` из `config/config.yaml`
(по умолчанию **127.0.0.1:8090**; на `vzu5-claw` порт 8080 занят другим
сервисом, проверено 2026-08-25). Dev-сервер фронтенда — Vite на 5173,
проксирует `/api` на тот же порт backend (см. `web/vite.config.ts`).

## Структура

```
cmd/pb/               бинарь: migrate | fx sync | serve (этап 2)
internal/config/      YAML-конфиг с валидацией
internal/db/          пул pgx + раннер миграций (schema_migrations)
internal/money/       деньги: int64 в минорных единицах, конвертация через big.Rat (без float, ТЗ §5)
internal/fx/          клиент XML-фида ЕЦБ + загрузчик в fx_rates
internal/api/         REST API дашборда (этап 2): meta, search-configs, objects
migrations/           SQL-миграции (0001…0016, схема ТЗ §12)
config/               конфиги (config.yaml — в .gitignore)
web/                  React + Vite + TS дашборд (этап 2), i18n ru/en
```

## Деньги (ТЗ §5)

Суммы — `BIGINT` в минорных единицах + `currency CHAR(3)`; float для денег
запрещён на всех уровнях. В Go — пакет `internal/money` (int64 + точная
рациональная конвертация). Курсы — `NUMERIC(20,10)`, фиксируются на дату
наблюдения; при отсутствии курса на дату функция `fx_rate_for()` возвращает
последний известный с пометкой `stale`.

## Статус этапов

| Этап | Статус |
|---|---|
| 1. Каркас, миграции, БД, курсы ЕЦБ | ✅ завершён ([отчёт](.handoff/stage1-report.md)) |
| 2. Дашборд v1 | ✅ завершён ([отчёт](.handoff/stage2-report.md)) |
| 3. Реестр атрибутов + коннектор | ✅ завершён ([отчёт](.handoff/stage3-report.md)) |
| 4. Зоны OMI | не начат |
| 5. Гедоническая модель | не начат |
| 6. delisted-логика | не начат |
| 7. Модель ликвидности | не начат |
| 8. Telegram-бот | не начат (нужен токен) |
| 9. Phone-agent | не начат (нужен телефон) |
| 10. Переводчик | не начат (нужен API-ключ LLM) |
| 11. Адаптивное расписание | не начат |
| 12. Подготовка vzu5-omi (phantom killer, termux-boot) | не начат |
| 13. Перенос всей системы на vzu5-omi | не начат |
| 14. Резервное копирование на vzu5-claw + проверка восстановления | не начат |
