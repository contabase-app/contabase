#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
INPUT="$PROJECT_ROOT/assets/css/input.css"
OUTPUT="$PROJECT_ROOT/assets/css/style.css"

require_node_version() {
    local node_full major
    node_full="$(node --version 2>/dev/null)"
    if [ -z "$node_full" ]; then
        echo "Erro: node não encontrado. O toolchain CSS do ContaBase exige Node.js 22 ou 24 LTS." >&2
        exit 1
    fi

    major="$(echo "$node_full" | sed -E 's/^v([0-9]+).*/\1/')"
    case "$major" in
    22|24)
        ;;
    *)
        cat >&2 <<EOF
Erro: Node.js ${node_full} não é suportado pelo toolchain CSS do ContaBase.

O toolchain CSS do ContaBase foi validado com Node.js 22 e 24 LTS.

Versoes suportadas:
  Node.js 24 LTS — recomendada
  Node.js 22 LTS — compativel

Execute:
  nvm install 24
  nvm use 24
EOF
        exit 1
        ;;
    esac
}

require_node_version

resolve_tailwind() {
    if [ -x "$PROJECT_ROOT/node_modules/.bin/tailwindcss" ]; then
        printf '%s\n' "$PROJECT_ROOT/node_modules/.bin/tailwindcss"
        return 0
    fi

    if command -v tailwindcss >/dev/null 2>&1; then
        command -v tailwindcss
        return 0
    fi

    if command -v npx >/dev/null 2>&1; then
        printf '%s\n' "npx --no-install tailwindcss"
        return 0
    fi

    return 1
}

TAILWIND_CMD="$(resolve_tailwind)" || {
    echo "Erro: não foi possível localizar o Tailwind CLI."
    echo "Execute 'npm ci' para instalar as dependências locais ou instale o Tailwind CLI no sistema."
    exit 1
}

if [ "$1" = "--watch" ]; then
    echo "Modo watch ativo. Compilando CSS automaticamente..."
    if [ "$TAILWIND_CMD" = "npx --no-install tailwindcss" ]; then
        npx --no-install tailwindcss -i "$INPUT" -o "$OUTPUT" --watch
    else
        "$TAILWIND_CMD" -i "$INPUT" -o "$OUTPUT" --watch
    fi
else
    echo "Compilando CSS (minified)..."
    if [ "$TAILWIND_CMD" = "npx --no-install tailwindcss" ]; then
        npx --no-install tailwindcss -i "$INPUT" -o "$OUTPUT" --minify
    else
        "$TAILWIND_CMD" -i "$INPUT" -o "$OUTPUT" --minify
    fi
    echo "CSS compilado: $OUTPUT"
fi
