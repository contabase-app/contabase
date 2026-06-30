#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/contabase-access-smoke.XXXXXX")"
RUN_DOCKER=false
RUN_NATIVE=false
SERVER_PID=""
COMPOSE_PROJECT="contabase-access-smoke-$(date +%s)-$$"
if [ "${SMOKE_ISOLATED_GOCACHE:-}" = "1" ]; then
  echo "Usando GOCACHE isolado; primeira execução pode demorar por recompilação."
  export GOCACHE="${TMP_DIR}/go-cache"
  mkdir -p "$GOCACHE"
fi

cleanup() {
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" >/dev/null 2>&1 || true
    wait "$SERVER_PID" >/dev/null 2>&1 || true
  fi
  if [ "$RUN_DOCKER" = true ] && [ -f "${TMP_DIR}/docker-compose.yml" ]; then
    docker compose -p "$COMPOSE_PROJECT" -f "${TMP_DIR}/docker-compose.yml" down -v --remove-orphans >/dev/null 2>&1 || true
  fi
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

usage() {
  cat <<'EOF'
Uso: scripts/smoke-access-modes.sh [--native] [--docker]

Sem flags:
  - roda a matriz unitária de acesso.

Com --native:
  - sobe o servidor nativo em porta temporária;
  - valida localhost e 127.0.0.1 sem 426.

Com --docker:
  - sobe um compose temporário com volume temporário;
  - valida localhost/127.0.0.1 no modo local-docker.
EOF
}

for arg in "$@"; do
  case "$arg" in
    --native)
      RUN_NATIVE=true
      ;;
    --docker)
      RUN_DOCKER=true
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      usage
      exit 1
      ;;
  esac
done

find_free_port() {
  local attempt port
  for attempt in $(seq 1 40); do
    port=$((20000 + RANDOM % 20000))
    if ! curl --connect-timeout 3 --max-time 5 -fsS "http://127.0.0.1:${port}/health" >/dev/null 2>&1; then
      printf '%s\n' "$port"
      return 0
    fi
  done
  echo "ERRO: nao foi possivel escolher uma porta temporaria" >&2
  return 1
}

http_code() {
  curl --connect-timeout 5 --max-time 10 -sS -o "${TMP_DIR}/curl-body" -w "%{http_code}" "$1"
}

assert_not_426() {
  local url="$1"
  local code
  code="$(http_code "$url")"
  if [ "$code" = "426" ]; then
    echo "ERRO: ${url} retornou 426 Upgrade Required"
    cat "${TMP_DIR}/curl-body"
    exit 1
  fi
}

assert_body_not_contains_block() {
  local url="$1"
  curl --connect-timeout 5 --max-time 10 -sS "$url" -o "${TMP_DIR}/curl-body"
  if grep -Fq "Acesso Remoto Bloqueado" "${TMP_DIR}/curl-body"; then
    echo "ERRO: ${url} exibiu bloqueio remoto"
    exit 1
  fi
}

wait_for_http() {
  local url="$1"
  local attempt
  for attempt in $(seq 1 180); do
    if curl --connect-timeout 3 --max-time 5 -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "ERRO: timeout aguardando ${url}"
  if [ -f "${TMP_DIR}/native.log" ]; then
    echo "---- native.log ----"
    tail -n 120 "${TMP_DIR}/native.log" || true
  fi
  exit 1
}

run_unit_matrix() {
  echo "[1/3] Matriz unitária de acesso"
  echo "  go test ./cmd/server -run 'Access|Remote|Host|LAN|Proxy|Allowed|Security|Health|Docker|Local' -v -count=1 -timeout 120s"
  (
    cd "$REPO_ROOT"
    go test ./cmd/server -run 'Access|Remote|Host|LAN|Proxy|Allowed|Security|Health|Docker|Local' -v -count=1 -timeout 120s
  )
}

run_native_local_smoke() {
  local port db_file data_dir uploads_dir log_file
  port="$(find_free_port)"
  data_dir="${TMP_DIR}/native-data"
  uploads_dir="${data_dir}/uploads"
  db_file="${data_dir}/contabase.db"
  log_file="${TMP_DIR}/native.log"
  mkdir -p "$uploads_dir"

  echo "[2/3] Servidor nativo local em porta temporária ${port}"
  (
    cd "$REPO_ROOT"
    APP_ENV=development \
    APP_DEBUG=false \
    PORT="$port" \
    APP_BASE_URL="http://localhost:${port}" \
    ALLOWED_HOSTS="localhost,127.0.0.1,::1" \
    TRUSTED_PROXIES="" \
    CONTABASE_ACCESS_MODE=local \
    DATABASE_URL="file:${db_file}" \
    DATA_DIR="$data_dir" \
    DB_FILE="$db_file" \
    UPLOADS_DIR="$uploads_dir" \
    go run ./cmd/server >"$log_file" 2>&1
  ) &
  SERVER_PID="$!"

  wait_for_http "http://localhost:${port}/health"
  assert_not_426 "http://localhost:${port}/health"
  assert_not_426 "http://127.0.0.1:${port}/health"
}

run_docker_local_smoke() {
  local port compose_file data_dir
  command -v docker >/dev/null 2>&1 || { echo "ERRO: docker não encontrado"; exit 1; }
  docker compose version >/dev/null

  port="$(find_free_port)"
  compose_file="${TMP_DIR}/docker-compose.yml"
  data_dir="${TMP_DIR}/docker-data"
  mkdir -p "${data_dir}/uploads"

  cat > "$compose_file" <<EOF
services:
  contabase:
    build:
      context: ${REPO_ROOT}
      args:
        VERSION: smoke
    ports:
      - "127.0.0.1:${port}:8080"
    environment:
      APP_ENV: development
      APP_DEBUG: "false"
      PORT: "8080"
      APP_BASE_URL: http://localhost:${port}
      ALLOWED_HOSTS: localhost,127.0.0.1,::1
      TRUSTED_PROXIES: ""
      CONTABASE_ACCESS_MODE: local-docker
      DATABASE_URL: file:/app/data/contabase.db
      DATA_DIR: /app/data
      DB_FILE: /app/data/contabase.db
      UPLOADS_DIR: /app/data/uploads
      CONTABASE_SETUP_TOKEN: ""
      AUTH_ENCRYPTION_KEY: ""
      SECURITY_MASTER_KEY: ""
    volumes:
      - ${data_dir}:/app/data
EOF

  if ! grep -Fq '"127.0.0.1:' "$compose_file"; then
    echo "ERRO: smoke Docker local-docker deve publicar porta em 127.0.0.1"
    exit 1
  fi

  echo "[3/3] Docker local em porta temporária ${port}"
  docker compose -p "$COMPOSE_PROJECT" -f "$compose_file" up --build -d
  wait_for_http "http://localhost:${port}/health"
  assert_not_426 "http://localhost:${port}/health"
  assert_not_426 "http://127.0.0.1:${port}/health"
  assert_body_not_contains_block "http://localhost:${port}/"

  if docker compose -p "$COMPOSE_PROJECT" -f "$compose_file" logs --tail 100 contabase 2>/dev/null | grep -Eq 'TRUSTED_PROXIES=0\.0\.0\.0/0|ALLOWED_HOSTS=\*'; then
    echo "ERRO: logs indicam configuração insegura"
    exit 1
  fi
}

run_unit_matrix
if [ "$RUN_NATIVE" = true ]; then
  run_native_local_smoke
else
  echo "[2/3] Servidor nativo não solicitado. Rode scripts/smoke-access-modes.sh --native para validar bind local real."
fi
if [ "$RUN_DOCKER" = true ]; then
  run_docker_local_smoke
else
  echo "[3/3] Docker não solicitado. Rode scripts/smoke-access-modes.sh --docker para o smoke com container."
fi

echo "Smoke de modos de acesso concluído."
