#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "uso: $0 <destino-backup-dir>" >&2
  exit 1
fi

DEST_DIR="$1"
DATA_DIR="${DATA_DIR:-data}"
DB_FILE="${DB_FILE:-$DATA_DIR/contabase.db}"
UPLOADS_DIR="${UPLOADS_DIR:-$DATA_DIR/uploads}"
APP_VERSION="${APP_VERSION-}"

fail() {
  echo "erro: $*" >&2
  exit 1
}

sha_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

file_size() {
  local path="$1" value

  if value="$(stat -f '%z' "$path" 2>/dev/null)"; then
    case "$value" in ''|*[!0-9]*) return 1 ;; esac
    printf '%s\n' "$value"
    return 0
  fi
  if value="$(stat -c '%s' "$path" 2>/dev/null)"; then
    case "$value" in ''|*[!0-9]*) return 1 ;; esac
    printf '%s\n' "$value"
    return 0
  fi
  return 1
}

validate_manifest_value() {
  local key="$1" value="$2"
  case "$value" in
    *$'\n'*|*$'\r'*|*$'\t'*) fail "valor inseguro para $key" ;;
  esac
}

[ -f "$DB_FILE" ] || fail "banco nao encontrado em $DB_FILE"
[ -d "$DEST_DIR" ] || fail "diretorio de destino nao encontrado em $DEST_DIR"
command -v sqlite3 >/dev/null 2>&1 || fail "sqlite3 nao encontrado"
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
  fail "sha256sum ou shasum nao encontrado"
fi

if [ -z "$APP_VERSION" ]; then
  [ -f VERSION ] || fail "versao da aplicacao nao informada e VERSION ausente"
  APP_VERSION="$(<VERSION)"
fi
validate_manifest_value application_version "$APP_VERSION"
[ -n "$APP_VERSION" ] || fail "versao da aplicacao vazia"
APP_VERSION_LOWER="$(printf '%s' "$APP_VERSION" | tr 'A-Z' 'a-z')"
[ "$APP_VERSION_LOWER" != "unknown" ] || fail "versao da aplicacao desconhecida"

TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
PARTIAL_DIR="$(mktemp -d "$DEST_DIR/.backup-partial.${TIMESTAMP}.XXXXXX")"
PARTIAL_BASENAME="${PARTIAL_DIR##*/}"
UNIQUE_SUFFIX="${PARTIAL_BASENAME##*.}"
BACKUP_ID="backup-${TIMESTAMP}-${UNIQUE_SUFFIX}"
FINAL_DIR="$DEST_DIR/$BACKUP_ID"
INVENTORY_TMP="$PARTIAL_DIR/.payload-inventory"

cleanup_partial() {
  if [ -n "${PARTIAL_DIR:-}" ] && [ -d "$PARTIAL_DIR" ]; then
    rm -rf "$PARTIAL_DIR"
  fi
}
trap cleanup_partial EXIT

BACKUP_DB="$PARTIAL_DIR/database.sqlite"
if ! sqlite3 "$DB_FILE" ".backup '$BACKUP_DB'"; then
  fail "sqlite3 .backup falhou"
fi

JOURNAL_MODE="$(sqlite3 -batch -noheader "$BACKUP_DB" 'PRAGMA journal_mode=DELETE;' | tr -d '\r')"
[ "$JOURNAL_MODE" = delete ] || fail "nao foi possivel normalizar journal_mode do backup"
for sidecar in "$BACKUP_DB-wal" "$BACKUP_DB-shm"; do
  if [ -e "$sidecar" ] && ! rm -f "$sidecar"; then
    fail "nao foi possivel remover sidecar do DB copiado: $sidecar"
  fi
done
[ ! -e "$BACKUP_DB-wal" ] && [ ! -e "$BACKUP_DB-shm" ] || fail "WAL ou SHM permaneceu no pacote"

INTEGRITY="$(sqlite3 -batch -noheader "$BACKUP_DB" 'PRAGMA integrity_check;' | tr -d '\r')"
[ "$INTEGRITY" = "ok" ] || fail "integrity_check falhou: $INTEGRITY"

FK_OUTPUT="$(sqlite3 -batch -noheader "$BACKUP_DB" 'PRAGMA foreign_key_check;' | tr -d '\r')"
[ -z "$FK_OUTPUT" ] || fail "foreign_key_check encontrou violacoes"

if ! SCHEMA_VERSION="$(sqlite3 -batch -noheader "$BACKUP_DB" 'SELECT MAX(version) FROM schema_migrations;' 2>/dev/null)"; then
  fail "nao foi possivel consultar schema_version"
fi
case "$SCHEMA_VERSION" in
  ''|*[!0-9]*) fail "schema_version nao comprovado" ;;
esac
[ "$SCHEMA_VERSION" -gt 0 ] || fail "schema_version nao comprovado"

mkdir "$PARTIAL_DIR/uploads"
if [ -e "$UPLOADS_DIR" ] && [ ! -d "$UPLOADS_DIR" ]; then
  fail "uploads nao e diretorio: $UPLOADS_DIR"
fi
if [ -d "$UPLOADS_DIR" ]; then
  if ! cp -pR "$UPLOADS_DIR/." "$PARTIAL_DIR/uploads/"; then
    fail "falha ao copiar uploads"
  fi
fi

if ! find "$PARTIAL_DIR/uploads" -mindepth 1 -print0 > "$PARTIAL_DIR/.entries"; then
  fail "falha ao enumerar uploads"
fi
: > "$INVENTORY_TMP"
while IFS= read -r -d '' entry; do
  rel="${entry#"$PARTIAL_DIR/uploads/"}"
  case "$rel" in
    ''|*$'\n'*|*$'\r'*|*$'\t'*) fail "nome de upload inseguro" ;;
  esac
  if [ -L "$entry" ]; then
    fail "symlink nao permitido em uploads: $rel"
  elif [ -d "$entry" ]; then
    continue
  elif [ -f "$entry" ]; then
    printf 'uploads/%s\n' "$rel" >> "$INVENTORY_TMP"
  else
    fail "tipo nao regular em uploads: $rel"
  fi
done < "$PARTIAL_DIR/.entries"
rm "$PARTIAL_DIR/.entries"

LC_ALL=C sort -o "$INVENTORY_TMP" "$INVENTORY_TMP"
UPLOAD_COUNT="$(wc -l < "$INVENTORY_TMP" | tr -d ' ')"
DB_SIZE="$(file_size "$BACKUP_DB")" || fail "nao foi possivel obter tamanho do banco"
DB_SHA="$(sha_file "$BACKUP_DB")" || fail "nao foi possivel calcular SHA-256 do banco"
case "$DB_SIZE" in ''|*[!0-9]*) fail "tamanho do banco invalido" ;; esac
case "$DB_SHA" in
  *[!0-9a-fA-F]*|'') fail "SHA-256 do banco invalido" ;;
esac
[ "${#DB_SHA}" -eq 64 ] || fail "SHA-256 do banco invalido"

CHECKSUMS="$PARTIAL_DIR/checksums.sha256"
: > "$CHECKSUMS"
printf '%s  database.sqlite\n' "$DB_SHA" >> "$CHECKSUMS"
while IFS= read -r payload_path; do
  [ -n "$payload_path" ] || fail "entrada vazia no inventario"
  payload_sha="$(sha_file "$PARTIAL_DIR/$payload_path")" || fail "falha ao calcular checksum: $payload_path"
  printf '%s  %s\n' "$payload_sha" "$payload_path" >> "$CHECKSUMS"
done < "$INVENTORY_TMP"
rm "$INVENTORY_TMP"

validate_manifest_value backup_id "$BACKUP_ID"
validate_manifest_value created_at_utc "$TIMESTAMP"
validate_manifest_value schema_version "$SCHEMA_VERSION"
validate_manifest_value source_db_path "$DB_FILE"

cat > "$PARTIAL_DIR/manifest.txt" <<MANIFEST
format_version=1
backup_id=$BACKUP_ID
created_at_utc=$TIMESTAMP
application_version=$APP_VERSION
schema_version=$SCHEMA_VERSION
source_db_path=$DB_FILE
method=sqlite-backup
wal_consistency_method=sqlite-online-backup
database_file=database.sqlite
uploads_directory=uploads
database_size=$DB_SIZE
database_sha256=$DB_SHA
uploads_file_count=$UPLOAD_COUNT
integrity_check=ok
foreign_key_check=ok
MANIFEST

if ! find "$PARTIAL_DIR" -mindepth 1 -maxdepth 1 -print0 > "$PARTIAL_DIR/.root-entries"; then
  fail "falha ao enumerar componentes finais do pacote"
fi
while IFS= read -r entry; do
  case "${entry##*/}" in
    database.sqlite|uploads|checksums.sha256|manifest.txt|.root-entries) ;;
    *) fail "componente inesperado antes da finalizacao: ${entry##*/}" ;;
  esac
done < <(tr '\0' '\n' < "$PARTIAL_DIR/.root-entries")
rm "$PARTIAL_DIR/.root-entries"
[ ! -e "$BACKUP_DB-wal" ] && [ ! -e "$BACKUP_DB-shm" ] || fail "WAL ou SHM permaneceu no pacote"

[ ! -e "$FINAL_DIR" ] || fail "pacote final ja existe: $FINAL_DIR"
if ! mv "$PARTIAL_DIR" "$FINAL_DIR"; then
  fail "falha ao finalizar pacote"
fi
trap - EXIT
echo "$FINAL_DIR"
