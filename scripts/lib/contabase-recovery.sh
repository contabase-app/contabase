# Recovery evidence is administrative state, never imported from a release bundle.
# === SAFE_RECOVERY_BLOCK_BEGIN ===
recovery_version_known() {
  [ "$1" = "v1.0.0-beta.6" ]
}

recovery_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum | awk '{print $1}'
  else
    shasum -a 256 | awk '{print $1}'
  fi
}

recovery_admin_path() {
  local path="$1" owner mode
  while :; do
    [ -e "$path" ] && [ ! -L "$path" ] || return 1
    owner="$(stat -c %u "$path" 2>/dev/null)" || owner="$(stat -f %u "$path")" || return 1
    mode="$(stat -c %a "$path" 2>/dev/null)" || mode="$(stat -f %Lp "$path")" || return 1
    [ "$owner" = 0 ] && [[ "$mode" =~ ^[0-7]{3,4}$ ]] || return 1
    (( (8#$mode & 0022) == 0 )) || return 1
    [ "$path" != / ] || break
    path="$(dirname "$path")"
  done
}

recovery_source_known() {
  local root="$1" expected path actual
  [ "$(cat "$root/VERSION" 2>/dev/null)" = v1.0.0-beta.6 ] || return 1
  # Exact reviewed security sources; a version label cannot attest a local build.
  while read -r expected path; do
    [ -f "$root/$path" ] && [ ! -L "$root/$path" ] || return 1
    actual="$(recovery_sha256 < "$root/$path")" || return 1
    [ "$actual" = "$expected" ] || return 1
  done <<'RECOVERY_SOURCES'
e00dcd07672e3f89242ffecf489926621020e18e943d3becd5ad8766140db674 cmd/server/main.go
28dac28177886cef83978757d943e013924af575ae4ac58052de0a668e29487c cmd/server/server_runtime.go
0fc224bc1fa1b27cc339aaaede07f28cdc204ae5a0cc17c8bd3c8fe8589340b5 cmd/server/legacy_seed_credentials.go
e6d72d81fd2d8e4089d718e1027faf214bd5791dff7f14c0eb9d821a6637ab8a internal/security/crypto.go
RECOVERY_SOURCES
}

recovery_bundle_identity() (
  set -euo pipefail
  local root="$1" suffix="$2" unit="$3" version path file files bad digest
  [ -d "$root" ] && [ ! -L "$root" ] && [ -f "$unit" ] && [ ! -L "$unit" ] || exit 1
  cd "$root"
  version="$(cat "VERSION$suffix")"
  recovery_version_known "$version" || exit 1
  for path in contabase VERSION templates assets; do
    [ -e "$path$suffix" ] && [ ! -L "$path$suffix" ] || exit 1
  done
  [ -f "contabase$suffix" ] && [ -f "VERSION$suffix" ] \
    && [ -d "templates$suffix" ] && [ -d "assets$suffix" ] || exit 1
  set -- "contabase$suffix" "VERSION$suffix" "templates$suffix" "assets$suffix"
  if [ -e "admin$suffix" ] || [ -L "admin$suffix" ]; then
    [ -f "admin$suffix" ] && [ ! -L "admin$suffix" ] || exit 1
    set -- "$@" "admin$suffix"
  fi
  bad="$(find "$@" ! -type f ! -type d -print)" || exit 1
  [ -z "$bad" ] || exit 1
  files="$(find "$@" -type f -print | LC_ALL=C sort)" || exit 1
  # Hash a framed manifest, including logical paths, without executing old code.
  {
    printf 'bundle-v1\000%s\000' "$version"
    recovery_sha256 < "$unit"
    while IFS= read -r file; do
      [ -f "$file" ] && [ ! -L "$file" ] || exit 1
      if [ -n "$suffix" ]; then
        path="${file%%"$suffix"*}${file#*"$suffix"}"
      else
        path="$file"
      fi
      digest="$(recovery_sha256 < "$file")" || exit 1
      printf '%s\000%s\000' "$path" "$digest"
    done <<< "$files"
  } | recovery_sha256
)

recovery_proof_text() {
  printf 'contabase-recovery-v1\nkind=%s\nversion=%s\nidentity=%s\n' "$1" "$2" "$3"
}

recovery_record() {
  local directory="$1" kind="$2" version="$3" identity="$4" tmp
  case "$kind" in bundle|docker) ;; *) return 1 ;; esac
  recovery_version_known "$version" || return 1
  [[ "$identity" =~ ^[0-9a-f]{64}$ ]] || return 1
  recovery_admin_path "$(dirname "$directory")" || return 1
  [ ! -L "$directory" ] || return 1
  install -d -o root -g root -m 0700 "$directory" || return 1
  recovery_admin_path "$directory" || return 1
  tmp="$(mktemp "$directory/.proof.XXXXXX")" || return 1
  if ! recovery_proof_text "$kind" "$version" "$identity" > "$tmp" \
    || ! chmod 0600 "$tmp" || ! mv -f "$tmp" "$directory/$kind-$identity.proof"; then
    rm -f "$tmp"
    return 1
  fi
}

recovery_verify() {
  local directory="$1" kind="$2" version="$3" identity="$4" proof
  case "$kind" in bundle|docker) ;; *) return 1 ;; esac
  recovery_version_known "$version" || return 1
  [[ "$identity" =~ ^[0-9a-f]{64}$ ]] || return 1
  proof="$directory/$kind-$identity.proof"
  [ -f "$proof" ] && recovery_admin_path "$proof" || return 1
  recovery_proof_text "$kind" "$version" "$identity" | cmp -s - "$proof"
}

recovery_blocked() {
  printf '%s\n' \
    'RECOVERY_BLOCKED: nenhum codigo anterior sera ativado automaticamente.' \
    'Recuperacao exige prova de codigo corrigido e nenhuma tentativa de startup da candidata.' \
    'Mantenha o servico parado. Preserve banco, WAL, uploads, configuracao, chaves e backups.' \
    'Repare a falha e use um artefato corrigido, verificado e compativel com o schema atual.' \
    'Nao use downgrade como resgate. Restore de banco exige plano e decisao explicita.' >&2
}
# === SAFE_RECOVERY_BLOCK_END ===
