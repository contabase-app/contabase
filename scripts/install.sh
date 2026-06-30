#!/usr/bin/env bash
set -euo pipefail

# ==============================================================================
# install.sh — Triagem de instalacao/atualizacao do ContaBase
#
# Entrada unica para escolher o metodo certo. Em um checkout completo, chama os
# scripts locais. Quando baixado sozinho, pode buscar somente o helper de release
# correspondente a CONTABASE_VERSION no repositorio publico oficial.
#
# A trilha release baixa somente assets de uma tag informada, valida checksum
# e delega a instalacao ao script especifico. A triagem nao consulta API.
#
# Modo nao interativo (sem menu):
#   CONTABASE_INSTALL_METHOD=docker         ./scripts/install.sh
#   CONTABASE_INSTALL_METHOD=source         ./scripts/install.sh
#   CONTABASE_INSTALL_METHOD=update-docker   ./scripts/install.sh
#   CONTABASE_INSTALL_METHOD=update-source   ./scripts/install.sh
#   CONTABASE_INSTALL_METHOD=release         CONTABASE_VERSION=vX.Y.Z ./scripts/install.sh
#   CONTABASE_INSTALL_METHOD=update-release  CONTABASE_VERSION=vX.Y.Z ./scripts/install.sh
# ==============================================================================

SCRIPT_SOURCE="${BASH_SOURCE[0]:-}"
if [ -n "$SCRIPT_SOURCE" ] && [ -f "$SCRIPT_SOURCE" ]; then
  SCRIPT_DIR="$(cd "$(dirname "$SCRIPT_SOURCE")" && pwd)"
  REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
  INSTALL_DOCKER="${SCRIPT_DIR}/install-contabase-docker.sh"
  INSTALL_RELEASE="${SCRIPT_DIR}/install-contabase-release.sh"
  INSTALL_SOURCE="${SCRIPT_DIR}/install-contabase-source.sh"
  UPDATE_DOCKER="${SCRIPT_DIR}/update-contabase-docker.sh"
  UPDATE_SOURCE="${SCRIPT_DIR}/update-contabase-source.sh"
  UPDATE_RELEASE="${SCRIPT_DIR}/update-contabase-release.sh"
else
  SCRIPT_DIR=""
  REPO_ROOT="$(pwd)"
  INSTALL_DOCKER=""
  INSTALL_RELEASE=""
  INSTALL_SOURCE=""
  UPDATE_DOCKER=""
  UPDATE_SOURCE=""
  UPDATE_RELEASE=""
fi
PUBLIC_RAW_BASE="https://raw.githubusercontent.com/contabase-app/contabase"

say() {
  printf '%s\n' "$*"
}

blank() {
  printf '\n'
}

public_tag_example() {
  printf '%s' 'vMAJOR.MINOR.PATCH[-beta.N]'
}

current_public_version() {
  local version_file current_version

  version_file="${REPO_ROOT}/VERSION"
  [ -f "$version_file" ] || return 1

  current_version="$(tr -d '[:space:]' < "$version_file")"
  case "$current_version" in
    ""|*-internal*|latest|main|master|dev|develop|stable)
      return 1
      ;;
  esac

  if [[ "$current_version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?(\+[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$ ]]; then
    printf '%s' "$current_version"
    return 0
  fi

  return 1
}

require_script() {
  script_path="$1"
  if [ -z "$script_path" ] || [ ! -f "$script_path" ]; then
    say "Erro: script esperado nao encontrado: ${script_path}"
    say "Docker/source exigem um checkout completo do repositorio ContaBase."
    exit 1
  fi
}

# Chama o script alvo via bash, sem eval e sem montar comando por string.
# Nao mascara o exit code: se o script chamado falhar, o set -e aborta a triagem.
run_script() {
  script_path="$1"
  require_script "$script_path"
  bash "$script_path"
}

resolve_release_version() {
  local default_version example_version user_input

  if [ -n "${CONTABASE_VERSION:-}" ]; then
    return 0
  fi

  example_version="$(public_tag_example)"
  if [ ! -t 0 ]; then
    say "CONTABASE_VERSION nao definido e terminal nao interativo."
    say "Defina a versao da release. Exemplo:"
    say "  export CONTABASE_VERSION=${example_version}"
    say "  ou:  CONTABASE_VERSION=${example_version} bash /tmp/contabase-install.sh"
    return 1
  fi

  default_version="$(current_public_version || true)"
  blank

  if [ -n "$default_version" ]; then
    say "CONTABASE_VERSION nao definido. Informe a versao da release publica."
    read -r -p "Versao da release [${default_version}]: " user_input
    CONTABASE_VERSION="${user_input:-$default_version}"
  else
    say "CONTABASE_VERSION nao definido. Informe a versao da release publica."
    read -r -p "Versao da release (ex.: ${example_version}): " user_input
    CONTABASE_VERSION="$user_input"
  fi

  if [ -z "$CONTABASE_VERSION" ]; then
    say "Erro: informe uma tag publica SemVer."
    return 1
  fi

  export CONTABASE_VERSION
  say "Usando versao: ${CONTABASE_VERSION}"
  blank
  return 0
}

validate_public_release_version() {
  local release_version
  release_version="${CONTABASE_VERSION:-}"
  if [ -z "$release_version" ]; then
    say "Erro: CONTABASE_VERSION e obrigatorio para baixar o helper de release."
    say "Exemplo: CONTABASE_VERSION=$(public_tag_example)"
    return 1
  fi
  case "$release_version" in
    *-internal*)
      say "Erro: versoes com -internal sao privadas e nao podem usar o bootstrap publico."
      return 1
      ;;
  esac
  if [[ ! "$release_version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?(\+[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$ ]]; then
    say "Erro: CONTABASE_VERSION nao e uma tag SemVer publica valida: ${release_version}"
    return 1
  fi
}

run_release_installer() {
  local helper_dir helper_path helper_url helper_status

  if [ -n "$INSTALL_RELEASE" ] && [ -f "$INSTALL_RELEASE" ]; then
    say "Usando helper local: ${INSTALL_RELEASE}"
    bash "$INSTALL_RELEASE"
    return
  fi

  resolve_release_version || return 1
  validate_public_release_version
  if ! command -v curl >/dev/null 2>&1 || ! command -v mktemp >/dev/null 2>&1; then
    say "Erro: curl e mktemp sao obrigatorios para baixar o helper de release."
    return 1
  fi

  helper_dir="$(mktemp -d "${TMPDIR:-/tmp}/contabase-bootstrap.XXXXXX")"
  helper_path="${helper_dir}/install-contabase-release.sh"
  helper_url="${PUBLIC_RAW_BASE}/${CONTABASE_VERSION}/scripts/install-contabase-release.sh"

  say "Helper local ausente. Baixando o helper da mesma tag publica:"
  say "$helper_url"
  if ! curl --fail --location --silent --show-error \
    --proto '=https' --tlsv1.2 \
    "$helper_url" -o "$helper_path"; then
    rm -rf "$helper_dir"
    say "Erro: nao foi possivel baixar o helper da tag ${CONTABASE_VERSION}."
    return 1
  fi

  if bash "$helper_path"; then
    helper_status=0
  else
    helper_status=$?
  fi
  rm -rf "$helper_dir"
  return "$helper_status"
}

run_release_updater() {
  local helper_dir helper_path helper_url helper_status

  if [ -n "$UPDATE_RELEASE" ] && [ -f "$UPDATE_RELEASE" ]; then
    say "Usando helper local: ${UPDATE_RELEASE}"
    bash "$UPDATE_RELEASE"
    return
  fi

  resolve_release_version || return 1
  validate_public_release_version
  if ! command -v curl >/dev/null 2>&1 || ! command -v mktemp >/dev/null 2>&1; then
    say "Erro: curl e mktemp sao obrigatorios para baixar o helper de update release."
    return 1
  fi

  helper_dir="$(mktemp -d "${TMPDIR:-/tmp}/contabase-bootstrap.XXXXXX")"
  helper_path="${helper_dir}/update-contabase-release.sh"
  helper_url="${PUBLIC_RAW_BASE}/${CONTABASE_VERSION}/scripts/update-contabase-release.sh"

  say "Helper local ausente. Baixando o helper da mesma tag publica:"
  say "$helper_url"
  if ! curl --fail --location --silent --show-error \
    --proto '=https' --tlsv1.2 \
    "$helper_url" -o "$helper_path"; then
    rm -rf "$helper_dir"
    say "Erro: nao foi possivel baixar o helper de update da tag ${CONTABASE_VERSION}."
    return 1
  fi

  if bash "$helper_path"; then
    helper_status=0
  else
    helper_status=$?
  fi
  rm -rf "$helper_dir"
  return "$helper_status"
}

dispatch_method() {
  method="$1"
  case "$method" in
    docker)
      say "Método selecionado: Instalar via Docker Compose"
      run_script "$INSTALL_DOCKER"
      ;;
    source)
      say "Método selecionado: Instalar via código-fonte/systemd"
      say "Este modo compila o ContaBase localmente."
      say "Indicado para desenvolvimento, customização ou repo privado."
      run_script "$INSTALL_SOURCE"
      ;;
    update-docker)
      say "Método selecionado: Atualizar via Docker Compose"
      run_script "$UPDATE_DOCKER"
      ;;
    update-source)
      say "Método selecionado: Atualizar via código-fonte/systemd"
      run_script "$UPDATE_SOURCE"
      ;;
    release)
      say "Método selecionado: Instalar via binário da Release"
      say "Este modo baixa o binário publicado no GitHub Release."
      say "Indicado para VPS/LXC sem Docker, sem Go e sem Node."
      run_release_installer
      ;;
    update-release)
      say "Método selecionado: Atualizar via binário da Release"
      run_release_updater
      ;;
    *)
      say "Erro: metodo invalido em CONTABASE_INSTALL_METHOD: ${method}"
      say "Valores aceitos: docker, source, release, update-docker, update-source, update-release."
      exit 1
      ;;
  esac
}

show_menu() {
  say "======================================================================"
  say "ContaBase Installer"
  say "======================================================================"
  blank
  say "1) Instalar via Docker Compose"
  say "   Recomendado para a maioria dos usuários. Isolado e fácil de atualizar."
  blank
  say "2) Instalar via binário da Release"
  say "   Para VPS/LXC sem Docker, sem Go e sem Node."
  say "   Baixa o binário publicado no GitHub Release."
  blank
  say "3) Instalar via código-fonte/systemd"
  say "   Avançado. Para desenvolvimento, customização ou repo privado."
  say "   Exige Go e Node."
  blank
  say "4) Avançado/manual"
  say "   Mostra os scripts específicos para executar manualmente."
  blank
  say "5) Atualizar via Docker Compose"
  say "   Atualiza instalação criada pela opção 1."
  blank
  say "6) Atualizar via binário da Release"
  say "   Atualiza instalação criada pela opção 2."
  blank
  say "7) Atualizar via código-fonte/systemd"
  say "   Atualiza instalação criada pela opção 3."
  blank
  say "8) Sair"
  blank
}

interactive_menu() {
  while true; do
    show_menu
    read -r -p "Opcao (1-8): " choice
    blank
    case "$choice" in
      1)
        dispatch_method docker
        return 0
        ;;
      2)
        dispatch_method release
        return 0
        ;;
      3)
        dispatch_method source
        return 0
        ;;
      4)
        say "Scripts manuais disponíveis:"
        say "  ./scripts/install-contabase-docker.sh"
        say "  ./scripts/install-contabase-release.sh"
        say "  ./scripts/install-contabase-source.sh"
        say "Modos de acesso:"
        say "  Docker local nesta maquina: local-docker"
        say "  Binario/source local nesta maquina: local"
        say "  Rede local por IP privado: lan"
        say "  Dominio HTTPS/proxy: proxy"
        return 0
        ;;
      5)
        dispatch_method update-docker
        return 0
        ;;
      6)
        dispatch_method update-release
        return 0
        ;;
      7)
        dispatch_method update-source
        return 0
        ;;
      8)
        say "Saindo."
        return 0
        ;;
      *)
        say "Opcao invalida. Escolha um numero de 1 a 8."
        blank
        ;;
    esac
  done
}

main() {
  cd "$REPO_ROOT"

  if [ "${1:-}" = "--help" ] || [ "${1:-}" = "-h" ]; then
    say "Uso: ./scripts/install.sh"
    say "Triagem local para instalar ou atualizar o ContaBase."
    blank
    say "Modo nao interativo via variavel de ambiente:"
    say "  CONTABASE_INSTALL_METHOD=docker          ./scripts/install.sh"
    say "  CONTABASE_INSTALL_METHOD=source          ./scripts/install.sh"
    say "  CONTABASE_INSTALL_METHOD=update-docker   ./scripts/install.sh"
    say "  CONTABASE_INSTALL_METHOD=update-source   ./scripts/install.sh"
    say "  CONTABASE_INSTALL_METHOD=release         CONTABASE_VERSION=vX.Y.Z ./scripts/install.sh"
    say "  CONTABASE_INSTALL_METHOD=update-release  CONTABASE_VERSION=vX.Y.Z ./scripts/install.sh"
    exit 0
  fi

  if [ -n "${CONTABASE_INSTALL_METHOD:-}" ]; then
    dispatch_method "${CONTABASE_INSTALL_METHOD}"
    exit 0
  fi

  if [ ! -t 0 ]; then
    say "Erro: terminal nao interativo e CONTABASE_INSTALL_METHOD nao definido."
    say "Defina CONTABASE_INSTALL_METHOD (docker, release, source, update-docker, update-source, update-release) ou rode em um terminal interativo."
    exit 1
  fi

  interactive_menu
}

main "$@"
