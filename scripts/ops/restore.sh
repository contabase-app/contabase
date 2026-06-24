#!/usr/bin/env sh
set -eu

if [ "$#" -lt 1 ]; then
  echo "uso: $0 <backup-dir>" >&2
  exit 1
fi

BACKUP_DIR="$1"
DATA_DIR="${DATA_DIR:-data}"
DB_FILE="${DB_FILE:-$DATA_DIR/contabase.db}"
UPLOADS_DIR="${UPLOADS_DIR:-$DATA_DIR/uploads}"
CONFIRM_APP_STOPPED="${CONFIRM_APP_STOPPED:-}"

if [ ! -d "$BACKUP_DIR" ]; then
  echo "erro: backup-dir inexistente: $BACKUP_DIR" >&2
  exit 1
fi

if [ "$CONFIRM_APP_STOPPED" != "yes" ]; then
  echo "erro: restore bloqueado. pare a aplicacao e execute com CONFIRM_APP_STOPPED=yes." >&2
  exit 1
fi

if ! command -v sqlite3 >/dev/null 2>&1; then
  echo "erro: sqlite3 nao encontrado. restore seguro exige validacao por integrity_check; abortando." >&2
  exit 1
fi

BACKUP_DB="$BACKUP_DIR/$(basename "$DB_FILE")"
BACKUP_UPLOADS="$BACKUP_DIR/uploads"

if [ ! -f "$BACKUP_DB" ]; then
  echo "erro: arquivo de banco nao encontrado no backup: $BACKUP_DB" >&2
  exit 1
fi

is_sqlite_file() {
  file="$1"
  header_hex="$(dd if="$file" bs=16 count=1 2>/dev/null | od -An -t x1 | tr -d ' \n')"
  [ "$header_hex" = "53514c69746520666f726d6174203300" ]
}

sqlite_integrity_ok() {
  file="$1"
  sqlite3 "$file" "PRAGMA integrity_check;" | tr -d '\r' | grep -qi '^ok$'
}

if ! is_sqlite_file "$BACKUP_DB"; then
  echo "erro: backup candidato invalido (magic header SQLite ausente)." >&2
  exit 1
fi

if ! sqlite_integrity_ok "$BACKUP_DB"; then
  echo "erro: backup candidato reprovado no PRAGMA integrity_check." >&2
  exit 1
fi

mkdir -p "$DATA_DIR"
mkdir -p "$(dirname "$UPLOADS_DIR")"

TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
PRE_RESTORE_DB="$DB_FILE.pre-restore.$TIMESTAMP.db"
CURRENT_DB_STAGED="$DB_FILE.restore-current.$TIMESTAMP.db"
STAGE_DB="$DATA_DIR/.restore-stage-$TIMESTAMP.db"
UPLOADS_STAGE="$DATA_DIR/.uploads-restore-stage-$TIMESTAMP"
UPLOADS_PREVIOUS="$UPLOADS_DIR.pre-restore.$TIMESTAMP"
RESTORE_DONE=0

rollback_restore() {
  if [ "$RESTORE_DONE" -eq 1 ]; then
    return
  fi
  if [ -f "$CURRENT_DB_STAGED" ]; then
    rm -f "$DB_FILE"
    mv "$CURRENT_DB_STAGED" "$DB_FILE" || true
  elif [ -f "$PRE_RESTORE_DB" ]; then
    rm -f "$DB_FILE"
    cp "$PRE_RESTORE_DB" "$DB_FILE" || true
  fi

  if [ -d "$UPLOADS_PREVIOUS" ]; then
    rm -rf "$UPLOADS_DIR"
    mv "$UPLOADS_PREVIOUS" "$UPLOADS_DIR" || true
  fi
}

trap 'status=$?; if [ "$status" -ne 0 ]; then rollback_restore; fi; rm -f "$STAGE_DB"; rm -rf "$UPLOADS_STAGE"; if [ "$status" -eq 0 ] && [ -d "$UPLOADS_PREVIOUS" ]; then rm -rf "$UPLOADS_PREVIOUS"; fi' EXIT

if [ -f "$DB_FILE" ]; then
  if ! sqlite3 "$DB_FILE" ".backup '$PRE_RESTORE_DB'"; then
    echo "erro: falha ao criar backup pre-restore de $DB_FILE." >&2
    exit 1
  fi
  mv "$DB_FILE" "$CURRENT_DB_STAGED"
fi

rm -f "$DB_FILE-wal" "$DB_FILE-shm"
rm -f "$CURRENT_DB_STAGED-wal" "$CURRENT_DB_STAGED-shm"
rm -f "$BACKUP_DB-wal" "$BACKUP_DB-shm"

cp "$BACKUP_DB" "$STAGE_DB"

if ! is_sqlite_file "$STAGE_DB"; then
  echo "erro: staging do restore invalido (magic header SQLite ausente)." >&2
  exit 1
fi

if ! sqlite_integrity_ok "$STAGE_DB"; then
  echo "erro: staging do restore reprovado no PRAGMA integrity_check." >&2
  exit 1
fi

mv "$STAGE_DB" "$DB_FILE"

if [ -d "$BACKUP_UPLOADS" ]; then
  cp -R "$BACKUP_UPLOADS" "$UPLOADS_STAGE"
  if [ -d "$UPLOADS_DIR" ]; then
    mv "$UPLOADS_DIR" "$UPLOADS_PREVIOUS"
  fi
  mv "$UPLOADS_STAGE" "$UPLOADS_DIR"
fi

RESTORE_DONE=1
echo "restore concluido com seguranca para $DB_FILE (app deve permanecer parada ate validacao final)"
