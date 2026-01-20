#!/usr/bin/env bash
set -euo pipefail

# Yandex Cloud (YC CLI) version: creates a VM and deploys via docker compose.
#
# Required env:
#   YC_CLOUD_ID, YC_FOLDER_ID, YC_ZONE, YC_SUBNET_ID
#   SSH_PUBLIC_KEY (path to .pub), SSH_PRIVATE_KEY (path to private)
#
# Optional:
#   VM_USER=ubuntu
#   INSTANCE_NAME=final-project-sem1
#   REMOTE_DIR=/opt/final_project
#   APP_PORT=8080

: "${YC_CLOUD_ID:?set YC_CLOUD_ID}"
: "${YC_FOLDER_ID:?set YC_FOLDER_ID}"
: "${YC_ZONE:?set YC_ZONE}"
: "${YC_SUBNET_ID:?set YC_SUBNET_ID}"
: "${SSH_PUBLIC_KEY:?set SSH_PUBLIC_KEY (path to .pub)}"
: "${SSH_PRIVATE_KEY:?set SSH_PRIVATE_KEY (path to private key)}"

VM_USER="${VM_USER:-ubuntu}"
INSTANCE_NAME="${INSTANCE_NAME:-final-project-sem1}"
REMOTE_DIR="${REMOTE_DIR:-/opt/final_project}"
APP_PORT="${APP_PORT:-8080}"

SSH_OPTS="-i ${SSH_PRIVATE_KEY} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null"

need_cmd() { command -v "$1" >/dev/null 2>&1 || { echo "ERROR: missing '$1'"; exit 1; }; }
need_cmd yc
need_cmd ssh
need_cmd scp
need_cmd tar
need_cmd jq

echo "==> Configure yc profile"
yc config set cloud-id "${YC_CLOUD_ID}" >/dev/null
yc config set folder-id "${YC_FOLDER_ID}" >/dev/null

echo "==> Ensure security group (22 + ${APP_PORT})"
NET_ID="$(yc vpc subnet get "${YC_SUBNET_ID}" --format json | jq -r '.network_id')"

SG_NAME="${INSTANCE_NAME}-sg"
SG_ID="$(yc vpc security-group get --name "${SG_NAME}" --format json 2>/dev/null | jq -r '.id' || true)"
if [[ -z "${SG_ID}" || "${SG_ID}" == "null" ]]; then
  SG_ID="$(yc vpc security-group create --name "${SG_NAME}" --network-id "${NET_ID}" --format json | jq -r '.id')"
fi

# try add rules (idempotency depends on backend; if duplicates error appears, we will handle after)
set +e
yc vpc security-group update-rules "${SG_ID}" \
  --add-rule "direction=ingress,protocol=tcp,port=22,v4-cidrs=[0.0.0.0/0]" \
  --add-rule "direction=ingress,protocol=tcp,port=${APP_PORT},v4-cidrs=[0.0.0.0/0]" \
  --add-rule "direction=egress,protocol=any,v4-cidrs=[0.0.0.0/0]" \
  >/dev/null 2>&1
set -e

echo "==> Create VM if missing"
PUB_KEY="$(cat "${SSH_PUBLIC_KEY}")"
TMP_USERDATA="$(mktemp)"
cat > "${TMP_USERDATA}" <<EOF
#cloud-config
users:
  - name: ${VM_USER}
    groups: sudo, docker
    shell: /bin/bash
    sudo: ["ALL=(ALL) NOPASSWD:ALL"]
    ssh_authorized_keys:
      - ${PUB_KEY}
package_update: true
EOF

INSTANCE_ID="$(yc compute instance get --name "${INSTANCE_NAME}" --format json 2>/dev/null | jq -r '.id' || true)"
if [[ -z "${INSTANCE_ID}" || "${INSTANCE_ID}" == "null" ]]; then
  create_json="$(yc compute instance create \
    --name "${INSTANCE_NAME}" \
    --zone "${YC_ZONE}" \
    --cores 2 \
    --memory 2GB \
    --create-boot-disk "image-folder-id=standard-images,image-family=ubuntu-2404-lts,size=20GB" \
    --network-interface "subnet-id=${YC_SUBNET_ID},nat-ip-version=ipv4,security-group-ids=[${SG_ID}]" \
    --metadata-from-file "user-data=${TMP_USERDATA}" \
    --format json)"
  INSTANCE_ID="$(echo "${create_json}" | jq -r '.id')"
fi
rm -f "${TMP_USERDATA}"

PUBLIC_IP="$(yc compute instance get "${INSTANCE_ID}" --format json | jq -r '.network_interfaces[0].primary_v4_address.one_to_one_nat.address')"
if [[ -z "${PUBLIC_IP}" || "${PUBLIC_IP}" == "null" ]]; then
  echo "ERROR: cannot get public IP"
  exit 1
fi

echo "==> Wait SSH on ${PUBLIC_IP}"
for i in $(seq 1 60); do
  if ssh ${SSH_OPTS} "${VM_USER}@${PUBLIC_IP}" "echo ok" >/dev/null 2>&1; then
    break
  fi
  sleep 5
done

echo "==> Install Docker + Compose plugin if missing"
ssh ${SSH_OPTS} "${VM_USER}@${PUBLIC_IP}" 'bash -s' <<'REMOTE'
set -euo pipefail
if ! command -v docker >/dev/null 2>&1; then
  sudo apt-get update -y
  sudo apt-get install -y ca-certificates curl gnupg
  sudo install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg
  sudo chmod a+r /etc/apt/keyrings/docker.gpg
  echo \
    "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu \
    $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | \
    sudo tee /etc/apt/sources.list.d/docker.list >/dev/null
  sudo apt-get update -y
  sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
fi
sudo systemctl enable --now docker || true
sudo docker version >/dev/null
sudo docker compose version >/dev/null
REMOTE

echo "==> Upload project to ${REMOTE_DIR} (tar over scp)"
TMP_TAR="$(mktemp --suffix=.tar.gz)"
tar --exclude=".git" --exclude="out.zip" --exclude="sample_data.tar" -czf "${TMP_TAR}" .
scp ${SSH_OPTS} "${TMP_TAR}" "${VM_USER}@${PUBLIC_IP}:/tmp/final_project.tar.gz"
rm -f "${TMP_TAR}"

echo "==> Unpack and run docker compose"
ssh ${SSH_OPTS} "${VM_USER}@${PUBLIC_IP}" "bash -s" <<EOF
set -euo pipefail
sudo mkdir -p "${REMOTE_DIR}"
sudo chown -R "${VM_USER}:${VM_USER}" "${REMOTE_DIR}"
rm -rf "${REMOTE_DIR:?}/"*
tar -xzf /tmp/final_project.tar.gz -C "${REMOTE_DIR}"
rm -f /tmp/final_project.tar.gz
cd "${REMOTE_DIR}"
sudo docker compose up -d --build
EOF

echo "==> Wait for /health"
for i in $(seq 1 60); do
  if curl -fsS "http://${PUBLIC_IP}:${APP_PORT}/health" >/dev/null 2>&1; then
    break
  fi
  sleep 2
done

curl -fsS "http://${PUBLIC_IP}:${APP_PORT}/health" >/dev/null

# Required output: IP of created server
echo "${PUBLIC_IP}"
