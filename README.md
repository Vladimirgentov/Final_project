# Финальный проект (сложный уровень)

REST API сервис для загрузки и выгрузки данных о ценах. Реализация на Go, СУБД PostgreSQL.

Порты по умолчанию:

* PostgreSQL: **5432**
* Go-сервис: **8080**

## Технологии

* Go (net/http)
* PostgreSQL
* Docker/Docker Compose
* GitHub Actions (CI)
* Yandex Cloud CLI (для `scripts/run.sh`)

## Структура данных

Таблица: `prices`

Колонки (CSV):

`id,name,category,price,create_date`

* `id` — строковый идентификатор продукта
* `name` — название
* `category` — категория
* `price` — цена (десятичное число; при сохранении конвертируется в «центы»)
* `create_date` — дата в формате `YYYY-MM-DD`

## Эндпоинты

### POST /api/v0/prices?type=zip|tar

Загружает архив (`zip` по умолчанию, либо `tar`) с файлом `data.csv`, валидирует строки и построчно пишет в БД.

Возвращает JSON:

```json
{
  "total_count": 123,
  "duplicates_count": 20,
  "total_items": 100,
  "total_categories": 15,
  "total_price": 100000
}
```

Пояснение:

* `total_count` — количество строк в исходном файле (без заголовка)
* `duplicates_count` — количество некорректных строк, дубликатов во входных данных и/или в БД
* `total_items` — сколько строк фактически добавлено в БД
* `total_categories` — количество уникальных категорий в БД
* `total_price` — суммарная стоимость по всем объектам в БД (в «центах»)

### GET /api/v0/prices?start=YYYY-MM-DD&end=YYYY-MM-DD&min=N&max=N

Возвращает `zip`-архив с файлом `data.csv`, содержащим записи из БД, отфильтрованные по:

* `start` / `end` — диапазон дат (включительно)
* `min` / `max` — диапазон цен (целые числа, **натуральные**, в «рублях/единицах»; внутри сервиса конвертируются в «центы»)

## Переменные окружения

Сервис читает параметры подключения к Postgres из переменных:

* `POSTGRES_HOST` (по умолчанию `127.0.0.1`)
* `POSTGRES_PORT` (по умолчанию `5432`)
* `POSTGRES_USER` (по умолчанию `validator`)
* `POSTGRES_PASSWORD` (по умолчанию `val1dat0r`)
* `POSTGRES_DB` (по умолчанию `project-sem-1`)

HTTP-адрес:

* `HTTP_ADDR` (по умолчанию `:8080`)

## Скрипты

Все скрипты находятся в `scripts/`.

### scripts/prepare.sh

Собирает Docker-образ приложения.

```bash
chmod +x scripts/prepare.sh
./scripts/prepare.sh
```

Опционально:

* `IMAGE_NAME` (по умолчанию `final_project-app`)
* `IMAGE_TAG` (по умолчанию `latest`)

### scripts/run.sh (Yandex Cloud)

Создаёт VM через YC CLI и разворачивает приложение + PostgreSQL через Docker Compose по SSH.
Скрипт выводит **IP-адрес** созданного сервера в stdout.

Зависимости: `yc`, `ssh`, `scp`, `tar`, `jq`.

Минимальные переменные окружения:

* `YC_CLOUD_ID`, `YC_FOLDER_ID`, `YC_ZONE`, `YC_SUBNET_ID`
* `SSH_PUBLIC_KEY` (путь к публичному ключу)
* `SSH_PRIVATE_KEY` (путь к приватному ключу)

Пример:

```bash
chmod +x scripts/run.sh
export YC_CLOUD_ID=...
export YC_FOLDER_ID=...
export YC_ZONE=ru-central1-a
export YC_SUBNET_ID=...
export SSH_PUBLIC_KEY=$HOME/.ssh/id_rsa.pub
export SSH_PRIVATE_KEY=$HOME/.ssh/id_rsa

./scripts/run.sh
```

### scripts/tests.sh

Запускает API-тесты (POST zip/tar и GET с фильтрами). По умолчанию работает с `http://localhost:8080`.

```bash
chmod +x scripts/tests.sh
./scripts/tests.sh
```

Опционально:

* `BASE_URL` — базовый URL сервиса
* `ZIP_FILE` — путь к zip-архиву (по умолчанию `sample_data.zip`)

## Локальный запуск (Docker Compose)

```bash
docker compose up -d --build
curl -fsS http://localhost:8080/health
./scripts/tests.sh
docker compose down -v
```
