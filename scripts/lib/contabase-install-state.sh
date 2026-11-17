#!/usr/bin/env bash
# Contrato de estado e proveniencia para instalacoes Source/Docker legadas.
# O arquivo de estado e lido como dados; nunca e executado como shell.
# Tag local comprova apenas coerencia/proveniencia local; ela NAO e prova de
# autenticidade remota. O fluxo de update (F1) deve buscar a tag exata na
# origem oficial e validar commit/versao antes de qualquer mutacao.
# Nao contem logica de update, backup, restore ou troca de versao.

CONTABASE_OFFICIAL_ORIGIN="https://github.com/contabase-app/contabase.git"
CONTABASE_STATE_MAX_BYTES=32768

state_error() {
  printf 'Erro de estado: %s\n' "$*" >&2
  return 1
}

state_value() {
  local file="$1" key="$2" value
  [ -n "$file" ] && [ -f "$file" ] || {
    printf ''
    return 0
  }
  value="$(awk -F= -v key="$key" '
    $1 == key { print substr($0, index($0, "=") + 1); exit }
  ' "$file")" || return 1
  value="${value%$'\r'}"
  case "$value" in
    \"*\") value="${value#\"}"; value="${value%\"}" ;;
    \'*\') value="${value#\'}"; value="${value%\'}" ;;
  esac
  printf '%s' "$value"
}

state_has_key() {
  local file="$1" key="$2"
  [ -n "$file" ] && [ -f "$file" ] || return 1
  awk -F= -v key="$key" '$1 == key { found = 1; exit } END { exit found ? 0 : 1 }' "$file"
}

state_count_lines() {
  printf '%s\n' "${1:-}" | sed '/^[[:space:]]*$/d' | wc -l | tr -d '[:space:]'
}

state_normalize_origin() {
  local value="${1:-}"

  case "$value" in
    ""|*[[:space:]]*|*$'\n'*|*$'\r'*|*$'\t'*) return 1 ;;
  esac
  value="${value%/}"

  case "$value" in
    https://github.com/contabase-app/contabase|\
    https://github.com/contabase-app/contabase.git|\
    git@github.com:contabase-app/contabase|\
    git@github.com:contabase-app/contabase.git|\
    ssh://git@github.com/contabase-app/contabase|\
    ssh://git@github.com/contabase-app/contabase.git)
      printf '%s' "$CONTABASE_OFFICIAL_ORIGIN"
      ;;
    *)
      return 1
      ;;
  esac
}

state_repo_origin() {
  local repo="$1" urls count url origin

  urls="$(git -C "$repo" config --get-all remote.origin.url 2>/dev/null || true)"
  count="$(state_count_lines "$urls")"
  [ "$count" = "1" ] || {
    state_error "origin ausente ou ambiguo no checkout: $repo"
    return 1
  }
  url="$(printf '%s\n' "$urls" | sed -n '1p')"
  origin="$(state_normalize_origin "$url")" || {
    state_error "origem Git nao oficial: $url"
    return 1
  }
  printf '%s' "$origin"
}

state_validate_file() {
  local file="$1" size key

  [ -n "$file" ] && [ -f "$file" ] && [ ! -L "$file" ] || {
    state_error "arquivo ausente, irregular ou symlink: ${file:-<vazio>}"
    return 1
  }
  size="$(wc -c < "$file" | tr -d '[:space:]')" || return 1
  case "$size" in
    ""|*[!0-9]*) state_error "tamanho invalido: $file"; return 1 ;;
  esac
  [ "$size" -le "$CONTABASE_STATE_MAX_BYTES" ] || {
    state_error "arquivo excede ${CONTABASE_STATE_MAX_BYTES} bytes: $file"
    return 1
  }

  awk -F= '
    /^[[:space:]]*$/ || /^[[:space:]]*#/ { next }
    {
      if (index($0, "=") == 0 || $1 !~ /^CONTABASE_[A-Z0-9_]+$/) exit 1
      if ($1 in seen) exit 1
      seen[$1] = 1
      if ($1 ~ /(SECRET|TOKEN|PASSWORD|KEY|CREDENTIAL)/) exit 1
      value = substr($0, index($0, "=") + 1)
      if (value ~ /[\r\n]/) exit 1
    }
  ' "$file" || {
    state_error "sintaxe, duplicata ou campo proibido: $file"
    return 1
  }

  for key in \
    CONTABASE_STATE_VERSION \
    CONTABASE_INSTALL_METHOD \
    CONTABASE_CHANNEL \
    CONTABASE_INSTALLED_VERSION \
    CONTABASE_REPO_PATH
  do
    state_has_key "$file" "$key" || {
      state_error "campo obrigatorio ausente em $file: $key"
      return 1
    }
  done

  [ "$(state_value "$file" CONTABASE_STATE_VERSION)" = "1" ] || {
    state_error "CONTABASE_STATE_VERSION invalido em $file"
    return 1
  }
  case "$(state_value "$file" CONTABASE_INSTALL_METHOD)" in
    release|binary|docker|docker-image|source|source-bootstrap) ;;
    *) state_error "metodo de instalacao invalido em $file"; return 1 ;;
  esac
  case "$(state_value "$file" CONTABASE_CHANNEL)" in
    beta|stable|pinned) ;;
    *) state_error "canal invalido em $file"; return 1 ;;
  esac
  [ -n "$(state_value "$file" CONTABASE_INSTALLED_VERSION)" ] || {
    state_error "versao instalada ausente em $file"
    return 1
  }
}

state_render() {
  local file="$1" method="$2" channel="$3" version="$4" repo_path="$5" kind="${6:-}" ref="${7:-}" commit="${8:-}" origin="${9:-}"
  local existing_kind="" existing_ref="" existing_commit="" existing_origin=""
  local line

  if [ -n "$file" ] && [ -e "$file" ]; then
    state_validate_file "$file" || return 1
    existing_kind="$(state_value "$file" CONTABASE_INSTALL_REF_KIND)"
    existing_ref="$(state_value "$file" CONTABASE_INSTALL_REF)"
    existing_commit="$(state_value "$file" CONTABASE_INSTALL_COMMIT)"
    existing_origin="$(state_value "$file" CONTABASE_INSTALL_ORIGIN)"
  fi

  [ -n "$kind" ] || kind="$existing_kind"
  [ -n "$ref" ] || ref="$existing_ref"
  [ -n "$commit" ] || commit="$existing_commit"
  [ -n "$origin" ] || origin="$existing_origin"

  case "$method" in
    source|docker|source-bootstrap|release|binary) ;;
    *) state_error "metodo invalido para o estado: $method"; return 1 ;;
  esac
  case "$channel" in
    beta|stable|pinned) ;;
    *) state_error "canal invalido para o estado: $channel"; return 1 ;;
  esac
  [ -n "$version" ] || { state_error "versao vazia para o estado"; return 1; }
  case "$version" in
    *[[:space:]]*|*$'\n'*|*$'\r'*) state_error "versao com caractere invalido: $version"; return 1 ;;
  esac
  case "$repo_path" in
    /*) ;;
    *) state_error "caminho do checkout deve ser absoluto: $repo_path"; return 1 ;;
  esac
  case "$repo_path" in
    *[[:space:]]*|*$'\n'*|*$'\r'*) state_error "caminho do checkout com caractere invalido"; return 1 ;;
  esac

  if [ -z "$kind" ]; then
    [ -z "$ref" ] && [ -z "$commit" ] && [ -z "$origin" ] || {
      state_error "proveniencia parcial: campos novos incompletos"
      return 1
    }
  else
    case "$kind" in
      tag|branch) ;;
      *) state_error "REF_KIND invalido: $kind"; return 1 ;;
    esac
    [ -n "$ref" ] || { state_error "REF vazio com REF_KIND definido"; return 1; }
    case "$ref" in
      *[!A-Za-z0-9._/-]*|*$'\n'*|*$'\r'*) state_error "REF com caractere invalido: $ref"; return 1 ;;
    esac
    [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || {
      state_error "COMMIT deve conter 40 hex minusculos"
      return 1
    }
    origin="$(state_normalize_origin "$origin")" || {
      state_error "ORIGIN nao e a origem oficial normalizada"
      return 1
    }
    if [ "$kind" = "tag" ]; then
      [ "$ref" = "$version" ] || {
        state_error "tag ${ref} diverge da versao ${version}"
        return 1
      }
    fi
  fi

  if [ -n "$file" ] && [ -e "$file" ]; then
    while IFS= read -r line || [ -n "$line" ]; do
      case "$line" in
        CONTABASE_STATE_VERSION=*|\
        CONTABASE_INSTALL_METHOD=*|\
        CONTABASE_CHANNEL=*|\
        CONTABASE_INSTALLED_VERSION=*|\
        CONTABASE_REPO_PATH=*|\
        CONTABASE_INSTALL_REF_KIND=*|\
        CONTABASE_INSTALL_REF=*|\
        CONTABASE_INSTALL_COMMIT=*|\
        CONTABASE_INSTALL_ORIGIN=*)
          continue
          ;;
      esac
      printf '%s\n' "$line"
    done < "$file"
  fi

  printf 'CONTABASE_STATE_VERSION=1\n'
  printf 'CONTABASE_INSTALL_METHOD=%s\n' "$method"
  printf 'CONTABASE_CHANNEL=%s\n' "$channel"
  printf 'CONTABASE_INSTALLED_VERSION=%s\n' "$version"
  printf 'CONTABASE_REPO_PATH=%s\n' "$repo_path"
  if [ -n "$kind" ]; then
    printf 'CONTABASE_INSTALL_REF_KIND=%s\n' "$kind"
    printf 'CONTABASE_INSTALL_REF=%s\n' "$ref"
    printf 'CONTABASE_INSTALL_COMMIT=%s\n' "$commit"
    printf 'CONTABASE_INSTALL_ORIGIN=%s\n' "$origin"
  fi
}

state_write_atomic() {
  local file="$1"; shift
  local dir tmp

  dir="$(dirname "$file")"
  mkdir -p "$dir" || {
    state_error "nao foi possivel criar o diretorio de estado: $dir"
    return 1
  }
  tmp="$(mktemp "${dir}/.install-state.XXXXXX")" || {
    state_error "nao foi possivel criar temporario em $dir"
    return 1
  }
  if ! state_render "$file" "$@" > "$tmp"; then
    rm -f "$tmp"
    return 1
  fi
  chmod 0644 "$tmp" || {
    rm -f "$tmp"
    state_error "nao foi possivel ajustar o modo do estado"
    return 1
  }
  if ! mv "$tmp" "$file"; then
    rm -f "$tmp"
    state_error "nao foi possivel substituir o estado atomicamente"
    return 1
  fi
}

provenance_detect() {
  local repo="$1" version="${2:-}" head branch remote merge remote_url remote_node
  local tags count tag

  [ -d "$repo/.git" ] || {
    state_error "checkout Git ausente: $repo"
    return 1
  }
  head="$(git -C "$repo" rev-parse --verify 'HEAD^{commit}' 2>/dev/null)" || {
    state_error "nao foi possivel resolver HEAD em $repo"
    return 1
  }
  [[ "$head" =~ ^[0-9a-f]{40}$ ]] || {
    state_error "HEAD nao e um commit de 40 hex"
    return 1
  }
  PROVENANCE_ORIGIN="$(state_repo_origin "$repo")" || return 1
  PROVENANCE_COMMIT="$head"

  branch="$(git -C "$repo" branch --show-current 2>/dev/null || true)"
  if [ -n "$branch" ]; then
    remote="$(git -C "$repo" config --get "branch.${branch}.remote" 2>/dev/null || true)"
    merge="$(git -C "$repo" config --get "branch.${branch}.merge" 2>/dev/null || true)"
    [ -n "$remote" ] && [ -n "$merge" ] || {
      state_error "branch sem upstream: $branch"
      return 1
    }
    case "$merge" in
      refs/heads/*) ;;
      *) state_error "upstream da branch nao aponta para refs/heads: $merge"; return 1 ;;
    esac
    remote_url="$(git -C "$repo" config --get-all "remote.${remote}.url" 2>/dev/null || true)"
    count="$(state_count_lines "$remote_url")"
    [ "$count" = "1" ] || {
      state_error "remote de upstream ausente ou ambiguo: $remote"
      return 1
    }
    remote_node="$(printf '%s\n' "$remote_url" | sed -n '1p')"
    remote_node="$(state_normalize_origin "$remote_node")" || {
      state_error "upstream nao aponta para a origem oficial"
      return 1
    }
    [ "$remote_node" = "$PROVENANCE_ORIGIN" ] || {
      state_error "origin e upstream apontam para origens diferentes"
      return 1
    }
    PROVENANCE_KIND="branch"
    PROVENANCE_REF="$branch"
  else
    # Tag local e apenas identidade local para o estado. A prova remota e
    # responsabilidade do fluxo de update por tag (F1), nunca deste detector.
    tags="$(git -C "$repo" tag --points-at "$head" 2>/dev/null || true)"
    count="$(state_count_lines "$tags")"
    [ "$count" = "1" ] || {
      state_error "HEAD destacado sem tag exata unica"
      return 1
    }
    tag="$(printf '%s\n' "$tags" | sed -n '1p')"
    [ -n "$version" ] || version="$(tr -d '[:space:]' < "$repo/VERSION" 2>/dev/null || true)"
    [ -n "$version" ] || {
      state_error "versao ausente para checkout destacado"
      return 1
    }
    [ "$tag" = "$version" ] || {
      state_error "tag ${tag} diverge da versao ${version}"
      return 1
    }
    PROVENANCE_KIND="tag"
    PROVENANCE_REF="$tag"
  fi

  [ -n "$version" ] || version="$(tr -d '[:space:]' < "$repo/VERSION" 2>/dev/null || true)"
  [ -n "$version" ] || {
    state_error "versao ausente no checkout"
    return 1
  }
  PROVENANCE_VERSION="$version"
  return 0
}

provenance_validate_state() {
  local file="$1" repo="$2" kind ref commit origin version head branch remote merge remote_url remote_node
  local tags count tag state_repo repo_canonical state_canonical

  state_validate_file "$file" || return 1
  state_repo="$(state_value "$file" CONTABASE_REPO_PATH)"
  [ -n "$state_repo" ] && [ -d "$state_repo" ] || {
    state_error "CONTABASE_REPO_PATH ausente ou inacessivel em $file"
    return 1
  }
  repo_canonical="$(cd "$repo" 2>/dev/null && pwd -P)" || {
    state_error "checkout inacessivel: $repo"
    return 1
  }
  state_canonical="$(cd "$state_repo" 2>/dev/null && pwd -P)" || {
    state_error "checkout do estado inacessivel: $state_repo"
    return 1
  }
  [ "$repo_canonical" = "$state_canonical" ] || {
    state_error "CONTABASE_REPO_PATH diverge do checkout informado"
    return 1
  }
  kind="$(state_value "$file" CONTABASE_INSTALL_REF_KIND)"
  ref="$(state_value "$file" CONTABASE_INSTALL_REF)"
  commit="$(state_value "$file" CONTABASE_INSTALL_COMMIT)"
  origin="$(state_value "$file" CONTABASE_INSTALL_ORIGIN)"

  if [ -z "$kind" ] && [ -z "$ref" ] && [ -z "$commit" ] && [ -z "$origin" ]; then
    return 2
  fi
  [ -n "$kind" ] && [ -n "$ref" ] && [ -n "$commit" ] && [ -n "$origin" ] || {
    state_error "proveniencia parcial em $file"
    return 1
  }
  case "$kind" in
    tag|branch) ;;
    *) state_error "REF_KIND invalido em $file: $kind"; return 1 ;;
  esac
  [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || {
    state_error "COMMIT invalido em $file"
    return 1
  }
  origin="$(state_normalize_origin "$origin")" || {
    state_error "ORIGIN invalida em $file"
    return 1
  }
  head="$(git -C "$repo" rev-parse --verify 'HEAD^{commit}' 2>/dev/null)" || {
    state_error "HEAD indisponivel em $repo"
    return 1
  }
  [ "$head" = "$commit" ] || {
    state_error "HEAD diverge do COMMIT persistido"
    return 1
  }
  [ "$(state_repo_origin "$repo")" = "$origin" ] || {
    state_error "origem atual diverge da origem persistida"
    return 1
  }
  version="$(state_value "$file" CONTABASE_INSTALLED_VERSION)"
  [ -n "$version" ] || {
    state_error "versao instalada ausente em $file"
    return 1
  }
  [ -n "$(tr -d '[:space:]' < "$repo/VERSION" 2>/dev/null || true)" ] || {
    state_error "VERSION ausente no checkout: $repo"
    return 1
  }
  [ "$version" = "$(tr -d '[:space:]' < "$repo/VERSION")" ] || {
    state_error "versao do estado diverge de VERSION no checkout"
    return 1
  }

  if [ "$kind" = "tag" ]; then
    # Coerencia local apenas; F1 deve confirmar a tag no remoto oficial.
    [ "$ref" = "$version" ] || {
      state_error "tag persistida diverge da versao instalada"
      return 1
    }
    tags="$(git -C "$repo" tag --points-at "$head" 2>/dev/null || true)"
    count="$(state_count_lines "$tags")"
    [ "$count" = "1" ] || {
      state_error "HEAD nao possui tag exata unica para validar a tag persistida"
      return 1
    }
    tag="$(printf '%s\n' "$tags" | sed -n '1p')"
    [ "$tag" = "$ref" ] || {
      state_error "tag local nao coincide com a tag persistida"
      return 1
    }
  else
    branch="$(git -C "$repo" branch --show-current 2>/dev/null || true)"
    [ "$branch" = "$ref" ] || {
      state_error "branch atual diverge da branch persistida"
      return 1
    }
    remote="$(git -C "$repo" config --get "branch.${branch}.remote" 2>/dev/null || true)"
    merge="$(git -C "$repo" config --get "branch.${branch}.merge" 2>/dev/null || true)"
    [ -n "$remote" ] && [ -n "$merge" ] || {
      state_error "branch persistida sem upstream"
      return 1
    }
    case "$merge" in
      refs/heads/*) ;;
      *) state_error "upstream invalido para a branch persistida"; return 1 ;;
    esac
    remote_url="$(git -C "$repo" config --get-all "remote.${remote}.url" 2>/dev/null || true)"
    count="$(state_count_lines "$remote_url")"
    [ "$count" = "1" ] || {
      state_error "remote da branch persistida ausente ou ambiguo"
      return 1
    }
    remote_node="$(printf '%s\n' "$remote_url" | sed -n '1p')"
    remote_node="$(state_normalize_origin "$remote_node")" || {
      state_error "upstream da branch persistida nao e oficial"
      return 1
    }
    [ "$remote_node" = "$origin" ] || {
      state_error "upstream da branch persistida diverge da origem"
      return 1
    }
  fi

  return 0
}
