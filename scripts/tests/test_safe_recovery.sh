#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$REPO_DIR/scripts/lib/contabase-recovery.sh"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/contabase-safe-recovery.XXXXXX")"
# Resolve symlinked ancestors (e.g. /var -> /private/var on macOS) because the
# production proof check intentionally rejects symlinked paths.
TMP_ROOT="$(cd "$TMP_ROOT" && pwd -P)"
trap 'rm -rf "$TMP_ROOT"' EXIT
PASS=0
ok() { PASS=$((PASS + 1)); printf 'ok %d - %s\n' "$PASS" "$1"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
say() { printf '%s\n' "$*"; }
log_warn() { say "$*"; }
load_function() {
  local code
  code="$(sed -n "/^$2() {$/,/^}$/p" "$1")"
  [ -n "$code" ] || fail "funcao ausente: $2"
  eval "$code"
}

# Ownership is a filesystem fixture; production checks are also exercised with
# non-admin owners, writable directories and symlinks below.
stat() {
  case "$1:$2" in
    -c:%u) if [ "${NONADMIN:-false}" = true ]; then echo 501; else echo 0; fi ;;
    -c:%a)
      case "$3" in "$TMP_ROOT"|"$TMP_ROOT"/*)
        if [ "$(uname -s)" = Darwin ]; then command stat -f %Lp "$3"; else command stat -c %a "$3"; fi ;;
        *) echo 755 ;;
      esac ;;
    *) command stat "$@" ;;
  esac
}
install() {
  local args=()
  while [ "$#" -gt 0 ]; do
    case "$1" in -o|-g) shift 2 ;; *) args+=("$1"); shift ;; esac
  done
  command install "${args[@]}"
}
chown() { :; }
systemctl() {
  printf '%s\n' "$*" >> "$EVENT_LOG"
  if [ "$1" = stop ] && [ "${STOP_FAIL:-false}" = true ]; then return 1; fi
  if [ "$1" = start ] || [ "$1" = restart ]; then
    [ "${START_FAIL:-false}" != true ] || return 1
  fi
  case "$1" in
    stop) printf 'inactive\n' > "$SERVICE_STATE" ;;
    start|restart) printf 'active\n' > "$SERVICE_STATE" ;;
  esac
}
journalctl() { :; }
wait_for_healthcheck() { [ "${HEALTH_FAIL:-false}" != true ]; }
run_healthcheck() { wait_for_healthcheck; }
starts() { awk '$1 == "start" || $1 == "restart" || $1 == "docker-up" { n++ } END { print n+0 }' "$EVENT_LOG"; }
# Observational commands (status, journalctl, daemon-reload) may follow the final
# stop; the invariant is that the LAST state change is the stop itself and the
# tracked service state ends inactive. Any start/restart after that stop fails.
last_state_change() {
  awk '$1 == "start" || $1 == "restart" || $1 == "stop" { line = $0 } END { print line }' "$EVENT_LOG"
}
assert_final_stop() {
  [ "$(last_state_change)" = 'stop contabase' ] || fail "$1: parada final ausente ou seguida de reativacao"
  [ "$(cat "$SERVICE_STATE")" = inactive ] || fail "$1: estado final do servico nao e inativo"
}
bundle() {
  mkdir -p "$1/templates" "$1/assets"
  printf '%s\n' "$2" > "$1/VERSION"
  printf 'synthetic-server-%s\n' "$2" > "$1/contabase"
  printf 'template\n' > "$1/templates/index.html"
  printf 'asset\n' > "$1/assets/main.css"
  chmod 0755 "$1/contabase"
}
setup() {
  local name="$1"
  CASE_ROOT="$TMP_ROOT/$name"
  mkdir -p "$CASE_ROOT/config" "$CASE_ROOT/data/backups" "$CASE_ROOT/data/uploads"
  CONFIG_DIR="$CASE_ROOT/config"; STATE_FILE="$CONFIG_DIR/install-state.env"
  INSTALL_DIR="$CASE_ROOT/active"; APP_DIR="$INSTALL_DIR"; PREVIOUS_DIR="$CASE_ROOT/previous"
  SERVICE_FILE="$CASE_ROOT/contabase.service"; SERVICE_BACKUP="$CASE_ROOT/unit.previous"
  printf 'ExecStart=%s/contabase\n' "$INSTALL_DIR" > "$SERVICE_FILE"
  cp "$SERVICE_FILE" "$SERVICE_BACKUP"
  EVENT_LOG="$CASE_ROOT/events"; : > "$EVENT_LOG"
  SERVICE_STATE="$CASE_ROOT/service-state"; printf 'inactive\n' > "$SERVICE_STATE"
  printf 'synthetic-db\n' > "$CASE_ROOT/data/contabase.db"
  printf 'synthetic-wal\n' > "$CASE_ROOT/data/contabase.db-wal"
  printf 'synthetic-shm\n' > "$CASE_ROOT/data/contabase.db-shm"
  printf 'backup\n' > "$CASE_ROOT/data/backups/old"
  printf 'upload\n' > "$CASE_ROOT/data/uploads/file"
  printf 'AUTH_ENCRYPTION_KEY=synthetic-auth\nSECURITY_MASTER_KEY=synthetic-master\n' > "$CONFIG_DIR/contabase.env"
  printf 'revoked-session\nrevoked-challenge\nhash-fixture\n' > "$CASE_ROOT/data/auth-fixture"
  PRESERVED="$(preserved_identity)"
  NEW_BUNDLE_ACTIVE=true; NEW_RUNTIME_STARTED=false; RECOVERY_COMPLETED=false
  STOP_FAIL=false; START_FAIL=false; HEALTH_FAIL=false; NONADMIN=false
  ROLLED_BACK=false
  bundle "$INSTALL_DIR" v1.0.0-beta.6
}
preserved_identity() {
  (cd "$CASE_ROOT" && find data config/contabase.env -type f -print | LC_ALL=C sort | while IFS= read -r file; do
    printf '%s\000' "$file"; recovery_sha256 < "$file"
  done) | recovery_sha256
}
preserved() { [ "$PRESERVED" = "$(preserved_identity)" ] || fail 'dados/configuracao alterados'; }
attest_bundle() {
  local root="$1" suffix="${2:-}" identity
  identity="$(recovery_bundle_identity "$root" "$suffix" "$SERVICE_BACKUP")" || fail 'identity fixture'
  recovery_record "$CONFIG_DIR/recovery" bundle v1.0.0-beta.6 "$identity" || fail 'record fixture'
}
blocked_bundle() {
  local before="$1"
  if rollback_bundle > "$CASE_ROOT/output" 2>&1; then fail 'rollback deveria bloquear'; fi
  [ "$(starts)" = 0 ] || fail 'restart indevido'
  [ "$(cat "$INSTALL_DIR/contabase")" = "$before" ] || fail 'bundle ativo substituido'
  preserved
}

# The private updater suite reuses these fixtures without running the public scenarios.
if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
  return 0
fi

for kind in install update; do
  script="$REPO_DIR/scripts/$kind-contabase-release.sh"
  lib_block="$(sed -n '/^# === SAFE_RECOVERY_BLOCK_BEGIN ===$/,/^# === SAFE_RECOVERY_BLOCK_END ===$/p' "$REPO_DIR/scripts/lib/contabase-recovery.sh")"
  [ "$lib_block" = "$(sed -n '/^# === SAFE_RECOVERY_BLOCK_BEGIN ===$/,/^# === SAFE_RECOVERY_BLOCK_END ===$/p' "$script")" ] || fail 'politica embutida divergiu'
  load_function "$script" rollback_bundle
  for version in v1.0.0-beta.5 '' nonsense v1.0.0-beta.7 v9.0.0; do
    setup "$kind-version-${version:-absent}"
    bundle "$PREVIOUS_DIR" "$version"
    blocked_bundle "$(cat "$INSTALL_DIR/contabase")"
    ok "$kind: versao '${version:-ausente}' bloqueada sem ativacao"
  done
  setup "$kind-safe"
  bundle "$PREVIOUS_DIR" v1.0.0-beta.6; attest_bundle "$PREVIOUS_DIR"
  rollback_bundle > "$CASE_ROOT/output" 2>&1 || fail "$kind: retorno seguro"
  [ "$(starts)" = 1 ] || fail 'retorno nao iniciou'
  rollback_bundle >> "$CASE_ROOT/output" 2>&1 || fail 'idempotencia'
  [ "$(starts)" = 1 ] || fail 'restart repetido'
  preserved; ok "$kind: seguro verificado antes do startup e idempotente"

  for scenario in missing-proof relabel tamper unit-tamper symlink post-start migration stop-fail; do
    setup "$kind-$scenario"
    bundle "$PREVIOUS_DIR" v1.0.0-beta.6
    case "$scenario" in
      missing-proof|relabel) ;;
      *) attest_bundle "$PREVIOUS_DIR" ;;
    esac
    case "$scenario" in
      relabel) printf 'synthetic-vulnerable-beta5\n' > "$PREVIOUS_DIR/contabase" ;;
      tamper) printf 'tampered\n' >> "$PREVIOUS_DIR/assets/main.css" ;;
      unit-tamper) printf 'tampered-unit\n' >> "$SERVICE_BACKUP" ;;
      symlink) rm "$PREVIOUS_DIR/contabase"; ln -s "$INSTALL_DIR/contabase" "$PREVIOUS_DIR/contabase" ;;
      post-start|migration) NEW_RUNTIME_STARTED=true ;;
      stop-fail) STOP_FAIL=true ;;
    esac
    blocked_bundle "$(cat "$INSTALL_DIR/contabase")"
    ok "$kind: $scenario bloqueado e estado preservado"
  done
  setup "$kind-clean"
  PREVIOUS_DIR=""; blocked_bundle "$(cat "$INSTALL_DIR/contabase")"
  ok "$kind: instalacao limpa sem bundle anterior"

  for scenario in health-fail start-fail; do
    setup "$kind-$scenario"; bundle "$PREVIOUS_DIR" v1.0.0-beta.6; attest_bundle "$PREVIOUS_DIR"
    if [ "$scenario" = health-fail ]; then HEALTH_FAIL=true; else START_FAIL=true; fi
    if rollback_bundle > "$CASE_ROOT/output" 2>&1; then fail 'retorno falho deveria falhar'; fi
    assert_final_stop "$kind/$scenario"
    rollback_bundle >> "$CASE_ROOT/output" 2>&1 && fail 'repeticao falha liberada'
    [ "$(starts)" = 1 ] || fail 'retorno falho repetiu restart'
    assert_final_stop "$kind/$scenario (repeticao)"
    preserved; ok "$kind: $scenario para servico sem repetir restart"
  done
done

load_function "$REPO_DIR/scripts/install-contabase-release.sh" record_release_recovery
for scenario in verified unverified changed-repo changed-bundle; do
  setup "release-proof-$scenario"
  EXTRACT_DIR="$CASE_ROOT/extract"; bundle "$EXTRACT_DIR/contabase" v1.0.0-beta.6
  REPO=https://github.com/contabase-app/contabase; TAG=v1.0.0-beta.6; RELEASE_BUNDLE_VERIFIED=true
  case "$scenario" in
    unverified) RELEASE_BUNDLE_VERIFIED=false ;;
    changed-repo) REPO=https://github.com/example/untrusted ;;
    changed-bundle) printf 'different\n' >> "$INSTALL_DIR/contabase" ;;
  esac
  record_release_recovery > "$CASE_ROOT/output" 2>&1
  identity="$(recovery_bundle_identity "$INSTALL_DIR" "" "$SERVICE_FILE")"
  if [ "$scenario" = verified ]; then
    recovery_verify "$CONFIG_DIR/recovery" bundle "$TAG" "$identity" || fail 'release nao emitiu prova valida'
  else
    if recovery_verify "$CONFIG_DIR/recovery" bundle "$TAG" "$identity"; then fail 'release emitiu prova indevida'; fi
  fi
  preserved; ok "release: emissao de prova $scenario"
done

load_function "$REPO_DIR/scripts/install-contabase-release.sh" stage_and_swap_bundle
load_function "$REPO_DIR/scripts/install-contabase-release.sh" render_service_file
load_function "$REPO_DIR/scripts/install-contabase-release.sh" install_bundle
load_function "$REPO_DIR/scripts/install-contabase-release.sh" rollback_bundle
ensure_user_and_directories() { :; }
manage_env_file() { :; }
install_update_command() { :; }
for scenario in clean clean-health-fail existing-vulnerable existing-safe; do
  setup "installer-flow-$scenario"
  EXTRACT_DIR="$CASE_ROOT/extract"; bundle "$EXTRACT_DIR/contabase" v1.0.0-beta.6
  TMP_DIR="$CASE_ROOT/tmp"; mkdir -p "$TMP_DIR"
  APP_USER=synthetic-user; ENV_FILE="$CONFIG_DIR/contabase.env"; DATA_DIR="$CASE_ROOT/data"
  PORT=8080; TAG=v1.0.0-beta.6; REPO=https://github.com/contabase-app/contabase; RELEASE_BUNDLE_VERIFIED=true
  case "$scenario" in
    clean*) rm -rf "$INSTALL_DIR"; rm "$SERVICE_FILE"; SERVICE_BACKUP="" ;;
    existing-vulnerable) printf 'v1.0.0-beta.5\n' > "$INSTALL_DIR/VERSION" ;;
    existing-safe) attest_bundle "$INSTALL_DIR" ;;
  esac
  if [ "$scenario" = clean-health-fail ]; then HEALTH_FAIL=true; fi
  rc=0
  (
    if [[ "$scenario" = existing-* ]]; then render_service_file() { return 1; }; fi
    install_bundle
  ) > "$CASE_ROOT/output" 2>&1 || rc=$?
  case "$scenario" in
    clean) [ "$rc" = 0 ] && [ "$(starts)" = 1 ] || fail 'instalacao limpa' ;;
    clean-health-fail) [ "$rc" != 0 ] && [ "$(starts)" = 1 ] && assert_final_stop 'installer/clean-health-fail' || fail 'health limpa' ;;
    existing-vulnerable) [ "$rc" != 0 ] && [ "$(starts)" = 0 ] || fail 'instalador reativou vulneravel' ;;
    existing-safe) [ "$rc" != 0 ] && [ "$(starts)" = 1 ] || fail 'instalador nao recuperou seguro' ;;
  esac
  preserved; ok "fluxo install_bundle: $scenario"
done

for script in "$REPO_DIR/scripts/update-contabase-source.sh"; do
  kind="$(basename "$script")"
  load_function "$script" rollback_bundle; load_function "$script" start_service
  for scenario in vulnerable safe missing-proof tamper post-start; do
    setup "$kind-$scenario"
    old_version=v1.0.0-beta.6
    [ "$scenario" != vulnerable ] || old_version=v1.0.0-beta.5
    bundle "$PREVIOUS_DIR" "$old_version"
    for item in contabase VERSION templates assets; do cp -R "$PREVIOUS_DIR/$item" "$APP_DIR/$item.previous"; done
    if [ "$scenario" != vulnerable ] && [ "$scenario" != missing-proof ]; then attest_bundle "$APP_DIR" .previous; fi
    if [ "$scenario" = tamper ]; then printf 'tamper\n' >> "$APP_DIR/contabase.previous"; fi
    if [ "$scenario" = post-start ]; then NEW_RUNTIME_STARTED=true; fi
    if [ "$scenario" = safe ]; then
      rollback_bundle > "$CASE_ROOT/output" 2>&1 || fail "$kind: seguro"
      [ "$(starts)" = 1 ] || fail 'source seguro nao iniciou'
      rollback_bundle >> "$CASE_ROOT/output" 2>&1 || fail 'source idempotencia'
      [ "$(starts)" = 1 ] || fail 'source restart repetido'
    else
      blocked_bundle "$(cat "$INSTALL_DIR/contabase")"
    fi
    preserved; ok "$kind: $scenario"
  done
  setup "$kind-direct-restart"
  if start_service > "$CASE_ROOT/output" 2>&1; then fail 'religamento sem prova'; fi
  [ "$(starts)" = 0 ] || fail 'religamento indevido'
  attest_bundle "$APP_DIR"
  start_service >> "$CASE_ROOT/output" 2>&1 || fail 'religamento seguro'
  preserved; ok "$kind: caminho anterior a troca tambem exige prova"
done

docker() {
  case " $* " in
    *' config '*) printf 'services:\n  contabase:\n    image: synthetic:mutable\n' ;;
    *' down '*) printf 'docker-down\n' >> "$EVENT_LOG" ;;
    *' up '*)
      case " $* " in *' --no-build --pull never '*) ;; *) fail 'Docker sem pin de pull/build' ;; esac
      [ "${NEW_RUNTIME_STARTED:-false}" = true ] || fail 'marcador startup Docker'
      local previous="" arg
      for arg in "$@"; do
        if [ "$previous" = -f ] && grep -q 'pull_policy: never' "$arg"; then
          grep -q "image: $PREVIOUS_IMAGE_ID" "$arg" || fail 'imagem Docker nao fixada'
        fi
        previous="$arg"
      done
      printf 'docker-up\n' >> "$EVENT_LOG" ;;
    *' inspect '*) printf '%s\n' "$MOCK_IMAGE" ;;
    *' logs '*) : ;;
    *) fail "Docker inesperado: $*" ;;
  esac
}
detect_confirmed_runtime_version() {
  CONFIRMED_RUNTIME_VERSION="$PREVIOUS_VERSION"; RESOLVED_CONTAINER_ID=synthetic-container
  [ "$HEALTH_FAIL" != true ]
}
load_function "$REPO_DIR/scripts/update-contabase-docker.sh" rollback_runtime
for scenario in vulnerable safe unknown missing-proof image-mismatch compose-change post-start health-fail; do
  setup "docker-$scenario"; REPO_ROOT="$CASE_ROOT"; COMPOSE_ARGS=""; STACK_STOPPED=true
  PREVIOUS_IMAGE_ID="sha256:$(printf 'synthetic-safe-image' | recovery_sha256)"; MOCK_IMAGE="$PREVIOUS_IMAGE_ID"
  PREVIOUS_VERSION=v1.0.0-beta.6; HEALTH_URL=synthetic-health
  PREVIOUS_ROLLBACK_TAG=synthetic-preserved; PREVIOUS_IMAGE_NAME=synthetic:mutable
  PREVIOUS_COMPOSE_ID="$(docker compose config | recovery_sha256)"
  case "$scenario" in vulnerable) PREVIOUS_VERSION=v1.0.0-beta.5 ;; unknown) PREVIOUS_VERSION=v9.0.0 ;; esac
  if [ "$scenario" != vulnerable ] && [ "$scenario" != unknown ] && [ "$scenario" != missing-proof ]; then
    recovery_record "$CONFIG_DIR/recovery" docker "$PREVIOUS_VERSION" "${PREVIOUS_IMAGE_ID#sha256:}" || fail 'Docker prova'
  fi
  case "$scenario" in
    image-mismatch) PREVIOUS_IMAGE_ID="sha256:$(printf 'changed' | recovery_sha256)" ;;
    compose-change) PREVIOUS_COMPOSE_ID=changed ;;
    post-start) NEW_RUNTIME_STARTED=true ;;
    health-fail) HEALTH_FAIL=true ;;
  esac
  if [ "$scenario" = safe ]; then
    rollback_runtime > "$CASE_ROOT/output" 2>&1 || fail 'Docker seguro'
    rollback_runtime >> "$CASE_ROOT/output" 2>&1 || fail 'Docker idempotencia'
    [ "$(starts)" = 1 ] || fail 'Docker restart repetido'
  else
    if rollback_runtime > "$CASE_ROOT/output" 2>&1; then fail "Docker deveria bloquear: $scenario"; fi
    if [ "$scenario" = health-fail ]; then
      [ "$(starts)" = 1 ] && [ "$(tail -n1 "$EVENT_LOG")" = docker-down ] || fail 'Docker health nao parou'
    else
      [ "$(starts)" = 0 ] || fail 'Docker vulneravel ativado'
    fi
  fi
  preserved; ok "Docker: $scenario"
done

setup proof-permissions; attest_bundle "$APP_DIR"
identity="$(recovery_bundle_identity "$APP_DIR" "" "$SERVICE_FILE")"
NONADMIN=true
if recovery_verify "$CONFIG_DIR/recovery" bundle v1.0.0-beta.6 "$identity"; then fail 'prova de usuario aceita'; fi
NONADMIN=false
chmod 0777 "$CONFIG_DIR/recovery"
if recovery_verify "$CONFIG_DIR/recovery" bundle v1.0.0-beta.6 "$identity"; then fail 'prova gravavel aceita'; fi
chmod 0700 "$CONFIG_DIR/recovery"
proof="$CONFIG_DIR/recovery/bundle-$identity.proof"; mv "$proof" "$proof.original"; ln -s "$proof.original" "$proof"
if recovery_verify "$CONFIG_DIR/recovery" bundle v1.0.0-beta.6 "$identity"; then fail 'symlink de prova aceito'; fi
ok 'provas: ownership, permissoes e symlinks falham fechados'

setup source-provenance
recovery_source_known "$REPO_DIR" || fail 'fontes homologadas nao reconhecidas'
mkdir -p "$CASE_ROOT/source/cmd/server" "$CASE_ROOT/source/internal/security"
cp "$REPO_DIR/VERSION" "$CASE_ROOT/source/VERSION"
for path in cmd/server/main.go cmd/server/server_runtime.go cmd/server/legacy_seed_credentials.go internal/security/crypto.go; do
  cp "$REPO_DIR/$path" "$CASE_ROOT/source/$path"
done
printf '\nchanged\n' >> "$CASE_ROOT/source/internal/security/crypto.go"
if recovery_source_known "$CASE_ROOT/source"; then fail 'source relabelado aceito'; fi
ok 'source: VERSION correto nao substitui prova das correcoes'

printf 'safe recovery: PASS (%d cenarios)\n' "$PASS"
