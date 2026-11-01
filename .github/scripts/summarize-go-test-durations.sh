#!/usr/bin/env bash
set -euo pipefail

# Resume a duracao por pacote da saida canonica de `go test`.
# Uso: summarize-go-test-durations.sh <log>
# Alerta preventivo: CONTABASE_TEST_DURATION_ALERT_SECONDS (padrao 600).
# Nao falha o gate: o status de `go test` continua sendo a fonte de verdade.

log_file="${1:-}"
if [[ -z "$log_file" ]]; then
  echo "uso: $0 <log-do-go-test>" >&2
  exit 2
fi
if [[ ! -s "$log_file" ]]; then
  echo "== Duracao dos testes: log vazio, nada a resumir =="
  exit 0
fi

alert_seconds="${CONTABASE_TEST_DURATION_ALERT_SECONDS:-600}"
if [[ ! "$alert_seconds" =~ ^[0-9]+$ ]]; then
  echo "CONTABASE_TEST_DURATION_ALERT_SECONDS invalido: $alert_seconds" >&2
  exit 1
fi

summary="$(awk '$1 == "ok" || $1 == "FAIL" {
  duration = $3
  if (duration !~ /^[0-9]+(\.[0-9]+)?s$/) { next }
  sub(/s$/, "", duration)
  printf "%.1f\t%s\t%s\n", duration, $1, $2
}' "$log_file" | sort -nr)"

if [[ -z "$summary" ]]; then
  echo "== Duracao dos testes: nenhum pacote concluido no log =="
  exit 0
fi

package_count="$(printf '%s\n' "$summary" | wc -l | tr -d ' ')"
echo "== Duracao dos testes por pacote ($package_count pacotes, mais lentos primeiro) =="
printf '%s\n' "$summary" | awk -F'\t' '{
  printf "  %8.1fs  %-4s %s\n", $1, $2, $3
}'

slow_packages="$(printf '%s\n' "$summary" | awk -F'\t' -v alert="$alert_seconds" '$1 + 0 >= alert + 0')"
if [[ -n "$slow_packages" ]]; then
  echo "== Alerta de duracao: pacote acima de ${alert_seconds}s (timeout de teste: 1200s) =="
  printf '%s\n' "$slow_packages" | awk -F'\t' '{
    printf "  %8.1fs  %s\n", $1, $3
  }'
fi
