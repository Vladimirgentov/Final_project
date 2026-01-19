#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

IMAGE_NAME="${IMAGE_NAME:-prices-service}"
IMAGE_TAG="${IMAGE_TAG:-latest}"

NET_NAME="${NET_NAME:-prices-net}"
PG_CONTAINER="${PG_CONTAINER:-project-sem1-postgres}"
APP_CONTAINER="${APP_CONTAINER:-prices-app}"

HOST_PORT="${HOST_PORT:-18080}"
BASE_URL="http://127.0.0.1:${HOST_PORT}"

ZIP_FILE="${ROOT_DIR}/sample_data.zip"
SAMPLE_DIR="${ROOT_DIR}/sample_data"
TAR_FILE="${ROOT_DIR}/sample_data.tar"

require() {
  command -v "$1" >/dev/null 2>&1 || { echo "Missing dependency: $1"; exit 1; }
}

require docker
require curl
require jq
require unzip
require tar

cleanup() {
  docker rm -f "${APP_CONTAINER}" >/dev/null 2>&1 || true
  docker rm -f "${PG_CONTAINER}" >/dev/null 2>&1 || true
  docker network rm "${NET_NAME}" >/dev/null 2>&1 || true
  rm -f "${TAR_FILE}" "${ROOT_DIR}/out.zip" >/dev/null 2>&1 || true
}
trap cleanup EXIT

if [[ ! -f "${ZIP_FILE}" ]]; then
  echo "sample_data.zip not found at: ${ZIP_FILE}"
  exit 1
fi

# tar для type=tar
if [[ -d "${SAMPLE_DIR}" && -f "${SAMPLE_DIR}/data.csv" ]]; then
  tar -cf "${TAR_FILE}" -C "${SAMPLE_DIR}" data.csv
else
  echo "sample_data directory/data.csv not found: ${SAMPLE_DIR}/data.csv"
  exit 1
fi

echo "==> Creating docker network: ${NET_NAME}"
docker network create "${NET_NAME}" >/dev/null

echo "==> Starting Postgres container: ${PG_CONTAINER}"
docker run -d --name "${PG_CONTAINER}" --network "${NET_NAME}" \
  -e POSTGRES_USER=validator \
  -e POSTGRES_PASSWORD=val1dat0r \
  -e POSTGRES_DB=project-sem-1 \
  -p 5432:5432 \
  --health-cmd="pg_isready -U validator -d project-sem-1" \
  --health-interval=2s --health-timeout=2s --health-retries=30 \
  postgres:18 >/dev/null

echo "==> Waiting for Postgres to become healthy..."
for i in {1..60}; do
  status="$(docker inspect -f '{{.State.Health.Status}}' "${PG_CONTAINER}" 2>/dev/null || true)"
  if [[ "${status}" == "healthy" ]]; then
    break
  fi
  sleep 1
done
if [[ "$(docker inspect -f '{{.State.Health.Status}}' "${PG_CONTAINER}")" != "healthy" ]]; then
  echo "Postgres did not become healthy"
  docker logs "${PG_CONTAINER}" --tail 200 || true
  exit 1
fi

echo "==> Ensuring table prices exists"
docker exec -i "${PG_CONTAINER}" psql -U validator -d project-sem-1 <<'SQL'
CREATE TABLE IF NOT EXISTS prices (
  product_id   TEXT NOT NULL,
  created_at   DATE NOT NULL,
  name         TEXT NOT NULL,
  category     TEXT NOT NULL,
  price        INTEGER NOT NULL CHECK (price > 0),
  CONSTRAINT prices_uniq UNIQUE (product_id, created_at, name, category, price)
);
SQL

echo "==> Building service image"
bash "${ROOT_DIR}/scripts/prepare.sh"

echo "==> Starting app container: ${APP_CONTAINER}"
docker run -d --name "${APP_CONTAINER}" --network "${NET_NAME}" \
  -e POSTGRES_HOST="${PG_CONTAINER}" \
  -e POSTGRES_PORT=5432 \
  -e POSTGRES_USER=validator \
  -e POSTGRES_PASSWORD=val1dat0r \
  -e POSTGRES_DB=project-sem-1 \
  -e HTTP_ADDR=":8080" \
  -p "${HOST_PORT}:8080" \
  "${IMAGE_NAME}:${IMAGE_TAG}" >/dev/null

echo "==> Waiting for /health..."
for i in {1..60}; do
  if curl -fsS "${BASE_URL}/health" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
curl -fsS "${BASE_URL}/health" >/dev/null

echo "==> TRUNCATE prices"
docker exec -i "${PG_CONTAINER}" psql -U validator -d project-sem-1 -c "TRUNCATE prices;" >/dev/null

echo "==> POST zip (expect insert 12, dup 0)"
resp="$(curl -fsS -X POST "${BASE_URL}/api/v0/prices?type=zip" \
  -H "Content-Type: application/octet-stream" \
  --data-binary @"${ZIP_FILE}")"

echo "${resp}" | jq .
items="$(echo "${resp}" | jq -r '.total_items')"
dups="$(echo "${resp}" | jq -r '.duplicates_count')"
[[ "${items}" -eq 12 ]] || { echo "Expected total_items=12, got ${items}"; exit 1; }
[[ "${dups}" -eq 0 ]] || { echo "Expected duplicates_count=0, got ${dups}"; exit 1; }

echo "==> POST zip again (expect dup 12, insert 0)"
resp2="$(curl -fsS -X POST "${BASE_URL}/api/v0/prices?type=zip" \
  -H "Content-Type: application/octet-stream" \
  --data-binary @"${ZIP_FILE}")"
echo "${resp2}" | jq .
items2="$(echo "${resp2}" | jq -r '.total_items')"
dups2="$(echo "${resp2}" | jq -r '.duplicates_count')"
[[ "${items2}" -eq 0 ]] || { echo "Expected total_items=0, got ${items2}"; exit 1; }
[[ "${dups2}" -eq 12 ]] || { echo "Expected duplicates_count=12, got ${dups2}"; exit 1; }

echo "==> GET export zip and validate data.csv exists"
curl -fsS -o "${ROOT_DIR}/out.zip" \
  "${BASE_URL}/api/v0/prices?start=2024-01-01&end=2024-12-31&min=1&max=10000"

unzip -l "${ROOT_DIR}/out.zip" | grep -q "data.csv" || { echo "data.csv not found in out.zip"; exit 1; }

header="$(unzip -p "${ROOT_DIR}/out.zip" data.csv | head -n 1)"
[[ "${header}" == "id,name,category,price,create_date" ]] || { echo "Unexpected CSV header: ${header}"; exit 1; }

echo "==> TRUNCATE prices (for tar test)"
docker exec -i "${PG_CONTAINER}" psql -U validator -d project-sem-1 -c "TRUNCATE prices;" >/dev/null

echo "==> POST tar (expect insert 12, dup 0)"
resp3="$(curl -fsS -X POST "${BASE_URL}/api/v0/prices?type=tar" \
  -H "Content-Type: application/octet-stream" \
  --data-binary @"${TAR_FILE}")"

echo "${resp3}" | jq .
items3="$(echo "${resp3}" | jq -r '.total_items')"
dups3="$(echo "${resp3}" | jq -r '.duplicates_count')"
[[ "${items3}" -eq 12 ]] || { echo "Expected total_items=12, got ${items3}"; exit 1; }
[[ "${dups3}" -eq 0 ]] || { echo "Expected duplicates_count=0, got ${dups3}"; exit 1; }

echo "ALL TESTS PASSED"
