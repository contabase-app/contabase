#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
INPUT="$PROJECT_ROOT/assets/css/input.css"
OUTPUT="$PROJECT_ROOT/assets/css/style.css"

require_node_version() {
    local node_version major
    node_version="$(node --version 2>/dev/null | sed 's/^v//')"
    if [ -z "$node_version" ]; then
        echo "Erro: node não encontrado. O build do CSS (Tailwind 4) exige Node.js 20 ou superior." >&2
        exit 1
    fi

    major="${node_version%%.*}"
    if ! [ "$major" -ge 20 ] 2>/dev/null; then
        echo "Erro: Node.js ${node_version} detectado, mas o build do CSS (Tailwind 4) exige Node.js 20 ou superior." >&2
        echo "O apt do Debian 12 instala Node 18, o que faz o npm pular o binário nativo @tailwindcss/oxide e quebra o build." >&2
        echo "Instale Node.js 20+ (ex.: via NodeSource) antes de continuar." >&2
        exit 1
    fi
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
