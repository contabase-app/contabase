#!/usr/bin/env sh
set -eu

if [ "$#" -lt 1 ]; then
  echo "uso: $0 <destino-backup-dir>" >&2
  exit 1
fi

DEST_DIR="$1"
DATA_DIR="${DATA_DIR:-data}"
DB_FILE="${DB_FILE:-$DATA_DIR/contabase.db}"
UPLOADS_DIR="${UPLOADS_DIR:-$DATA_DIR/uploads}"

TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
OUT_DIR="$DEST_DIR/contabase-backup-$TIMESTAMP"

mkdir -p "$OUT_DIR"

if [ ! -f "$DB_FILE" ]; then
  echo "erro: banco nao encontrado em $DB_FILE" >&2
  exit 1
fi

SQLITE_BASE="$(basename "$DB_FILE")"
BACKUP_DB="$OUT_DIR/$SQLITE_BASE"

if ! command -v sqlite3 >/dev/null 2>&1; then
  echo "erro: sqlite3 nao encontrado. backup SQLite em WAL exige sqlite3 .backup; abortando sem copiar .db cru." >&2
  exit 1
fi

if ! sqlite3 "$DB_FILE" ".backup '$BACKUP_DB'"; then
  echo "erro: falha ao gerar backup consistente via sqlite3 .backup." >&2
  exit 1
fi

if ! sqlite3 "$BACKUP_DB" "PRAGMA integrity_check;" | grep -qi '^ok$'; then
  echo "erro: backup gerado falhou no integrity_check." >&2
  exit 1
fi

if [ -d "$UPLOADS_DIR" ]; then
  cp -R "$UPLOADS_DIR" "$OUT_DIR/uploads"
else
  mkdir -p "$OUT_DIR/uploads"
fi

{
  echo "created_at=$TIMESTAMP"
  echo "db_file=$DB_FILE"
  echo "backup_db=$BACKUP_DB"
  echo "uploads_dir=$UPLOADS_DIR"
  echo "method=sqlite3_backup"
  echo "wal_consistency=ok"
} > "$OUT_DIR/manifest.txt"

echo "backup criado em: $OUT_DIR"
