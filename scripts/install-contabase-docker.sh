#!/bin/bash
set -euo pipefail

# ==============================================================================
# ContaBase - Instalador Guiado Docker
# ==============================================================================

INTERACTIVE=true
CHECK_ONLY=false
ACCESS_URL="http://localhost:8080"

say() {
  printf '%s\n' "$*"
}

blank() {
  printf '\n'
}

section() {
  blank
  say "$1"
}

random_base64() {
  bytes="$1"
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 "$bytes" | tr -d '\n'
  else
    head -c "$bytes" /dev/urandom | base64 | tr -d '\n'
  fi
}

random_hex() {
  bytes="$1"
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex "$bytes" | tr -d '\n'
  else
    od -An -N"$bytes" -tx1 /dev/urandom | tr -d ' \n'
  fi
}

set_env_var() {
  key="$1"
  value="$2"
  file="${3:-.env.docker}"
  tmp="$(mktemp "${TMPDIR:-/tmp}/contabase-env.XXXXXX")"

  if grep -q "^${key}=" "$file"; then
    awk -v key="$key" -v value="$value" '
      BEGIN { prefix = key "=" }
      index($0, prefix) == 1 { $0 = prefix value }
      { print }
    ' "$file" > "$tmp"
    cat "$tmp" > "$file"
  else
    cat "$file" > "$tmp"
    printf '%s=%s\n' "$key" "$value" >> "$tmp"
    cat "$tmp" > "$file"
  fi

  rm -f "$tmp"
}

env_var_has_value() {
  key="$1"
  file="${2:-.env.docker}"
  awk -v key="$key" '
    BEGIN { found = 0 }
    index($0, key "=") == 1 {
      value = substr($0, length(key) + 2)
      gsub(/[[:space:]]/, "", value)
      if (value != "") {
        found = 1
      }
    }
    END { exit found ? 0 : 1 }
  ' "$file"
}

# Read raw value of a key from env file. Silent; value via stdout.
env_load_value() {
  key="$1"
  file="${2:-.env.docker}"
  [ -f "$file" ] || { printf ''; return 0; }
  awk -v k="$key" 'index($0, k"=") == 1 { v = substr($0, length(k) + 2); gsub(/[[:space:]]/, "", v); print v; exit }' "$file"
}

# Detect known placeholder patterns.
env_is_placeholder() {
  local val="$1"
  [ -z "$val" ] && return 0
  case "$val" in
    __PREENCHA_*|__GERAR_COM_*|CHANGE_ME*|change_me*|REPLACE_ME*|replace_me*|YOUR_*|your_*|PLACEHOLDER*|placeholder*) return 0 ;;
    *) return 1 ;;
  esac
}

# Check if key has a real (non-empty, non-placeholder) value.
env_key_is_set() {
  local key="$1"
  local file="${2:-.env.docker}"
  local val
  [ -f "$file" ] || return 1
  val="$(env_load_value "$key" "$file")"
  [ -n "$val" ] || return 1
  env_is_placeholder "$val" && return 1
  return 0
}

# Set a key only if it does not have a real value. Replaces placeholder/empty lines.
set_env_var_safe() {
  local key="$1"
  local value="$2"
  local file="${3:-.env.docker}"

  if env_key_is_set "$key" "$file"; then
    return 0
  fi
  set_env_var "$key" "$value" "$file"
}

# Create timestamped backup of the env file.
env_backup() {
  local file="${1:-.env.docker}"
  [ -f "$file" ] || return 0
  local backup="${file}.$(date +%Y%m%d-%H%M%S).bak"
  if cp "$file" "$backup"; then
    say "Backup de ${file} criado em: ${backup}"
  fi
}

# Validate mandatory secrets exist. Returns 0 if all ok, 1 if any missing.
env_validate_secrets() {
  local file="${1:-.env.docker}"
  local missing=()
  local key

  for key in AUTH_ENCRYPTION_KEY SECURITY_MASTER_KEY CONTABASE_SETUP_TOKEN; do
    env_key_is_set "$key" "$file" || missing+=("$key")
  done

  [ "${#missing[@]}" -eq 0 ] && return 0

  say ""
  say "====================================================================="
  say "ERRO: Secret(s) obrigatorio(s) ausente(s) ou com placeholder em ${file}:"
  for key in "${missing[@]}"; do
    say "  - ${key}"
  done
  say "====================================================================="
  return 1
}

ensure_secret() {
  local key="$1"
  local generator="$2"
  local file="${3:-.env.docker}"
  local is_new="${4:-false}"
  local value

  # Key already has a real value — preserve
  if env_key_is_set "$key" "$file"; then
    say "  [OK] ${key} ja configurado (valor preservado)."
    return 0
  fi

  # Key is missing, empty, or placeholder
  say "  [ALERTA] ${key} ausente, vazio ou com placeholder."

  # New installation — auto-generate always (headless or interactive)
  if [ "$is_new" = "true" ]; then
    : # fall through to generation below
  # Existing installation, headless — fail
  elif [ "$INTERACTIVE" = false ]; then
    say ""
    say "====================================================================="
    say "ERRO (modo headless): ${key} ausente em instalacao existente."
    say "Nao e possivel gerar automaticamente."
    say "Edite ${file} manualmente e execute novamente:"
    say "  - ${key}=<valor seguro>"
    say "====================================================================="
    return 1
  else
    # Interactive mode — ask before generating
    read -r -p "Gerar novo valor para ${key}? (s/N) " answer
    case "$answer" in
      s|S|sim|SIM) ;;
      *) say "  [SKIP] ${key} nao foi gerado. Defina manualmente em ${file}."; return 1 ;;
    esac
  fi

  # Generate secret
  case "$generator" in
    setup-token) value="$(random_base64 48)" ;;
    auth-key) value="$(random_base64 32)" ;;
    master-key) value="$(random_hex 16)" ;;
    *)
      say "Erro: gerador desconhecido para $key."
      exit 1
      ;;
  esac
  set_env_var "$key" "$value" "$file"
  say "  [NEW] ${key} gerado (valor nao exibido)."
}

normalize_domain() {
  domain="$1"
  domain="${domain#http://}"
  domain="${domain#https://}"
  domain="${domain%%/*}"
  domain="${domain%%:*}"
  printf '%s' "$domain"
}

show_override_warning() {
  say "Revise docker-compose.override.yml antes de expor publicamente."
  say "- Use-o para portas, redes Docker, labels de proxy e IP fixo."
  say "- Nao edite docker-compose.yml."
  say "- Se usar proxy/tunnel no mesmo host, prefira 127.0.0.1:8080:8080 ou rede Docker compartilhada."
  say "- Exposicao publica direta sem HTTPS/proxy/tunnel nao e recomendada e pode ser bloqueada."
}

create_override_if_requested() {
  if [ -f "docker-compose.override.yml" ]; then
    say "OK: docker-compose.override.yml ja existe e foi preservado."
    return
  fi

  if [ ! -f "docker-compose.override.example.yml" ]; then
    return
  fi

  if [ "$INTERACTIVE" = true ]; then
    read -r -p "Deseja criar docker-compose.override.yml para customizacoes? (s/N) " reply
    if printf '%s' "$reply" | grep -qE '^[Ss]$'; then
      cp docker-compose.override.example.yml docker-compose.override.yml
      say "OK: docker-compose.override.yml criado a partir do exemplo."
      show_override_warning
    fi
  else
    say "Pulando criacao do override (modo automatico)."
  fi
}

ensure_override_for_profile() {
  if [ ! -f "docker-compose.override.yml" ] && [ -f "docker-compose.override.example.yml" ]; then
    cp docker-compose.override.example.yml docker-compose.override.yml
    say "OK: docker-compose.override.yml criado para customizacoes locais."
    show_override_warning
  fi
}

as_root() {
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
  elif command -v sudo >/dev/null 2>&1; then
    sudo "$@"
  else
    return 1
  fi
}

is_debian_like() {
  [ -f /etc/os-release ] || return 1
  # shellcheck disable=SC1091
  . /etc/os-release
  case "${ID:-} ${ID_LIKE:-}" in
    *debian*|*ubuntu*) return 0 ;;
    *) return 1 ;;
  esac
}

ca_certificates_installed() {
  if command -v dpkg-query >/dev/null 2>&1; then
    dpkg-query -W -f='${Status}' ca-certificates 2>/dev/null | grep -q "install ok installed"
    return
  fi
  [ -d /etc/ssl/certs ]
}

collect_missing_dependencies() {
  local missing=()

  command -v curl >/dev/null 2>&1 || missing+=("curl")
  command -v openssl >/dev/null 2>&1 || missing+=("openssl")
  command -v python3 >/dev/null 2>&1 || missing+=("python3")
  ca_certificates_installed || missing+=("ca-certificates")
  command -v docker >/dev/null 2>&1 || missing+=("docker")
  if command -v docker >/dev/null 2>&1 && ! docker compose version >/dev/null 2>&1; then
    missing+=("docker compose")
  fi

  printf '%s\n' "${missing[@]}"
}

install_debian_dependencies() {
  say "Instalando dependencias via apt..."
  as_root apt-get update
  if ! as_root env DEBIAN_FRONTEND=noninteractive apt-get install -y \
    ca-certificates curl openssl python3 docker.io docker-compose-plugin; then
    say "Pacote docker-compose-plugin indisponivel; tentando fallback docker-compose."
    as_root env DEBIAN_FRONTEND=noninteractive apt-get install -y \
      ca-certificates curl openssl python3 docker.io docker-compose
  fi

  if command -v systemctl >/dev/null 2>&1; then
    as_root systemctl enable --now docker >/dev/null 2>&1 || true
  fi
  as_root service docker start >/dev/null 2>&1 || true
}

print_dependency_help() {
  local missing_text="$1"
  say "Dependencias ausentes:"
  printf '%s\n' "$missing_text" | sed '/^$/d;s/^/  - /'
  say ""
  say "Instale Docker Engine, Docker Compose v2, curl, openssl, ca-certificates e python3."
  say "Em Debian/Ubuntu, execute novamente em modo interativo para autorizar a instalacao,"
  say "ou use CONTABASE_INSTALL_DEPS=1 em modo headless se quiser instalar via apt."
}

ensure_dependencies() {
  local missing_text answer

  missing_text="$(collect_missing_dependencies | sed '/^$/d')"
  [ -n "$missing_text" ] || return 0

  if [ "$CHECK_ONLY" = true ]; then
    print_dependency_help "$missing_text"
    exit 1
  fi

  if ! is_debian_like; then
    print_dependency_help "$missing_text"
    say "Sistema nao suportado para instalacao automatica de dependencias."
    exit 1
  fi

  if [ "$INTERACTIVE" = true ] && [ -t 0 ]; then
    print_dependency_help "$missing_text"
    read -r -p "Docker/Compose nao estao instalados. Deseja instalar as dependencias agora? [s/N]: " answer
    case "$answer" in
      s|S|sim|SIM|Sim) ;;
      *) say "Instalacao cancelada. Instale as dependencias e rode novamente."; exit 1 ;;
    esac
  elif [ "${CONTABASE_INSTALL_DEPS:-0}" != "1" ]; then
    print_dependency_help "$missing_text"
    say "Modo headless: defina CONTABASE_INSTALL_DEPS=1 para autorizar instalacao via apt."
    exit 1
  else
    say "CONTABASE_INSTALL_DEPS=1 definido; instalando dependencias ausentes via apt."
  fi

  install_debian_dependencies

  missing_text="$(collect_missing_dependencies | sed '/^$/d')"
  if [ -n "$missing_text" ]; then
    print_dependency_help "$missing_text"
    say "Dependencias ainda ausentes apos tentativa de instalacao."
    exit 1
  fi
}

for arg in "$@"; do
  case "$arg" in
    --help)
      say "ContaBase Instalador Guiado"
      blank
      say "Uso: ./scripts/install-contabase-docker.sh [opcoes]"
      blank
      say "Opcoes:"
      say "  --help       Exibe esta mensagem"
      say "  --yes        Aceita todos os padroes automaticamente (instalacao local)"
      say "  --check      Apenas verifica os pre-requisitos sem rodar o container"
      say "  --dry-run    Alias para --check"
      exit 0
      ;;
    --yes)
      INTERACTIVE=false
      ;;
    --check|--dry-run)
      CHECK_ONLY=true
      ;;
  esac
done

[ "${CONTABASE_ASSUME_YES:-0}" = "1" ] && INTERACTIVE=false

say "======================================================================"
say "Iniciando Instalador do ContaBase"
say "======================================================================"

section "[1/5] Verificando pre-requisitos..."

if [ ! -f "docker-compose.yml" ]; then
  say "Erro: arquivo docker-compose.yml nao encontrado."
  say "Certifique-se de estar na raiz do repositorio ContaBase."
  exit 1
fi

ensure_dependencies

say "OK: pre-requisitos atendidos."

if [ "$CHECK_ONLY" = true ]; then
  blank
  say "Modo --check ativado. Instalacao simulada com sucesso."
  say "Para instalar de verdade, rode o script sem a flag --check."
  exit 0
fi

section "[2/5] Configurando orquestracao Docker..."
say "Aviso: docker-compose.yml e oficial e nao deve ser editado manualmente."
say "Customizacoes locais devem ficar em docker-compose.override.yml."
create_override_if_requested

section "[3/5] Configurando ambiente local..."

env_is_new=false
if [ ! -f ".env.docker" ]; then
  if [ -f ".env.docker.example" ]; then
    cp .env.docker.example .env.docker
    say "OK: .env.docker criado a partir do exemplo."
    env_is_new=true
  else
    say "Erro: .env.docker.example nao encontrado."
    exit 1
  fi
else
  say "OK: .env.docker ja existe."
  say "Fazendo backup antes de qualquer alteracao..."
  env_backup ".env.docker"

  # Validate secrets in existing env (headless: fail; interactive: offer to fix)
  if [ "$INTERACTIVE" = false ]; then
    if ! env_validate_secrets ".env.docker"; then
      say ""
      say "Corrija os secrets em .env.docker e execute novamente."
      exit 1
    fi
    say "Secrets validados com sucesso (modo headless)."
  fi
fi

say ""
say "Verificando secrets obrigatorios..."
if ! ensure_secret "CONTABASE_SETUP_TOKEN" "setup-token" ".env.docker" "$env_is_new"; then
  [ "$INTERACTIVE" = false ] && exit 1
fi
if ! ensure_secret "AUTH_ENCRYPTION_KEY" "auth-key" ".env.docker" "$env_is_new"; then
  [ "$INTERACTIVE" = false ] && exit 1
fi
if ! ensure_secret "SECURITY_MASTER_KEY" "master-key" ".env.docker" "$env_is_new"; then
  [ "$INTERACTIVE" = false ] && exit 1
fi

section "[4/5] Configurando volume de persistencia local..."

if [ ! -d "data" ]; then
  mkdir -p data
  say "OK: pasta data/ criada."
fi

say "Ajustando permissoes para o usuario non-root do container (UID 1000)..."

if chown -R 1000:1000 data 2>/dev/null; then
  chmod -R u+rwX data
  say "OK: permissoes ajustadas com sucesso."
else
  if command -v sudo >/dev/null 2>&1; then
    say "E necessario privilegio administrativo para configurar data/ com UID 1000."
    if sudo chown -R 1000:1000 data; then
      sudo chmod -R u+rwX data
      say "OK: permissoes ajustadas via sudo com sucesso."
    else
      say "Erro: nao foi possivel ajustar as permissoes via sudo."
      say "Execute manualmente: sudo chown -R 1000:1000 data && sudo chmod -R u+rwX data"
      exit 1
    fi
  else
    say "Erro: nao foi possivel ajustar permissoes e sudo nao foi encontrado."
    say "A aplicacao nao conseguira escrever no banco SQLite."
    say "Execute manualmente como root: chown -R 1000:1000 data && chmod -R u+rwX data"
    exit 1
  fi
fi

if [ "$INTERACTIVE" = true ]; then
  section "Selecione o perfil de instalacao:"
  say "1) Local / LAN privada"
  say "2) Domínio com HTTPS via proxy/tunnel/CDN"
  say "3) Avançado / manual"
  read -r -p "Opcao (1/2/3): " INSTALL_TYPE
  blank

  case "$INSTALL_TYPE" in
    1)
      say "Modo Local / LAN privada selecionado."
      say "Aviso: exposicao publica direta sem HTTPS/proxy/tunnel nao e recomendada e pode ser bloqueada."
      ACCESS_URL="http://localhost:8080"
      ;;
    2)
      say "Modo Domínio com HTTPS via proxy/tunnel/CDN selecionado."
      say "Este perfil cobre Nginx Proxy Manager, Caddy, Traefik, Nginx,"
      say "Cloudflare Tunnel, Cloudflare Proxy/CDN, BunnyCDN/outro CDN,"
      say "e proxies em servidor local ou VPS."
      say "O instalador nao configura provedores externos automaticamente."
      read -r -p "Dominio HTTPS (ex: app.seudominio.com): " PUBLIC_DOMAIN_RAW
      PUBLIC_DOMAIN="$(normalize_domain "$PUBLIC_DOMAIN_RAW")"

      if [ -n "$PUBLIC_DOMAIN" ]; then
        set_env_var_safe "APP_BASE_URL" "https://$PUBLIC_DOMAIN"
        set_env_var_safe "ALLOWED_HOSTS" "$PUBLIC_DOMAIN"
        ACCESS_URL="https://$PUBLIC_DOMAIN"
        say "OK: APP_BASE_URL e ALLOWED_HOSTS definidos em .env.docker (valores existentes preservados)."
      else
        say "Aviso: dominio vazio. Configure APP_BASE_URL e ALLOWED_HOSTS manualmente antes de expor."
      fi

      ensure_override_for_profile
      blank
      say "Configure o proxy/tunnel/origin local para enviar estes headers ao ContaBase:"
      say "- Host"
      say "- X-Forwarded-Host"
      say "- X-Forwarded-Proto"
      say "- X-Real-IP"
      say "- X-Forwarded-For"
      blank
      say "TRUSTED_PROXIES deve ser o IP/CIDR que conecta diretamente no ContaBase."
      say "Geralmente e a rede/IP do proxy local, tunnel ou container que encaminha ao app."
      say "Nao e necessariamente o IP publico do usuario."
      say "Nao e necessariamente todos os IPs da Cloudflare/Bunny se houver proxy local entre eles e o ContaBase."
      say "Sugestoes comuns, confirme de acordo com a sua topologia real:"
      say "- 172.16.0.0/12 para redes Docker privadas comuns"
      say "- 172.17.0.0/16 para a bridge Docker padrao"
      say "- IP/CIDR especifico do proxy/tunnel/container, se voce souber"
      read -r -p "TRUSTED_PROXIES (pode deixar vazio para configurar manualmente): " TRUSTED_PROXIES_VALUE
      if [ -n "$TRUSTED_PROXIES_VALUE" ]; then
        set_env_var_safe "TRUSTED_PROXIES" "$TRUSTED_PROXIES_VALUE"
        say "OK: TRUSTED_PROXIES definido em .env.docker (valor existente preservado)."
      else
        say "Aviso forte: TRUSTED_PROXIES vazio/incorreto pode fazer o app bloquear acesso remoto por seguranca."
      fi
      ;;
    3)
      say "Modo Avancado / manual selecionado."
      ensure_override_for_profile
      say "Revise .env.docker e docker-compose.override.yml antes de expor a aplicacao."
      ;;
    *)
      say "Opcao invalida. Prosseguindo com defaults locais."
      ACCESS_URL="http://localhost:8080"
      ;;
  esac
fi

section "[5/5] Subindo aplicacao via Docker..."

if [ -f ".env.docker" ]; then
  say "Executando docker compose config para validar arquivos..."
  docker compose config >/dev/null
  say "OK: configuracoes do Compose validadas."
else
  say "Aviso: docker compose config pulado porque .env.docker nao existe."
fi

docker compose up -d --build
blank
say "Containers ativos:"
docker compose ps

blank
say "Ultimos logs:"
docker compose logs --tail=80 contabase || true

blank
say "======================================================================"
say "CONCLUIDO: o ContaBase esta rodando."
say "======================================================================"
blank
say "Como acessar e continuar:"
say "1. Acesse: $ACCESS_URL"
say "2. O sistema pedira o TOKEN LOCAL DE SETUP."
say "3. Copie o valor de CONTABASE_SETUP_TOKEN diretamente do arquivo .env.docker local."
blank
say "IMPORTANTE POS-SETUP:"
say "Apos criar sua primeira conta de administrador, remova ou comente CONTABASE_SETUP_TOKEN no .env.docker."
say "Depois reinicie sem o token: docker compose up -d --build"
say "O alerta no painel administrativo deve sumir apos o container reiniciar sem esse token."
blank
say "Atualizacoes futuras:"
say "./scripts/update-contabase-docker.sh"
blank
