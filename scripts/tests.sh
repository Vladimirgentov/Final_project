#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
ZIP_FILE="${ZIP_FILE:-sample_data.zip}"

# tar делаем на лету из sample_data/data.csv
TAR_FILE="${TAR_FILE:-sample_data.tar}"

echo "==> Healthcheck"
curl -fsS "${BASE_URL}/health" | grep -q "ok"
echo "OK"

echo "==> Prepare tar from sample_data/data.csv"
if [ -f "${TAR_FILE}" ]; then rm -f "${TAR_FILE}"; fi
tar -cf "${TAR_FILE}" -C "./sample_data" "data.csv"

echo "==> POST zip"
resp_zip="$(curl -fsS -X POST "${BASE_URL}/api/v0/prices?type=zip" \
  --data-binary "@${ZIP_FILE}" \
  -H "Content-Type: application/octet-stream")"
echo "${resp_zip}"
echo "${resp_zip}" | grep -q '"total_count"'
echo "${resp_zip}" | grep -q '"total_price"'

echo "==> POST tar"
resp_tar="$(curl -fsS -X POST "${BASE_URL}/api/v0/prices?type=tar" \
  --data-binary "@${TAR_FILE}" \
  -H "Content-Type: application/octet-stream")"
echo "${resp_tar}"
echo "${resp_tar}" | grep -q '"total_count"'

echo "==> GET filtered data -> zip"
OUT_ZIP="out.zip"
curl -fsS -o "${OUT_ZIP}" "${BASE_URL}/api/v0/prices?start=2024-01-01&end=2024-12-31&min=1&max=1000000"

# проверяем что zip корректный и содержит data.csv
unzip -l "${OUT_ZIP}" | grep -q "data.csv"
unzip -p "${OUT_ZIP}" data.csv | head -n 1 | grep -q "^id,name,category,price,create_date"

echo "==> All tests passed"
