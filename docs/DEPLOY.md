# Развертывание на VPS

Полный путь от чистого сервера до работающего Mini App.

Для хостинга Bothost используйте отдельный [гайд](BOTHOST.md).

## Что нужно заранее

- VPS с Linux и установленным Docker (включая плагин Compose).
- Домен, A-запись которого указывает на IP этого VPS.
- Свободные порты 80 и 443 — их займет Caddy для автоматического HTTPS.
- Токен бота от [@BotFather](https://t.me/BotFather) (`/newbot`). Для приложения заводите **отдельного** бота.

Telegram Mini App работает только по HTTPS, поэтому домен обязателен — по IP приложение не откроется.

## 1. Установить Docker (если его нет)

```bash
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER   # перелогиньтесь после этой команды
docker compose version
```

## 2. Забрать код

```bash
git clone https://github.com/nikitabaskov/vkurilke.git
cd vkurilke
```

## 3. Заполнить `.env`

```bash
cp .env.example .env
nano .env
```

| Переменная | Что вписать |
| --- | --- |
| `BOT_TOKEN` | Токен от BotFather |
| `BASE_URL` | `https://smoke.example.com` — ваш домен со схемой, без пути и слеша в конце |
| `DOMAIN` | `smoke.example.com` — тот же домен без схемы, для Caddy |
| `ADMIN_IDS` | Ваш **числовой** Telegram User ID. Не username и не ID бота. Несколько — через запятую |
| `PORT` | Порт на хосте, по умолчанию `8080`. Внутри контейнера всегда 8080 |
| `CHECK_INTERVAL_MINUTES` | Через сколько минут бот спрашивает «все еще в курилке?» (по умолчанию 15) |
| `ANSWER_TIMEOUT_MINUTES` | Сколько ждать ответа на проверку (по умолчанию 3) |

`DB_PATH` в Compose переопределяется на `/data/vkurilke.db` — менять его в `.env` не нужно.

Не знаете свой ID — оставьте `ADMIN_IDS` пустым, запустите сервис, отправьте боту `/id`, впишите полученное число и выполните шаг 4 еще раз.

## 4. Запустить

```bash
docker compose --profile https up -d --build
docker compose logs -f app
```

В логах должна появиться строка `ВКурилке started` с именем бота. Caddy сам выпустит сертификат Let's Encrypt для `DOMAIN`.

Проверка живости:

```bash
curl https://smoke.example.com/healthz   # {"ok":true}
```

## 5. Создать первую комнату

1. Отправьте боту `/start`.
2. Откройте Mini App кнопкой меню «ВКурилке 🚬» у поля ввода.
3. Как администратор приложения создайте комнату (например, «Общага»).
4. В настройках комнаты скопируйте ссылку-приглашение и отдайте ее участникам. Они переходят по ссылке, запускают бота и открывают приложение.

Бот сам ставит кнопку меню и список команд. Дополнительно в BotFather можно указать Main Mini App с адресом `BASE_URL` — тогда появится кнопка запуска прямо в профиле бота ([документация Telegram](https://core.telegram.org/bots/webapps#launching-the-main-mini-app)).

## Свой reverse proxy вместо Caddy

Если HTTPS на хосте уже обслуживает Nginx или свой Caddy, запускайте без профиля `https`:

```bash
docker compose up -d --build
```

Приложение слушает `127.0.0.1:${PORT}` — направьте туда proxy_pass. Mini App и API обязаны быть на одном origin, совпадающем с `BASE_URL`.

## Обновление версии

```bash
git pull
docker compose --profile https up -d --build
```

Данные в volume `app_data` переживают пересборку.

## Данные и резервные копии

SQLite лежит в Docker volume `app_data`. `docker compose down` данные сохраняет, **`docker compose down -v` удаляет их безвозвратно**.

Согласованная копия (WAL нельзя копировать по одному файлу на работающем сервисе):

```bash
docker compose stop app
docker run --rm -v vkurilke_app_data:/data -v "$PWD":/backup alpine \
  tar czf /backup/vkurilke-$(date +%F).tar.gz -C /data .
docker compose start app
```

Восстановление — та же команда с `tar xzf` в пустой volume при остановленном `app`. Имя volume собирается из имени каталога проекта; проверить точное имя: `docker volume ls`.

## Эксплуатация

```bash
docker compose logs -f app         # ошибки Telegram и повторные отправки
docker compose ps                  # состояние и healthcheck
docker compose restart app         # применить новые интервалы из .env
```

- Запускайте **ровно одну реплику**: Telegram long polling и локальный SQLite не масштабируются горизонтально.
- Новые значения `CHECK_INTERVAL_MINUTES` и `ANSWER_TIMEOUT_MINUTES` применяются после перезапуска; уже назначенные дедлайны доживают до следующего перехода.
- Если пользователь заблокировал бота, его статус выключается — проверить присутствие больше нечем. Повторный `/start` восстанавливает канал.

## Типичные проблемы

| Симптом | Причина и решение |
| --- | --- |
| Контейнер падает с `BASE_URL must be an HTTPS origin` | В `BASE_URL` нет `https://`, есть путь или слеш в конце |
| Контейнер падает с `BOT_TOKEN is required` | `.env` не заполнен или Compose запущен не из каталога с `.env` |
| Caddy не выпускает сертификат | DNS еще не указывает на VPS, либо порты 80/443 заняты (`sudo ss -lntp`) |
| Mini App открывается, но сразу «Откройте приложение заново» | `BASE_URL` не совпадает с доменом, с которого открыт Mini App |
| Кнопка статуса отвечает «Сначала запустите бота» | Пользователь не нажимал `/start` — без личного чата проверку присутствия доставить некуда |
| Бот не отвечает | Тот же токен используется другим запущенным экземпляром или webhook'ом |
