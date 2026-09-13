# ВКурилке 🚬

Telegram-бот и Mini App для своей компании: одна большая кнопка отмечает, что ты в курилке, остальные видят, кто там сейчас и сколько уже стоит.

Один Go-процесс, SQLite и встроенная статика. Без Redis, без отдельного Node-сервера, без CGO.

[Темная тема](docs/preview-dark.png) · [Светлая тема](docs/preview-light.png)

## Возможности

- Вход только по приглашению, несколько комнат, один активный статус на человека.
- Кнопка «Я в курилке» / «Я вернулся», счетчик времени, «Спускаюсь (+1)» в боте.
- Проверка присутствия через 15 минут — без ответа статус выключается сам.
- Одна обновляемая карточка сеанса в личном чате вместо потока сообщений.
- Роли владельца и администратора: приглашения, исключение, повторное добавление.
- Настройки уведомлений о начале сеанса, приходах и уходах.
- Светлая и темная темы Telegram, аватары, тактильный отклик.
- Таймеры, очередь доставки и авторизация переживают перезапуск.

## Быстрый старт на VPS

Нужны Docker с Compose, домен на этот сервер и токен от [@BotFather](https://t.me/BotFather).

```bash
git clone https://github.com/nikitabaskov/vkurilke.git
cd vkurilke
cp .env.example .env   # BOT_TOKEN, BASE_URL, DOMAIN, ADMIN_IDS
docker compose --profile https up -d --build
```

Дальше — `/start` боту и создание первой комнаты.

**Подробный гайд со всеми шагами, бэкапами и разбором типичных ошибок: [docs/DEPLOY.md](docs/DEPLOY.md).**

## Развертывание на Bothost

Используйте корневой `Dockerfile`, включите HTTPS-домен и порт `8080`, задайте `BOT_TOKEN`, `BASE_URL`, `ADMIN_IDS` и `DB_PATH=/app/data/vkurilke.db` в панели. Для Mini App нужен тариф с веб-доменом и постоянным хранилищем.

**Пошаговый гайд: [docs/BOTHOST.md](docs/BOTHOST.md).**

## Локальная разработка

Нужны Go 1.25+ и Node.js 22.12+.

```bash
make check   # сборка фронтенда, типы, go vet, тесты с -race
make run     # собрать и запустить
```

`web/embed.go` встраивает `web/dist`, поэтому на свежем клоне сначала `make web`, иначе команды `go` упадут.

Как присылать пул-реквесты: [CONTRIBUTING.md](CONTRIBUTING.md).

## Документация

| Файл | О чем |
| --- | --- |
| [docs/DEPLOY.md](docs/DEPLOY.md) | Развертывание на VPS, обновление, бэкапы, эксплуатация |
| [docs/BOTHOST.md](docs/BOTHOST.md) | Развертывание на Bothost, домен Mini App, SQLite и диагностика |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Локальная среда, правила PR, стиль кода |
| [docs/API.md](docs/API.md) | HTTP API, авторизация, команды бота |
| [docs/DECISIONS.md](docs/DECISIONS.md) | Согласованная продуктовая логика — источник правды о поведении |

## Структура

```text
cmd/app/          запуск, остановка, HTTP-сервер
internal/config/  переменные окружения
internal/api/     Telegram auth, REST, проверка запросов
internal/store/   SQLite, права, статусы, сеансы, очередь
internal/bot/     Telegram API, команды, кнопки, карточки
internal/worker/  таймеры и доставка с повторными попытками
web/              TypeScript, Tailwind, Vite, go:embed
```

## Лицензия

[MIT](LICENSE)
