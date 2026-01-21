# Финальный проект (сложный уровень)

REST API сервис для загрузки и выгрузки данных о ценах товаров.

Проект реализован на **Go**, использует **PostgreSQL**, запускается в **Docker** и автоматически разворачивается в **Yandex Cloud** с помощью **GitHub Actions (CI/CD)**.

---

## Используемые технологии

- Go 1.21+
- PostgreSQL 15+
- Docker и Docker Compose
- Yandex Cloud CLI (yc)
- GitHub Actions (CI/CD)

---

## Сетевые порты

- PostgreSQL: `5432`
- REST API сервер: `8080`

---

## Конфигурация базы данных

Параметры по умолчанию (используются в docker-compose и сервисе):

| Параметр | Значение |
|----------|-----------|
| Пользователь | `validator` |
| Пароль | `val1dat0r` |
| База данных | `project-sem-1` |
| Таблица | `prices` |

Таблица `prices` создаётся автоматически при инициализации контейнера PostgreSQL (см. файл `db/10-init.sql`).

---

## API эндпоинты (сложный уровень)

### 1. POST `/api/v0/prices?type=zip|tar`

Загружает архив с CSV‑данными и построчно записывает корректные записи в базу данных.

**Параметр запроса:**

- `type` — тип архива: `zip` или `tar` (по умолчанию `zip`)

**Тело запроса:**

- бинарный архив с CSV‑файлом

**Валидация данных:**

- проверка дубликатов (во входных данных и в БД)
- проверка формата
- проверка полноты данных

**Пример ответа:**

```json
{
  "total_count": 123,
  "duplicates_count": 20,
  "total_items": 100,
  "total_categories": 15,
  "total_price": 100000
}
```

---

### 2. GET `/api/v0/prices?start=YYYY-MM-DD&end=YYYY-MM-DD&min=N&max=N`

Возвращает ZIP‑архив с файлом `data.csv`, содержащим записи из базы данных, отфильтрованные по параметрам.

**Параметры:**

- `start` — минимальная дата создания
- `end` — максимальная дата создания
- `min` — минимальная цена (> 0)
- `max` — максимальная цена (> 0)

**Ответ:**

- ZIP‑архив с файлом `data.csv`

---

## Локальный запуск (Docker)

### Сборка образа

```bash
./scripts/prepare.sh
```

### Запуск сервиса и базы данных

```bash
./scripts/run.sh
```

### Запуск автотестов API

```bash
./scripts/tests.sh
```

### Проверка работоспособности

```bash
curl http://localhost:8080/health
```

---

## Пример использования API (Windows PowerShell)

### Загрузка архива

```powershell
curl.exe -X POST "http://localhost:8080/api/v0/prices?type=zip" --data-binary "@sample_data.zip"
```

### Получение ZIP‑архива с CSV

```powershell
curl.exe -L "http://localhost:8080/api/v0/prices?start=2024-01-01&end=2024-01-31&min=300&max=1000" -o out.zip
Expand-Archive -Path .\out.zip -DestinationPath .\out -Force
Get-Content .\out\data.csv -TotalCount 10
```

---

## Деплой в Yandex Cloud (сложный уровень)

Развёртывание полностью автоматизировано через GitHub Actions.

Файл workflow:

```
.github/workflows/yc-deploy.yml
```

Этапы пайплайна:

1. Сборка Docker‑образа
2. Аутентификация в Yandex Cloud через сервисный аккаунт
3. Создание виртуальной машины
4. Установка Docker на VM
5. Копирование проекта
6. Запуск через docker-compose
7. Запуск автотестов API

Пайплайн запускается:

- при push в ветку `main`
- вручную через GitHub Actions (workflow_dispatch)

---

## Переменные окружения для деплоя

Файл `.env.yc`:

```bash
YC_CLOUD_ID
YC_FOLDER_ID
YC_ZONE=ru-central1-b
YC_SUBNET_ID
VM_USER=ubuntu
```

Secrets в GitHub Actions:

- `YC_SA_KEY_JSON` — ключ сервисного аккаунта
- `SSH_PRIVATE_KEY_B64` — приватный SSH‑ключ в формате base64

---

## Структура проекта

```
.
├── main.go
├── Dockerfile
├── docker-compose.yml
├── db/
│   └── 10-init.sql
├── scripts/
│   ├── prepare.sh
│   ├── run.sh
│   └── tests.sh
├── sample_data.zip
└── README.md
```

---

## Примечания

- GET‑эндпоинт возвращает бинарный ZIP‑архив — это ожидаемое поведение.
- Повторная загрузка одного и того же архива приведёт к увеличению `duplicates_count`.
- После проверки проектную VM рекомендуется удалить, чтобы избежать расходов:

```bash
yc compute instance delete --name final-project-sem1
```

---

## Автор

Финальный проект первого семестра — сложный уровень

