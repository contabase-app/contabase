#!/usr/bin/env bash
# Testes sinteticos do adaptador Docker por digest. Nunca usam Docker real nem dados reais.
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
INSTALLER="${ROOT_DIR}/scripts/install-contabase-docker.sh"
LIB="${ROOT_DIR}/scripts/lib/contabase-docker-image.sh"
REAL_PYTHON="$(command -v python3)"
TMP_BASE="$(mktemp -d "${TMPDIR:-/tmp}/contabase-docker-image-test.XXXXXX")" || exit 1
TMP_BASE="$(cd "$TMP_BASE" && pwd -P)"
TEST_VERSION="v1.0.0-beta.8"
TEST_CHANNEL="beta"
TEST_INDEX="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
TEST_PLATFORM_DIGEST="sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
TEST_REFERENCE="ghcr.io/contabase-app/contabase@${TEST_INDEX}"
TEST_PORT="18080"
TEST_BIND="127.0.0.1"
TEST_DOCKER_HOST=""
TEST_DOCKER_CONTEXT=""
PASS=0
FAIL=0
PASS_FILE="${TMP_BASE}/.pass-count"
FAIL_FILE="${TMP_BASE}/.fail-count"
printf '0\n' > "$PASS_FILE"
printf '0\n' > "$FAIL_FILE"

cleanup() {
  if [ "${KEEP_TMP:-0}" = "1" ]; then
    printf 'TMP_BASE preservado: %s\n' "$TMP_BASE" >&2
    return
  fi
  rm -rf "$TMP_BASE"
}
trap cleanup EXIT

ok() {
  local count
  count="$(cat "$PASS_FILE")"
  printf '%s\n' $((count + 1)) > "$PASS_FILE"
  printf 'ok - %s\n' "$1"
}

not_ok() {
  local count
  count="$(cat "$FAIL_FILE")"
  printf '%s\n' $((count + 1)) > "$FAIL_FILE"
  printf 'not ok - %s\n' "$1"
}

assert_equal() {
  local expected="$1" observed="$2" label="$3"
  if [ "$expected" = "$observed" ]; then
    ok "$label"
  else
    not_ok "$label (esperado: $expected; observado: $observed)"
  fi
}

assert_contains() {
  local haystack="$1" needle="$2" label="$3"
  case "$haystack" in
    *"$needle"*) ok "$label" ;;
    *) not_ok "$label (nao contem: $needle)" ;;
  esac
}

assert_not_contains() {
  local haystack="$1" needle="$2" label="$3"
  case "$haystack" in
    *"$needle"*) not_ok "$label (contem: $needle)" ;;
    *) ok "$label" ;;
  esac
}

file_mode() {
  if stat -c %a "$1" >/dev/null 2>&1; then
    stat -c %a "$1"
  else
    stat -f %Lp "$1"
  fi
}

file_sha() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

make_checkout() {
  local name="$1" directory="${TMP_BASE}/checkouts/$1"
  mkdir -p "$directory/scripts/lib" "$directory/scripts/ops" "$directory/templates" "$directory/assets" \
    "$directory/internal" "$directory/cmd"
  printf '%s\n' "$TEST_VERSION" > "$directory/VERSION"
  cat > "$directory/docker-compose.yml" <<'YAML'
services:
  contabase:
    build:
      context: .
    container_name: contabase_app
    ports:
      - "127.0.0.1:8080:8080"
    env_file:
      - .env.docker
    environment:
      - PORT=8080
    volumes:
      - ./data:/app/data
    restart: unless-stopped
YAML
  printf 'FROM scratch\n' > "$directory/Dockerfile"
  cat > "$directory/.env.docker.example" <<'ENV'
APP_ENV=production
PORT=8080
CONTABASE_FEATURE_INVESTMENTS=false
CONTABASE_SETUP_TOKEN=
AUTH_ENCRYPTION_KEY=
SECURITY_MASTER_KEY=
TRUSTED_PROXIES=
ENV
  printf '#!/bin/bash\nexit 99\n' > "$directory/scripts/install-contabase-docker.sh"
  cp "$ROOT_DIR/scripts/lib/contabase-update-wrapper.sh" "$directory/scripts/lib/"
  cp "$ROOT_DIR/scripts/lib/contabase-docker-image-update.py" "$directory/scripts/lib/"
  cp "$ROOT_DIR/scripts/ops/backup.sh" "$directory/scripts/ops/"
  printf 'module synthetic\n' > "$directory/go.mod"
  printf '{}\n' > "$directory/package-lock.json"
  git -C "$directory" init -q
  git -C "$directory" add -A
  git -C "$directory" -c user.email=synthetic@example.test -c user.name=Synthetic \
    -c commit.gpgsign=false commit -q -m synthetic
  git -C "$directory" checkout -q --detach HEAD
  printf '%s' "$directory"
}

make_stubs() {
  local directory="$1" mode="${2:-success}" checkout="${3:-}"
  mkdir -p "$directory"
  cat > "$directory/python3" <<PY
#!${REAL_PYTHON}
import os
from pathlib import Path
import subprocess
import sys
args = sys.argv[1:]
if args[:3] == ['-I', '-B', '-']:
    source = sys.stdin.read()
    injection = '''spec.loader.exec_module(module)
    module.ADMIN_UID = os.getuid()
    module.os.geteuid = lambda: 0
    module.COMMAND_PREFIX = Path('${directory}/system/usr/local')
    original_open = module.os.open
    def injected_open(name, flags, *args, **kwargs):
        if str(name) == 'contabase-update':
            if Path('${directory}/race_command').exists():
                race_fd = original_open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o755, **kwargs)
                os.write(race_fd, b'preexisting-race-command')
                os.close(race_fd)
            if Path('${directory}/fail_command').exists():
                raise OSError('synthetic command creation failure')
        return original_open(name, flags, *args, **kwargs)
    module.os.open = injected_open'''
    source = source.replace('spec.loader.exec_module(module)', injection)
    sys.argv = ['-', *args[3:]]
    exec(compile(source, '<synthetic-installer>', 'exec'), {'__name__': '__main__', 'os': os, 'Path': Path})
else:
    if Path('${directory}/old_python').exists() and '-c' in args:
        sys.exit(1)
    sys.exit(subprocess.call(['${REAL_PYTHON}', *args]))
PY
  mkdir -p "$directory/system/usr/local/bin" "$directory/system/usr/local/lib"
  cat > "$directory/install" <<SH
#!/usr/bin/env bash
args=()
for arg in "\$@"; do
  case "\$arg" in /usr/local/*) arg="${directory}/system\$arg" ;; esac
  args+=("\$arg")
done
mkdir -p "${directory}/system/usr/local/bin"
/usr/bin/install "\${args[@]}"
SH
  cat > "$directory/ln" <<SH
#!/usr/bin/env bash
args=()
for arg in "\$@"; do
  case "\$arg" in /usr/local/*) arg="${directory}/system\$arg" ;; esac
  args+=("\$arg")
done
/bin/ln "\${args[@]}"
SH
  cat > "$directory/uname" <<'SH'
#!/usr/bin/env bash
case "${1:-}" in
  -s) echo Linux ;;
  -m) echo arm64 ;;
  *) echo Linux ;;
esac
SH
  cat > "$directory/chown" <<SH
#!/usr/bin/env bash
[ ! -f "${directory}/chown_fail" ]
SH
  cat > "$directory/id" <<SH
#!/usr/bin/env bash
if [ "\${1:-}" = "-u" ]; then
  if [ -f "${directory}/non_root" ]; then echo 1000; else echo 0; fi
else
  /usr/bin/id "\$@"
fi
SH
  cat > "$directory/sudo" <<SH
#!/usr/bin/env bash
[ ! -f "${directory}/sudo_fail" ]
SH
  cat > "$directory/systemctl" <<'SH'
#!/usr/bin/env bash
echo not-found
SH
  cat > "$directory/findmnt" <<SH
#!/usr/bin/env bash
if [ -f "${directory}/fstype" ]; then cat "${directory}/fstype"; else echo ext4; fi
SH
  cat > "$directory/docker" <<SH
#!/usr/bin/env bash
set -u
LOG="${directory}/docker.log"
echo "\$*" >> "\$LOG"
if [ "\${1:-}" = "compose" ] && [ "\${2:-}" = "version" ]; then
  [ ! -f "${directory}/no_compose" ] || exit 1
  echo "Docker Compose version v5.5.0"
  exit 0
fi
if [ "\${1:-}" = "info" ]; then
  [ ! -f "${directory}/docker_down" ] || exit 1
  if [ -f "${directory}/rootless" ]; then echo '["name=rootless"]'; else echo '["name=seccomp"]'; fi
  exit 0
fi
if [ "\${1:-}" = "ps" ] && [ "\${2:-}" = "-aq" ]; then
  if [ -f "${directory}/container_present" ] || [ -f "${directory}/container_name" ]; then
    echo 123456789abc
  fi
  exit 0
fi
if [ "\${1:-}" = "volume" ] && [ "\${2:-}" = "ls" ]; then
  [ ! -f "${directory}/volume_name" ] || cat "${directory}/volume_name"
  exit 0
fi
if [ "\${1:-}" = "compose" ] && [ "\${2:-}" = "ls" ]; then
  [ ! -f "${directory}/project_name" ] || cat "${directory}/project_name"
  exit 0
fi
if [ "\${1:-}" = "compose" ]; then
  shift 2
  while [ \$# -gt 0 ]; do
    case "\$1" in
      --project-directory|-f) shift 2 ;;
      config|pull|up|ps|exec|down|logs) break ;;
      *) shift ;;
    esac
  done
  action="\${1:-}"
  case "\$action" in
    config)
      cat <<'YAML'
services:
  contabase:
    environment:
      ALLOWED_HOSTS: localhost,127.0.0.1,::1
      APP_BASE_URL: http://localhost:18080
      CONTABASE_ACCESS_MODE: local-docker
      CONTABASE_FEATURE_INVESTMENTS: "false"
      TRUSTED_PROXIES: ""
    image: ghcr.io/contabase-app/contabase@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    ports:
      - mode: ingress
        host_ip: 127.0.0.1
        target: 8080
        published: "18080"
        protocol: tcp
    restart: unless-stopped
    user: 1000:1000
    volumes:
      - type: bind
        source: /tmp/synthetic/data
        target: /app/data
        bind: {}
    healthcheck:
      test: ["CMD", "wget", "--no-verbose", "--tries=1", "--spider", "http://localhost:8080/health"]
YAML
      exit 0 ;;
    pull)
      [ ! -f "${directory}/fail_pull" ] || exit 1
      exit 0 ;;
    up)
      [ ! -f "${directory}/fail_up" ] || exit 1
      exit 0 ;;
    ps)
      echo 123456789abc
      exit 0 ;;
    exec)
      echo "${TEST_VERSION}"
      exit 0 ;;
    down)
      exit 0 ;;
  esac
fi
if [ "\${1:-}" = "image" ] && [ "\${2:-}" = "inspect" ]; then
  echo '["${TEST_REFERENCE}"]'
  exit 0
fi
if [ "\${1:-}" = "inspect" ]; then
  case "\$*" in
    *".Name"*)
      if [ -f "${directory}/container_name" ]; then echo /contabase_app; else echo /unrelated; fi ;;
    *"Config.Labels"*)
      if [ -f "${directory}/container_labels" ]; then echo '{"com.docker.compose.project":"contabase"}'; else echo '{}'; fi ;;
    *"Mounts"*)
      if [ -f "${directory}/mount_source" ]; then cat "${directory}/mount_source"; echo "|"; fi ;;
    *)
      if [ -f "${directory}/health_fail" ]; then echo unhealthy; else echo healthy; fi ;;
  esac
  exit 0
fi
exit 0
SH
  chmod +x "$directory"/*
}

state_dir_for() {
  printf '%s/state/%s' "$TMP_BASE" "$(basename "$1")"
}

run_installer() {
  local checkout="$1" install="$2" stubs="$3" state_dir commit
  state_dir="$(state_dir_for "$install")"
  mkdir -p "$state_dir"
  commit="$(git -C "$checkout" rev-parse HEAD)"
  shift 3
  local -a endpoint_env=()
  [ -z "$TEST_DOCKER_HOST" ] || endpoint_env+=("DOCKER_HOST=${TEST_DOCKER_HOST}")
  [ -z "$TEST_DOCKER_CONTEXT" ] || endpoint_env+=("DOCKER_CONTEXT=${TEST_DOCKER_CONTEXT}")
  (cd "$checkout" && env -i \
    "PATH=${stubs}:${PATH}" \
    "HOME=${TMP_BASE}/home" \
    "TMPDIR=${TMP_BASE}" \
    "CONTABASE_VERSION=${TEST_VERSION}" \
    "CONTABASE_CHANNEL=${TEST_CHANNEL}" \
    "CONTABASE_COMMIT=${commit}" \
    "CONTABASE_INSTALL_METHOD=docker" \
    "CONTABASE_INSTALL_MODE=image" \
    "CONTABASE_BOOTSTRAP_CONTRACT_VERSION=1" \
    "CONTABASE_IMAGE_PROOF_SOURCE=bootstrap-catalog" \
    "CONTABASE_CONFIRM_INSTALL=INSTALAR" \
    "CONTABASE_FEATURE_INVESTMENTS=false" \
    "CONTABASE_IMAGE_INDEX_DIGEST=${TEST_INDEX}" \
    "CONTABASE_IMAGE_PLATFORM=linux/arm64" \
    "CONTABASE_IMAGE_PLATFORM_DIGEST=${TEST_PLATFORM_DIGEST}" \
    "CONTABASE_IMAGE_REFERENCE=${TEST_REFERENCE}" \
    "CONTABASE_PORT=${TEST_PORT}" \
    "CONTABASE_HOST_BIND=${TEST_BIND}" \
    "CONTABASE_INTERNAL_PORT=8080" \
    "APP_BASE_URL=http://localhost:${TEST_PORT}" \
    "ALLOWED_HOSTS=localhost,127.0.0.1,::1" \
    "TRUSTED_PROXIES=" \
    "CONTABASE_ACCESS_MODE=local-docker" \
    "CONTABASE_TIMEZONE=America/Sao_Paulo" \
    "CONTABASE_INSTALL_DIR=${install}" \
    "CONTABASE_DATA_DIR=${install}/data" \
    "CONTABASE_CONFIG_DIR=${install}" \
    "CONTABASE_STATE_FILE=${state_dir}/install-state.env" \
    "CONTABASE_MODE_FILE=${state_dir}/install-mode" \
    "CONTABASE_INSTALL_HISTORY_FILE=${state_dir}/history.log" \
    "CONTABASE_BOOTSTRAP_CHECKOUT=${checkout}" \
    ${endpoint_env[@]+"${endpoint_env[@]}"} \
    bash "$INSTALLER" "$@")
}

lib_run() {
  local script="$1"
  bash -c "set -uo pipefail; . '${LIB}'; ${script}"
}

# 1. Contrato de digest e referencia imutavel.
(
  export CONTABASE_VERSION="$TEST_VERSION" CONTABASE_CHANNEL="$TEST_CHANNEL"
  export CONTABASE_COMMIT=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
  export CONTABASE_INSTALL_METHOD=docker CONTABASE_INSTALL_MODE=image
  export CONTABASE_BOOTSTRAP_CONTRACT_VERSION=1 CONTABASE_IMAGE_PROOF_SOURCE=bootstrap-catalog
  export CONTABASE_CONFIRM_INSTALL=INSTALAR CONTABASE_FEATURE_INVESTMENTS=false
  export CONTABASE_IMAGE_INDEX_DIGEST="$TEST_INDEX" CONTABASE_IMAGE_PLATFORM=linux/arm64
  export CONTABASE_IMAGE_PLATFORM_DIGEST="$TEST_PLATFORM_DIGEST" CONTABASE_IMAGE_REFERENCE="$TEST_REFERENCE"
  export CONTABASE_PORT="$TEST_PORT" CONTABASE_HOST_BIND="$TEST_BIND"
  export CONTABASE_INTERNAL_PORT=8080
  export APP_BASE_URL="http://localhost:${TEST_PORT}" ALLOWED_HOSTS=localhost,127.0.0.1,::1
  export TRUSTED_PROXIES= CONTABASE_ACCESS_MODE=local-docker CONTABASE_TIMEZONE=America/Sao_Paulo
  . "$LIB"
  validate_image_contract && ok "digest valido e referencia imutavel aceitos"
  validate_network_config && ok "perfil localhost valido"

  CONTABASE_IMAGE_INDEX_DIGEST="" validate_image_contract >/dev/null 2>&1 \
    && not_ok "digest ausente aceito" || ok "digest ausente bloqueado"
  CONTABASE_IMAGE_INDEX_DIGEST="sha256:short" validate_image_contract >/dev/null 2>&1 \
    && not_ok "digest invalido aceito" || ok "digest invalido bloqueado"
  CONTABASE_IMAGE_REFERENCE="ghcr.io/contabase-app/contabase:beta" validate_image_contract >/dev/null 2>&1 \
    && not_ok "tag movel aceita" || ok "tag movel bloqueada"
  CONTABASE_IMAGE_PLATFORM_DIGEST="bbbb" validate_image_contract >/dev/null 2>&1 \
    && not_ok "digest de plataforma invalido aceito" || ok "digest de plataforma invalido bloqueado"
  CONTABASE_IMAGE_PROOF_SOURCE=manual validate_image_contract >/dev/null 2>&1 \
    && not_ok "digest avulso aceito" || ok "digest avulso bloqueado"
  CONTABASE_CONFIRM_INSTALL="" validate_image_contract >/dev/null 2>&1 \
    && not_ok "confirmacao ausente aceita" || ok "confirmacao ausente bloqueada"
  assert_repo_digest "[\"${TEST_REFERENCE}\"]" "$TEST_REFERENCE" \
    && ok "RepoDigest homologado aceito"
  assert_repo_digest '["ghcr.io/contabase-app/contabase@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"]' "$TEST_REFERENCE" >/dev/null 2>&1 \
    && not_ok "RepoDigest divergente aceito" || ok "RepoDigest divergente bloqueado"
)

# 2. Perfis de rede.
(
  . "$LIB"
  export CONTABASE_PORT=18081 CONTABASE_HOST_BIND=192.168.1.20
  export APP_BASE_URL=http://192.168.1.20:18081 ALLOWED_HOSTS=192.168.1.20
  export TRUSTED_PROXIES= CONTABASE_ACCESS_MODE=lan
  validate_network_config && ok "perfil LAN valido"
  export CONTABASE_PORT=18082 CONTABASE_HOST_BIND=127.0.0.1
  export APP_BASE_URL=https://money.example.test ALLOWED_HOSTS=money.example.test
  export TRUSTED_PROXIES=127.0.0.1/32 CONTABASE_ACCESS_MODE=proxy
  validate_network_config && ok "perfil proxy valido"
  export CONTABASE_HOST_BIND=0.0.0.0 CONTABASE_ACCESS_MODE=local-docker
  export APP_BASE_URL=http://localhost:18082 ALLOWED_HOSTS=localhost,127.0.0.1,::1
  export TRUSTED_PROXIES=
  validate_network_config >/dev/null 2>&1 && not_ok "bind 0.0.0.0 aceito" || ok "bind 0.0.0.0 bloqueado"
  export CONTABASE_HOST_BIND=192.168.1.20 CONTABASE_ACCESS_MODE=lan
  export APP_BASE_URL=http://192.168.1.20:18082 ALLOWED_HOSTS=192.168.1.20
  export TRUSTED_PROXIES=0.0.0.0/0
  validate_network_config >/dev/null 2>&1 && not_ok "proxy universal aceito" || ok "proxy universal bloqueado"
)

# 3. Compose real, porta externa, volume e perfis.
compose_tmp="${TMP_BASE}/compose"
mkdir -p "$compose_tmp/data"
printf 'CONTABASE_SETUP_TOKEN=synthetic\nAUTH_ENCRYPTION_KEY=synthetic\nSECURITY_MASTER_KEY=synthetic\n' > "$compose_tmp/.env.docker"
. "$LIB"
write_image_compose "$compose_tmp/local.yml" "$TEST_REFERENCE" "$TEST_BIND" "$TEST_PORT" \
  "http://localhost:${TEST_PORT}" "localhost,127.0.0.1,::1" "" "local-docker" "America/Sao_Paulo" \
  && ok "Compose local gerado"
if command -v docker >/dev/null 2>&1; then
  rendered="$(cd "$compose_tmp" && DOCKER_HOST=unix:///nonexistent docker compose -f local.yml config 2>/dev/null)" \
    && ok "Compose local valido pelo Docker Compose" || not_ok "Compose local invalido"
  assert_rendered_compose "$rendered" "$TEST_REFERENCE" "$TEST_BIND" "$TEST_PORT" \
    "http://localhost:${TEST_PORT}" "localhost,127.0.0.1,::1" "" "local-docker" \
    && ok "porta externa, volume e contrato renderizados"
  assert_contains "$rendered" "target: /app/data" "volume persistente /app/data presente"
  assert_not_contains "$rendered" "target: /app/uploads" "sem mount redundante de uploads"
  assert_not_contains "$rendered" "target: /app/backups" "sem mount redundante de backups"
  assert_contains "$rendered" "published: \"${TEST_PORT}\"" "porta externa diferente de 8080"
  assert_contains "$rendered" "target: 8080" "porta interna fixa 8080"
else
  not_ok "Docker Compose indisponivel para validar YAML"
fi
write_image_compose "$compose_tmp/lan.yml" "$TEST_REFERENCE" "192.168.1.20" "18081" \
  "http://192.168.1.20:18081" "192.168.1.20" "" "lan" "America/Sao_Paulo" \
  && ok "Compose LAN gerado"
lan_rendered="$(cd "$compose_tmp" && DOCKER_HOST=unix:///nonexistent docker compose -f lan.yml config 2>/dev/null)"
assert_contains "$lan_rendered" "host_ip: 192.168.1.20" "LAN publica no IP privado"
write_image_compose "$compose_tmp/proxy.yml" "$TEST_REFERENCE" "127.0.0.1" "18082" \
  "https://money.example.test" "money.example.test" "127.0.0.1/32" "proxy" "America/Sao_Paulo" \
  && ok "Compose proxy gerado"
proxy_rendered="$(cd "$compose_tmp" && DOCKER_HOST=unix:///nonexistent docker compose -f proxy.yml config 2>/dev/null)"
assert_contains "$proxy_rendered" "host_ip: 127.0.0.1" "proxy mantem bind loopback"
assert_contains "$proxy_rendered" "TRUSTED_PROXIES: 127.0.0.1/32" "proxy preserva TRUSTED_PROXIES"

# 4. Instalacao completa sintetica.
checkout="$(make_checkout success)"
success_commit="$(git -C "$checkout" rev-parse HEAD)"
stubs="${TMP_BASE}/stubs-success"
make_stubs "$stubs" success
install="${TMP_BASE}/success-install"
output="$(run_installer "$checkout" "$install" "$stubs" 2>&1)"
status=$?
assert_equal "0" "$status" "instalacao sintetica concluida"
if [ "$status" -ne 0 ]; then printf '%s\n' "$output"; fi
assert_equal "$TEST_VERSION" "$(tr -d '[:space:]' < "$install/VERSION")" "VERSION instalada"
assert_contains "$(cat "$install/docker-compose.image.yml")" "$TEST_INDEX" "digest no Compose gerado"
assert_not_contains "$(cat "$install/docker-compose.image.yml")" ":beta" "Compose sem tag beta"
assert_not_contains "$(cat "$install/docker-compose.image.yml")" ":latest" "Compose sem latest"
assert_not_contains "$(cat "$install/docker-compose.image.yml")" "build:" "Compose sem build local"
assert_equal "600" "$(file_mode "$install/.env.docker")" ".env.docker protegido"
assert_contains "$(cat "$install/.env.docker")" "CONTABASE_SETUP_TOKEN=" "setup token presente em arquivo"
token="$(awk -F= '$1 == "CONTABASE_SETUP_TOKEN" { print $2 }' "$install/.env.docker")"
[ -n "$token" ] && ok "setup token gerado" || not_ok "setup token vazio"
assert_not_contains "$output" "$token" "setup token nao impresso"
success_state="$(state_dir_for "$install")"
assert_contains "$(cat "${success_state}/install-state.env")" "CONTABASE_INSTALL_METHOD=docker-image" "metodo registrado"
assert_contains "$(cat "${success_state}/install-state.env")" "CONTABASE_IMAGE_INDEX_DIGEST=$TEST_INDEX" "digest registrado"
assert_contains "$(cat "${success_state}/install-state.env")" "CONTABASE_INSTALLED_VERSION=$TEST_VERSION" "versao registrada"
assert_contains "$(cat "${success_state}/install-state.env")" "CONTABASE_COMMIT=$success_commit" "commit registrado"
assert_contains "$(cat "${success_state}/history.log")" "method=docker-image" "historico registrado"
assert_contains "$(cat "${success_state}/history.log")" "commit=$success_commit" "commit no historico"
assert_contains "$(cat "$stubs/system/usr/local/lib/contabase/contabase-docker-image-update.py")" \
  "def handle_pending_update" "consumidor real instalado no destino administrativo simulado"
assert_contains "$(cat "$stubs/system/usr/local/bin/contabase-update")" \
  "${success_state}/install-state.env" "wrapper real preserva caminho de estado personalizado"
assert_not_contains "$(cat "$stubs/docker.log")" "--build" "nenhum build local disparado"
[ ! -e "$install/.contabase-tmp" ] && ok "temporarios privados removidos" || not_ok "temporarios privados remanescentes"

# Execute the installed wrapper and the installed Python consumer, without a TTY.
output="$(env -i PATH="$PATH" bash "$stubs/system/usr/local/bin/contabase-update" --dry-run </dev/null 2>&1)"
status=$?
[ "$status" -ne 0 ] && ok "comando instalado executa consumidor com bloqueio seguro no host sintetico" \
  || not_ok "comando instalado aceitou host sintetico"
assert_contains "$output" "UPDATE_BLOCKED" "wrapper instalado chegou ao consumidor real"
assert_not_contains "$output" "nao foi possivel detectar" "estado personalizado localizado sem override de ambiente"

for marker in preexisting_command fail_command race_command symlink_parent insecure_parent; do
  checkout="$(make_checkout "command-${marker}")"
  stubs="${TMP_BASE}/stubs-command-${marker}"
  make_stubs "$stubs" success
  command_target="$stubs/system/usr/local/bin/contabase-update"
  case "$marker" in
    preexisting_command) printf 'preexisting-command' > "$command_target" ;;
    symlink_parent)
      rmdir "$stubs/system/usr/local/lib"
      mkdir "$stubs/unrelated-lib"
      ln -s "$stubs/unrelated-lib" "$stubs/system/usr/local/lib" ;;
    insecure_parent) chmod 0777 "$stubs/system/usr/local/lib" ;;
    *) touch "$stubs/$marker" ;;
  esac
  install="${TMP_BASE}/command-install-${marker}"
  output="$(run_installer "$checkout" "$install" "$stubs" 2>&1)"
  status=$?
  [ "$status" -ne 0 ] && ok "$marker bloqueia instalacao administrativa" || not_ok "$marker aceito"
  command_state="$(state_dir_for "$install")"
  [ ! -e "$command_state/install-state.env" ] && [ ! -e "$command_state/history.log" ] \
    && ok "$marker nao finaliza estado nem historico" || not_ok "$marker deixou estado finalizado"
  [ ! -e "$stubs/system/usr/local/lib/contabase/contabase-docker-image-update.py" ] \
    && ok "$marker remove somente payload criado na tentativa" || not_ok "$marker deixou payload parcial"
  case "$marker" in
    preexisting_command) assert_equal preexisting-command "$(cat "$command_target")" "comando preexistente preservado" ;;
    race_command) assert_equal preexisting-race-command "$(cat "$command_target")" "comando criado na corrida preservado" ;;
    fail_command) [ ! -e "$command_target" ] && ok "comando nao publicado apos falha" || not_ok "comando parcial publicado" ;;
    symlink_parent) [ -z "$(ls -A "$stubs/unrelated-lib")" ] && ok "destino do symlink preservado" || not_ok "escrita atraves do symlink" ;;
    insecure_parent) assert_equal 777 "$(file_mode "$stubs/system/usr/local/lib")" "permissoes preexistentes nao alteradas" ;;
  esac
done

for marker in old_python invalid_helper; do
  checkout="$(make_checkout "$marker")"
  stubs="${TMP_BASE}/stubs-${marker}"
  make_stubs "$stubs" success
  if [ "$marker" = old_python ]; then
    touch "$stubs/old_python"
  else
    printf 'untrusted helper\n' > "$checkout/scripts/ops/backup.sh"
  fi
  install="${TMP_BASE}/${marker}-install"
  output="$(run_installer "$checkout" "$install" "$stubs" 2>&1)"
  status=$?
  [ "$status" -ne 0 ] && [ ! -e "$install" ] \
    && ok "$marker bloqueia antes da criacao da instalacao" || not_ok "$marker passou do preflight"
done

checkout="$(make_checkout non-root)"
stubs="${TMP_BASE}/stubs-non-root"
make_stubs "$stubs" success
touch "$stubs/non_root"
install="${TMP_BASE}/non-root-install"
output="$(run_installer "$checkout" "$install" "$stubs" 2>&1)"
status=$?
[ "$status" -ne 0 ] && [ ! -e "$install" ] && [ ! -e "$stubs/docker.log" ] \
  && ok "root exigido antes de Docker e de qualquer escrita da instalacao" || not_ok "non-root passou do gate inicial"

# 5. Modo --check nao escreve.
checkout="$(make_checkout check-only)"
stubs="${TMP_BASE}/stubs-check"
make_stubs "$stubs" success
install="${TMP_BASE}/check-install"
output="$(run_installer "$checkout" "$install" "$stubs" --check 2>&1)"
status=$?
[ "$status" -eq 0 ] && ok "modo --check aprovado" || { not_ok "modo --check falhou"; printf '%s\n' "$output"; }
[ ! -e "$install" ] && ok "modo --check nao cria diretorio" || not_ok "modo --check criou diretorio"
check_state="$(state_dir_for "$install")"
[ ! -f "${check_state}/install-state.env" ] && ok "modo --check nao grava estado" || not_ok "modo --check gravou estado"

# 6. Instalacao existente e symlink.
checkout="$(make_checkout existing)"
stubs="${TMP_BASE}/stubs-existing"
make_stubs "$stubs" success
install="${TMP_BASE}/existing-install"
mkdir -p "$install"
printf 'sentinel\n' > "$install/sentinel.txt"
output="$(run_installer "$checkout" "$install" "$stubs" 2>&1)"
status=$?
[ "$status" -ne 0 ] && ok "instalacao existente bloqueada" || not_ok "instalacao existente aceita"
assert_equal "sentinel" "$(cat "$install/sentinel.txt")" "dados existentes preservados"

install_target="${TMP_BASE}/symlink-target"
mkdir -p "$install_target"
ln -s "$install_target" "${TMP_BASE}/symlink-install"
output="$(run_installer "$checkout" "${TMP_BASE}/symlink-install" "$stubs" 2>&1)"
status=$?
[ "$status" -ne 0 ] && ok "symlink de instalacao bloqueado" || not_ok "symlink de instalacao aceito"

# 7. Container, volume, mount e projeto conflitantes.
for marker in container_name volume_name project_name; do
  checkout="$(make_checkout "conflict-${marker}")"
  stubs="${TMP_BASE}/stubs-${marker}"
  make_stubs "$stubs" success
  touch "$stubs/$marker"
  [ "$marker" = "volume_name" ] && printf 'contabase_data\n' > "$stubs/$marker"
  [ "$marker" = "project_name" ] && printf 'contabase\n' > "$stubs/$marker"
  install="${TMP_BASE}/conflict-install-${marker}"
  output="$(run_installer "$checkout" "$install" "$stubs" 2>&1)"
  status=$?
  [ "$status" -ne 0 ] && ok "$marker conflitante bloqueado" || not_ok "$marker conflitante aceito"
done
checkout="$(make_checkout conflict-mount)"
stubs="${TMP_BASE}/stubs-mount"
make_stubs "$stubs" success
touch "$stubs/container_present"
install="${TMP_BASE}/conflict-install-mount"
printf '%s\n' "$install" > "$stubs/mount_source"
output="$(run_installer "$checkout" "$install" "$stubs" 2>&1)"
status=$?
[ "$status" -ne 0 ] && ok "mount conflitante bloqueado" || not_ok "mount conflitante aceito"

# 8. Docker indisponivel, rootless e endpoints remotos.
checkout="$(make_checkout docker-unavailable)"
stubs="${TMP_BASE}/stubs-no-compose"
make_stubs "$stubs" success
touch "$stubs/no_compose"
output="$(run_installer "$checkout" "${TMP_BASE}/no-compose-install" "$stubs" 2>&1)"
status=$?
[ "$status" -ne 0 ] && ok "Docker Compose indisponivel bloqueado" || not_ok "Docker Compose indisponivel aceito"

stubs="${TMP_BASE}/stubs-docker-down"
make_stubs "$stubs" success
touch "$stubs/docker_down"
output="$(run_installer "$checkout" "${TMP_BASE}/docker-down-install" "$stubs" 2>&1)"
status=$?
[ "$status" -ne 0 ] && ok "Docker indisponivel bloqueado" || not_ok "Docker indisponivel aceito"

stubs="${TMP_BASE}/stubs-rootless"
make_stubs "$stubs" success
touch "$stubs/rootless"
output="$(run_installer "$checkout" "${TMP_BASE}/rootless-install" "$stubs" 2>&1)"
status=$?
[ "$status" -ne 0 ] && ok "Docker rootless bloqueado explicitamente" || not_ok "Docker rootless aceito"
assert_contains "$output" "rootless" "mensagem de rootless explicita"

stubs="${TMP_BASE}/stubs-endpoint"
make_stubs "$stubs" success
TEST_DOCKER_HOST="tcp://127.0.0.1:2375"
output="$(run_installer "$checkout" "${TMP_BASE}/tcp-install" "$stubs" 2>&1)"
status=$?
TEST_DOCKER_HOST=""
[ "$status" -ne 0 ] && ok "DOCKER_HOST TCP bloqueado" || not_ok "DOCKER_HOST TCP aceito"
TEST_DOCKER_CONTEXT="remote"
output="$(run_installer "$checkout" "${TMP_BASE}/context-install" "$stubs" 2>&1)"
status=$?
TEST_DOCKER_CONTEXT=""
[ "$status" -ne 0 ] && ok "DOCKER_CONTEXT remoto bloqueado" || not_ok "DOCKER_CONTEXT remoto aceito"
TEST_DOCKER_HOST="unix:///run/user/1000/docker.sock"
output="$(run_installer "$checkout" "${TMP_BASE}/rootless-socket-install" "$stubs" 2>&1)"
status=$?
TEST_DOCKER_HOST=""
[ "$status" -ne 0 ] && ok "socket rootless bloqueado" || not_ok "socket rootless aceito"

# 9. Falha de permissao e falha parcial preservam dados.
checkout="$(make_checkout permission)"
stubs="${TMP_BASE}/stubs-permission"
make_stubs "$stubs" success
touch "$stubs/chown_fail" "$stubs/non_root" "$stubs/sudo_fail"
install="${TMP_BASE}/permission-install"
printf 'sibling\n' > "${TMP_BASE}/sibling.txt"
output="$(run_installer "$checkout" "$install" "$stubs" 2>&1)"
status=$?
[ "$status" -ne 0 ] && ok "falha de permissao bloqueada" || not_ok "falha de permissao aceita"
assert_equal "sibling" "$(cat "${TMP_BASE}/sibling.txt")" "arquivo vizinho preservado"
permission_state="$(state_dir_for "$install")"
[ ! -f "${permission_state}/install-state.env" ] && ok "estado nao gravado em falha" || not_ok "estado gravado em falha"

checkout="$(make_checkout partial)"
stubs="${TMP_BASE}/stubs-partial"
make_stubs "$stubs" success
touch "$stubs/fail_pull"
install="${TMP_BASE}/partial-install"
output="$(run_installer "$checkout" "$install" "$stubs" 2>&1)"
status=$?
[ "$status" -ne 0 ] && ok "falha de pull bloqueada" || not_ok "falha de pull aceita"
[ -d "$install" ] && ok "diretorio preservado para diagnostico" || not_ok "diretorio removido em falha"
assert_not_contains "$(cat "$stubs/docker.log")" "up -d" "stack nao iniciada apos falha de pull"

# Falha de healthcheck para a stack criada, preserva arquivos e nao publica estado.
checkout="$(make_checkout unhealthy)"
stubs="${TMP_BASE}/stubs-unhealthy"
make_stubs "$stubs" success
touch "$stubs/health_fail"
install="${TMP_BASE}/unhealthy-install"
output="$(run_installer "$checkout" "$install" "$stubs" 2>&1)"
status=$?
[ "$status" -ne 0 ] && ok "healthcheck unhealthy bloqueia conclusao" || not_ok "healthcheck unhealthy aceito"
assert_contains "$output" "healthcheck nao ficou saudavel" "falha de health informada"
assert_contains "$(cat "$stubs/docker.log")" "down --remove-orphans" "stack unhealthy parada"
[ -f "$install/.env.docker" ] && [ -d "$install/data" ] && ok "configuracao e dados novos preservados apos unhealthy" || not_ok "arquivos removidos apos unhealthy"
unhealthy_state="$(state_dir_for "$install")"
[ ! -f "$unhealthy_state/install-state.env" ] && ok "estado nao publicado apos unhealthy" || not_ok "estado publicado apos unhealthy"

# 10. Banco, WAL e backups preexistentes intactos.
checkout="$(make_checkout data-preservation)"
stubs="${TMP_BASE}/stubs-data"
make_stubs "$stubs" success
install="${TMP_BASE}/data-install"
mkdir -p "$install/data/backups"
printf 'database\n' > "$install/data/contabase.db"
printf 'wal\n' > "$install/data/contabase.db-wal"
printf 'shm\n' > "$install/data/contabase.db-shm"
printf 'backup\n' > "$install/data/backups/synthetic.db"
before="$(
  file_sha "$install/data/contabase.db"
  file_sha "$install/data/contabase.db-wal"
  file_sha "$install/data/contabase.db-shm"
  file_sha "$install/data/backups/synthetic.db"
)"
output="$(run_installer "$checkout" "$install" "$stubs" 2>&1)"
status=$?
[ "$status" -ne 0 ] && ok "instalacao sobre dados existentes bloqueada" || not_ok "instalacao sobre dados existentes aceita"
after="$(
  file_sha "$install/data/contabase.db"
  file_sha "$install/data/contabase.db-wal"
  file_sha "$install/data/contabase.db-shm"
  file_sha "$install/data/backups/synthetic.db"
)"
assert_equal "$before" "$after" "banco, WAL, SHM e backups inalterados"

# 11. Estado incompleto nao recebe o atalho de igualdade de versao.
cat > "${TMP_BASE}/updater-state.env" <<ENV
CONTABASE_STATE_VERSION=1
CONTABASE_INSTALL_METHOD=docker-image
CONTABASE_CHANNEL=beta
CONTABASE_INSTALLED_VERSION=${TEST_VERSION}
CONTABASE_REPO_PATH=/nonexistent
ENV
output="$(
  env -i PATH="${PATH}" CONTABASE_STATE_FILE="${TMP_BASE}/updater-state.env" \
    CONTABASE_MODE_FILE="${TMP_BASE}/no-mode" CONTABASE_ENV_FILE="${TMP_BASE}/no-env" \
    bash "${ROOT_DIR}/scripts/lib/contabase-update-wrapper.sh" "$TEST_VERSION" 2>&1
)"
status=$?
[ "$status" -ne 0 ] && ok "updater falha fechado para docker-image incompleto" || not_ok "updater aceita estado incompleto"
assert_not_contains "$output" "ja esta" "igualdade nao ignora validacao de identidade"

# 12. Filesystem de dados.
stubs="${TMP_BASE}/stubs-fstype"
make_stubs "$stubs" success
printf 'nfs\n' > "$stubs/fstype"
(
  PATH="${stubs}:${PATH}"
  . "$LIB"
  assert_local_storage "${TMP_BASE}" >/dev/null 2>&1 && exit 0 || exit 1
) && not_ok "filesystem NFS aceito" || ok "filesystem NFS bloqueado"
printf 'ext4\n' > "$stubs/fstype"
(
  PATH="${stubs}:${PATH}"
  . "$LIB"
  assert_local_storage "${TMP_BASE}" >/dev/null 2>&1
) && ok "filesystem local aceito" || not_ok "filesystem local bloqueado"

PASS="$(cat "$PASS_FILE")"
FAIL="$(cat "$FAIL_FILE")"
printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
