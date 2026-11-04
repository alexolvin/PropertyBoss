# PropertyBoss

**Языки:** [English](README.md) | Русский

<p align="left">
  <img alt="License" src="https://img.shields.io/badge/license-MIT-blue.svg">
</p>

PropertyBoss следит за публичными объявлениями о недвижимости, оценивает отклонение цены от местного рынка и вероятность того, что объявление уйдёт с рынка.

## Возможности

- Сканирование чешских площадок, для которых есть коннектор (Bazos, UlovDomov, Bezrealitky)
- Рынки в конфигурации: Чехия, Италия, Нидерланды
- Гедоническая оценка рыночной цены
- Модель ликвидности: вероятность ухода с рынка, а не совершённой продажи
- Детекция ушедших объявлений с защитой от массового исчезновения за один прогон
- Необязательные уведомления в Telegram
- Локальный дашборд (список, карта, объект, зоны, модели) на русском и английском
- Курсы ЕЦБ; деньги хранятся целыми числами в минорных единицах
- Необязательные перевод и извлечение атрибутов через OpenAI-совместимый API

## Требования

- Go 1.25 или новее
- PostgreSQL 16 с PostGIS
- Node.js 20 или новее и npm, чтобы собрать дашборд
- Сеть до площадок и до фида курсов ЕЦБ, когда эти задания запускаются

Достаточно процессора ноутбука. Графический ускоритель не нужен. Место на диске растёт вместе с базой и, если включена загрузка фотографий, вместе с локальным кэшем `data/photos` (этот каталог не входит в git).

## Быстрый старт

```sh
git clone https://github.com/alexolvin/PropertyBoss.git
cd PropertyBoss
cp config/config.example.yaml config/config.yaml
cp config/secrets.example.yaml config/secrets.yaml
# В config/secrets.yaml задайте database.dsn.
# Токены оставьте пустыми, пока не нужны Telegram или LLM.

go run ./cmd/pb migrate
go run ./cmd/pb fx sync

cd web && npm ci && npm run build && cd ..
go run ./cmd/pb serve
```

API и дашборд слушают `dashboard.listen` (по умолчанию `http://127.0.0.1:8090`). У API нет аутентификации. Не привязывайте его к публичному интерфейсу.

## Конфигурация

Скопируйте файлы-примеры. `config/config.yaml` и `config/secrets.yaml` перечислены в `.gitignore`.

| Имя | Назначение | Пример | Обязательно |
|---|---|---|---|
| `database.dsn` | Строка подключения PostgreSQL. Только в `config/secrets.yaml`. | `postgres://propertyboss:CHANGE_ME@127.0.0.1:5432/propertyboss?sslmode=disable` | да |
| `dashboard.listen` | Адрес API и интерфейса | `127.0.0.1:8090` | да |
| `dashboard.ui` | Каталог собранного фронтенда | `web/dist` | нет; пусто — только JSON |
| `PB_CONFIG` | Другой путь к YAML-конфигу | `config/config.yaml` | нет |
| `telegram.token` | Токен бота, в `config/secrets.yaml` | пусто | нет |
| `telegram.chat_id` | Идентификатор чата оператора, в `config.yaml` | пусто | нет |
| `translate.api_key` | Ключ LLM для переводов, в `config/secrets.yaml` | пусто | нет |
| `extract.api_key` | Ключ LLM для извлечения атрибутов, в `config/secrets.yaml` | пусто | нет |

Пороги оценки, ликвидности, делистинга и расписания лежат в `config/config.example.yaml`.

## Использование

```sh
go run ./cmd/pb scan -list
go run ./cmd/pb serve
go run ./cmd/pb notify status
go run ./cmd/pb doctor web
```

`pb doctor web` проверяет, что `web/dist` собран из текущего дерева git. `make ui` пересобирает интерфейс, поднимает разовый сервер на `127.0.0.1:8091` и запускает набор Playwright. Для этой цели нужны `config/config.yaml`, `config/secrets.yaml` и доступная база.

Полигоны зон импортируются командой `pb zones import` из вашего файла GeoJSON. Такие файлы остаются вне git (каталог `data/` игнорируется).

Фотографии объявлений хранятся для собственного использования оператора. Этот репозиторий не даёт права распространять их дальше.

Снимки проб в `artifacts/` остаются на машине, где проба запускалась. Каталог в `.gitignore`: в нём есть имя машины и фрагменты чужих страниц. `pb sources report` пишет `docs/sources/CZ_REGISTRY.md` и не записывает имя машины.

## Структура проекта

```text
cmd/pb/            командная строка
internal/          сервер, коннекторы, модели
migrations/        SQL-миграции
config/            примеры конфигурации
web/               дашборд на React
docs/              ограничения и журнал дефектов
```

## Тестирование

```sh
go test ./...
```

Тесты, которым нужен PostgreSQL, пропускаются, пока `PB_TEST_DSN` не называет одноразовую базу.

Тесты дашборда при уже запущенном API:

```sh
cd web && npm ci && npx playwright install chromium
PB_UI_BASE_URL=http://127.0.0.1:8090 npm run test:ui
```

## Безопасность

Секреты не коммитятся. Об уязвимостях сообщайте так, как описано в [SECURITY.md](SECURITY.md).

## Участие

См. [CONTRIBUTING.ru.md](CONTRIBUTING.ru.md). Журнал изменений: [CHANGELOG.md](CHANGELOG.md). Нумерованный тег версии ещё не опубликован.

## Лицензия

Проект распространяется по лицензии MIT. Текст лицензии: [LICENSE](LICENSE).

Значки PWA в `web/public/icons/` генерирует `web/scripts/gen-icons.mjs`; они входят в состав программы и распространяются по той же лицензии MIT. Фотографии объявлений и текст чужих страниц в эту лицензию не входят.
