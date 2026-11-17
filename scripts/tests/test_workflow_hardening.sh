#!/usr/bin/env bash
# R1.3-D5-SEC-R3-WF-RED: contratos de endurecimento dos workflows de release.
#
# TEST-ONLY. Le os workflows e o helper de estado, executa apenas fixtures
# sinteticas locais e nunca chama GitHub, GHCR, actions, rede ou credenciais.
#
# Estrutura YAML e validada com parser real (Psych/Ruby) em modo fail-closed.
# Psych.safe_load e o unico caminho de desserializacao: nao existe fallback
# para Psych.load, classes arbitrarias ou simbolos. Erro de parsing registra a
# falha e encerra com status nao zero; nunca declara PASS.
# Nenhuma checagem de estrutura usa grep/regex sobre o YAML. grep aparece
# apenas sobre shell produtivo (release-validation.sh), onde a estrutura nao
# e YAML.
#
# Fixtures: CONTABASE_WF_ARTIFACTS, CONTABASE_WF_PREFLIGHT,
# CONTABASE_WF_VALIDATION e CONTABASE_WF_STATE_HELPER permitem apontar a suite
# para copias sinteticas fora do repositorio (probes negativas). Sem override,
# os alvos sao os arquivos reais do repositorio.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

ART_WF="${CONTABASE_WF_ARTIFACTS:-.github/workflows/release-artifacts.yml}"
PRE_WF="${CONTABASE_WF_PREFLIGHT:-.github/workflows/release-preflight.yml}"
VALIDATION="${CONTABASE_WF_VALIDATION:-.github/scripts/release-validation.sh}"
STATE_HELPER="${CONTABASE_WF_STATE_HELPER:-.github/scripts/release-state.sh}"

[[ "$VALIDATION" = /* ]] || VALIDATION="$ROOT/$VALIDATION"
[[ "$STATE_HELPER" = /* ]] || STATE_HELPER="$ROOT/$STATE_HELPER"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/contabase-wf-hardening.XXXXXX")"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

PASS=0
FAIL=0
SKIP=0

pass() { printf 'PASS: %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf 'FAIL: %s\n' "$1"; FAIL=$((FAIL + 1)); }
skip() { printf 'SKIP: %s\n' "$1"; SKIP=$((SKIP + 1)); }

printf '%s\n' '== R3-WF-RED: contratos de seguranca dos workflows de release =='

# ---------------------------------------------------------------------------
# Parser YAML: Psych (Ruby stdlib). Sem instalacao automatica de dependencias.
# ---------------------------------------------------------------------------
PARSER=0
if command -v ruby >/dev/null 2>&1 && ruby -ryaml -e '' >/dev/null 2>&1; then
  PARSER=1
else
  skip 'W00 parser YAML indisponivel: contratos estruturais nao foram avaliados'
  printf '%s\n' '      limitacao: suite PARCIAL, nao aprovada integralmente' >&2
fi

PARSER_SCRIPT="$WORK/parse_workflow.rb"
cat > "$PARSER_SCRIPT" <<'RUBY'
#!/usr/bin/env ruby
# Parser YAML fail-closed dos workflows de release.
# Psych.safe_load e o unico caminho de desserializacao: sem Psych.load, sem
# classes/simbolos nao confiaveis e sem fallback apos erro.
require 'yaml'
require 'json'

path = ARGV.fetch(0)
raw = File.read(path)

safe_options = { permitted_classes: [], permitted_symbols: [], aliases: true }
doc =
  begin
    if Psych.method(:safe_load).parameters.any? { |kind, _name| kind == :key }
      Psych.safe_load(raw, **safe_options)
    else
      Psych.safe_load(raw, [], [], true)
    end
  rescue StandardError => error
    warn "yaml-parse-error: #{error.class}: #{error.message}"
    exit 4
  end

unless doc.is_a?(Hash)
  warn 'yaml-parse-error: workflow is not a mapping'
  exit 5
end

notes = []
if doc.key?(true) && !doc.key?('on')
  doc['on'] = doc.delete(true)
  notes << 'on-key-coerced'
end

def str(value)
  value.nil? ? nil : value.to_s
end

def flag(value)
  case value
  when nil then nil
  when true, 'true' then true
  when false, 'false' then false
  else value.to_s
  end
end

def stringify_map(value)
  return {} unless value.is_a?(Hash)
  value.each_with_object({}) { |(key, item), out| out[key.to_s] = item.nil? ? nil : item.to_s }
end

def permissions_map(value)
  return {} if value.nil?
  return { '*': value.to_s } unless value.is_a?(Hash)
  value.each_with_object({}) { |(key, item), out| out[key.to_s] = item.nil? ? nil : item.to_s }
end

BUILD_USES = %r{\A(?:actions/setup-go|actions/setup-node|actions/setup-python)(?:@|\z)}
BUILD_RUN = %r{(?:\bgo[ \t]+(?:build|test|vet|run|install|mod)\b)|(?:\bnpm[ \t]+(?:ci|install|run)\b)|(?:\bbuild-css\.sh\b)|(?:\bmake\b)|(?:\bgovulncheck\b)|(?:\brelease-validation\.sh\b)|(?:\bscripts/tests/)|(?:\brun-backup-restore-busybox\.sh\b)}
INSTALLER_RUN = %r{(?:\binstall-contabase[\w.-]*\.sh\b)|(?:\bupdate-contabase[\w.-]*\.sh\b)|(?:\bscripts/install(?:\.sh|-[\w.-]+\.sh)?\b)|(?:\bscripts/update(?:\.sh|-[\w.-]+\.sh)?\b)|(?:\bapt-get\b)|(?:\bapk[ \t]+add\b)}
PUBLISH_USES = %r{\Asoftprops/action-gh-release(?:@|\z)}

workflow_permissions = permissions_map(doc['permissions'])

concurrency = doc['concurrency']
concurrency_out =
  if concurrency.is_a?(Hash)
    {
      'group' => str(concurrency['group']),
      'cancel_in_progress' => flag(concurrency['cancel-in-progress'])
    }
  elsif concurrency.is_a?(String)
    { 'group' => concurrency, 'cancel_in_progress' => nil }
  else
    { 'group' => nil, 'cancel_in_progress' => nil }
  end

jobs = {}
(doc['jobs'] || {}).each do |job_id, job|
  job = {} unless job.is_a?(Hash)
  raw_steps = job['steps'].is_a?(Array) ? job['steps'] : []

  steps = raw_steps.map.with_index do |step, index|
    step = {} unless step.is_a?(Hash)
    {
      'index' => index,
      'name' => str(step['name']),
      'id' => str(step['id']),
      'uses' => str(step['uses']),
      'run' => str(step['run']),
      'if' => str(step['if']),
      'continue_on_error' => str(step['continue-on-error']),
      'with' => stringify_map(step['with']),
      'env' => stringify_map(step['env'])
    }
  end

  uses_list = steps.map { |step| step['uses'].to_s }
  run_list = steps.map { |step| step['run'].to_s }

  uploads = steps.each_with_index.select { |step, _| step['uses'].to_s.start_with?('actions/upload-artifact') }
                 .map do |step, index|
    {
      'step' => index,
      'name' => step['with']['name'],
      'path' => step['with']['path'],
      'with' => step['with'],
      'if' => step['if']
    }
  end

  downloads = steps.each_with_index.select { |step, _| step['uses'].to_s.start_with?('actions/download-artifact') }
                   .map do |step, index|
    {
      'step' => index,
      'name' => step['with']['name'],
      'path' => step['with']['path'],
      'with' => step['with'],
      'if' => step['if']
    }
  end

  checkouts = steps.each_with_index.select { |step, _| step['uses'].to_s.start_with?('actions/checkout') }
                   .map do |step, index|
    {
      'step' => index,
      'uses' => step['uses'],
      'persist_credentials' => flag(step['with']['persist-credentials']),
      'ref' => step['with']['ref']
    }
  end

  needs = Array(job['needs']).map(&:to_s)

  job_permissions = job.key?('permissions') ? permissions_map(job['permissions']) : workflow_permissions

  jobs[job_id.to_s] = {
    'needs' => needs,
    'outputs' => stringify_map(job['outputs']),
    'if' => str(job['if']),
    'permissions' => permissions_map(job['permissions']),
    'effective_permissions' => job_permissions,
    'is_publisher' => uses_list.any? { |use| use.match?(PUBLISH_USES) },
    'publishes_image' => uses_list.any? { |use| use.start_with?('docker/build-push-action') },
    'has_build_tooling' => uses_list.any? { |use| use.match?(BUILD_USES) } ||
                           run_list.any? { |run| run.match?(BUILD_RUN) },
    'runs_installer' => run_list.any? { |run| run.match?(INSTALLER_RUN) },
    'upload_artifact_capable' => uploads.any?,
    'download_artifact_capable' => downloads.any?,
    'uploads' => uploads,
    'downloads' => downloads,
    'checkouts' => checkouts,
    'steps' => steps
  }
end

puts JSON.pretty_generate(
  'path' => path,
  'name' => str(doc['name']),
  'on' => doc['on'],
  'permissions' => workflow_permissions,
  'concurrency' => concurrency_out,
  'jobs' => jobs,
  'notes' => notes
)
RUBY

extract_workflow() {
  local src="$1" dst="$2"
  ruby "$PARSER_SCRIPT" "$src" > "$dst"
}

if (( PARSER == 1 )); then
  for pair in "art:$ART_WF" "pre:$PRE_WF"; do
    key="${pair%%:*}"
    file="${pair#*:}"
    if ! extract_workflow "$file" "$WORK/$key.json"; then
      printf 'ERRO: falha ao interpretar %s como YAML\n' "$file" >&2
      exit 3
    fi
  done
  jq -s '.' "$WORK/art.json" "$WORK/pre.json" > "$WORK/combined.json"
fi

JQ_LIB='
def jobs: .[] | .path as $p | .jobs | to_entries[]
  | ({file: $p, id: .key} + (.value // {}));
def publishers: jobs | select(.is_publisher == true);
def builders: jobs | select(.has_build_tooling == true);
def writers: jobs | select(((.effective_permissions.contents // "none") | tostring) == "write");
# Gramatica minima do verificador de manifesto.
# Aceita somente o comando standalone cujo status determina o resultado do
# passo: [sudo] [command] (sha256sum|shasum -a 256) (-c|--check) <checksums.txt>
# A ancoragem da linha inteira rejeita prefixos que condicionam o status
# (if, !, eval, bash -c, subshell) e qualquer token apos o manifesto que possa
# mascarar a falha (pipe, ||, ;, &, &&, redirecionamento, segundo comando).
# Apenas um comentario de shell final e tolerado. Nao e um interpretador de
# Bash: restringe a gramatica aceita e delega o comportamento real ao shell.
def checksum_manifest_arg:
  "(?:\"[^\"]*checksums\\.txt\"|\\x27[^\\x27]*checksums\\.txt\\x27|[^[:space:]\"\\x27;|&]*checksums\\.txt)";
def checksum_command:
  test("^(?:sudo[[:blank:]]+)?(?:command[[:blank:]]+)?(?:sha256sum|shasum[[:blank:]]+-a[[:blank:]]+256)[[:blank:]]+(?:--check|-c)[[:blank:]]+"
       + checksum_manifest_arg
       + "(?:[[:blank:]]*#[^#]*)?[[:blank:]]*$");
# A verificacao precisa ser incondicional: continue-on-error e if capazes de
# pular ou neutralizar a falha invalidam o passo como verificacao obrigatoria.
def checksum_step_enabled($step):
  ((($step.continue_on_error // "false") | ascii_downcase) as $coe
   | ($coe == "" or $coe == "false" or $coe == "${{ false }}"))
  and ((($step.if // "") | gsub("[[:blank:]]"; "")) as $cond
   | ($cond == "" or $cond == "true" or $cond == "${{ true }}" or $cond == "${{ success() }}"));
def errexit_enabled:
  test("(?m)^[[:blank:]]*set[[:blank:]]+(-[A-Za-z]*e[A-Za-z]*|-o[[:blank:]]+errexit)([[:blank:];]|$)");
def errexit_disabled:
  test("(?m)^[[:blank:]]*set[[:blank:]]+(\\+[A-Za-z]*e[A-Za-z]*|\\+o[[:blank:]]+errexit)([[:blank:];]|$)");
def step_lines: (.run // "") | split("\n") | map(gsub("^[[:blank:]]+";"") | gsub("[[:blank:]]+$";""));
def has_real_checksum: ((step_lines | map(select(checksum_command)) | length) > 0);
def checksum_step_verifications:
  publishers as $job
  | $job.steps[]
  | . as $step
  | select($step | has_real_checksum)
  | {job: $job.id, name: ($step.name // "run"), step: $step.index,
     fail_closed: ((checksum_step_enabled($step))
                   and ((($step.run // "") | errexit_enabled))
                   and (((($step.run // "") | errexit_disabled)) | not))};
def publisher_manifest_line_problems:
  publishers as $job
  | $job.steps[] as $step
  | (($step.run // "") | split("\n")[])
  | gsub("^[[:blank:]]+";"") | gsub("[[:blank:]]+$";"")
  | select(length > 0)
  | select((startswith("#") | not))
  | select(test("checksums\\.txt"))
  | select((checksum_command | not))
  | "\($job.id): linha do publicador manipula checksums.txt fora da verificacao real: \(.)";
def publisher_checksum_masking_problems:
  publishers as $job
  | $job.steps[]
  | . as $step
  | select($step | has_real_checksum)
  | select((checksum_step_enabled($step)) | not)
  | "\($job.id): verificacao de checksums condicionada (continue-on-error=\($step.continue_on_error // ""), if=\($step.if // ""))";
def publisher_checksum_problems:
  ([ checksum_step_verifications | select(.fail_closed == true) ] | length) as $ok
  | (if $ok >= 1 then empty else "publicador sem verificacao real e fail-closed de checksums.txt" end),
    publisher_manifest_line_problems,
    publisher_checksum_masking_problems;
def is_download: ((.uses // "") | startswith("actions/download-artifact"));
def is_attach: ((.uses // "") | startswith("softprops/action-gh-release"));
def is_state_step: ((.run // "") | contains(".github/scripts/release-state.sh"));
def step_index($job; cond):
  [ $job.steps[] | select(cond) | .index ] as $found
  | if ($found | length) == 0 then -1 else ($found | min) end;
def publisher_order_problems:
  publishers as $job
  | (step_index($job; is_download)) as $download
  | (step_index($job; has_real_checksum)) as $verify
  | (step_index($job; is_state_step)) as $state
  | (step_index($job; is_attach)) as $attach
  | select($download == -1 or $verify == -1 or $state == -1 or $attach == -1
           or $download >= $verify or $verify >= $state or $state >= $attach)
  | "\($job.id): download=\($download) verify=\($verify) state=\($state) attach=\($attach)";
def publisher_download_problems:
  . as $root
  | (($root | publishers | select((.downloads | length) == 0)
      | "\(.id): publicador sem download de artefato")),
    (($root | publishers) as $job
     | ($job.downloads[]) as $download
     | ($download.with // {}) as $with
     | ($root | [ jobs
         | select((.uploads // []) | map(.name) | index($download.name)) | .id ]) as $producers
     | (if (($with["run-id"] // "") != "") then "\($job.id): download com override run-id=\($with["run-id"])" else empty end),
       (if (($with.repository // "") != "") then "\($job.id): download com override repository=\($with.repository)" else empty end),
       (if (($with["github-token"] // "") != "") then "\($job.id): download com override github-token" else empty end),
       (if (($download.name // "") == "") then "\($job.id): download sem nome explicito" else empty end),
       (if ($producers | length) != 1 then "\($job.id): nome \($download.name // "?") nao corresponde a um unico upload" else empty end),
       (if (($producers | length) == 1) and (($job.needs | index($producers[0])) == null)
          then "\($job.id): needs nao declara o job produtor \($producers[0])" else empty end));

# Cadeia estrutural do manifesto: o output do produtor precisa chegar ao step
# de verificacao do publicador por identificadores reais, nao por texto solto.
def artifact_jobs: [ jobs | select(.id == "artifacts") ];
def publisher_jobs: [ publishers ];
def non_comment_lines:
  (.run // "")
  | split("\n")
  | map(gsub("^[[:blank:]]+";"") | gsub("[[:blank:]]+$";""))
  | map(select(length > 0))
  | map(select(startswith("#") | not));
def manifest_producer_steps:
  [ artifact_jobs | .[] | .steps[] | select((.id // "") == "manifest") ];
def manifest_verify_steps:
  [ publisher_jobs
    | .[] as $job
    | $job.steps[] as $step
    | select(
        ((($step.env.PRODUCER_MANIFEST // "") | gsub("[[:blank:]]";""))
          == "${{needs.artifacts.outputs.manifest}}")
        and (($step | non_comment_lines
               | map(select(test("^[^#]*PRODUCER_MANIFEST")))
               | length) > 0)
        and (($step | non_comment_lines
               | map(select(test("^[^#]*PRODUCER_MANIFEST")
                      and test("==|!=|-eq|-ne")
                      and test("\\[\\[|(^|[[:blank:]])(if|elif|while|until)[[:blank:]]")))
               | length) > 0)
      )
    | {job: $job.id, step: $step.index, name: ($step.name // "run")} ];
def manifest_chain_order_problems:
  publisher_jobs as $jobs
  | if ($jobs | length) != 1 then "publicador ausente ou duplicado"
    else
      $jobs[0] as $job
      | (step_index($job; is_download)) as $download
      | (manifest_verify_steps[0].step // -1) as $verify
      | (step_index($job; is_attach)) as $attach
      | if $download == -1 then "publicador sem download"
        elif $verify == -1 then "publicador sem verificacao de manifesto"
        elif $attach == -1 then "publicador sem step de publicacao"
        elif $download >= $verify then "verificacao do manifesto nao ocorre apos o download (download=\($download), verify=\($verify))"
        elif $verify >= $attach then "verificacao do manifesto nao ocorre antes da publicacao (verify=\($verify), attach=\($attach))"
        else empty end
    end;
'

jqf() {
  local out status=0
  out="$(jq -r "$JQ_LIB $1" "$WORK/combined.json" 2>"$WORK/jq.err")" || status=$?
  if (( status != 0 )); then
    printf 'jq-error(status=%s): %s\n' "$status" "$(head -n 1 "$WORK/jq.err")" >&2
    return "$status"
  fi
  printf '%s\n' "$out"
}

expect_empty() {
  local id="$1" desc="$2" expr="$3" out status=0
  out="$(jqf "$expr")" || status=$?
  if (( status != 0 )); then
    fail "$id $desc"
    printf '      erro ao avaliar expressao jq (status=%s)\n' "$status"
    return
  fi
  if [[ -z "$out" ]]; then
    pass "$id $desc"
  else
    fail "$id $desc"
    printf '      %s\n' "$out"
  fi
}

expect_value() {
  local id="$1" desc="$2" expected="$3" expr="$4" out status=0
  out="$(jqf "$expr")" || status=$?
  if (( status != 0 )); then
    fail "$id $desc"
    printf '      erro ao avaliar expressao jq (status=%s)\n' "$status"
    return
  fi
  if [[ "$out" == "$expected" ]]; then
    pass "$id $desc"
  else
    fail "$id $desc"
    printf '      esperado=%s obtido=%s\n' "$expected" "${out:-<vazio>}"
  fi
}

# ---------------------------------------------------------------------------
# W01-W05: permissoes de escrita durante build/teste
# ---------------------------------------------------------------------------
if (( PARSER == 1 )); then
  expect_empty W01 'nenhum workflow declara permissao de escrita no nivel raiz' '
    [ .[] | .path as $p | (.permissions // {}) | to_entries[]
      | select((.value | tostring) | test("write"))
      | "\($p): permissions.\(.key)=\(.value)" ] | .[]'

  expect_empty W02 'nenhum job de build/teste possui contents:write' '
    builders
    | select(((.effective_permissions.contents // "none") | tostring) == "write")
    | "\(.file)#\(.id) (build/teste com contents:write)"'

  expect_empty W03 'nenhum job de build/teste possui packages:write' '
    builders
    | select(((.effective_permissions.packages // "none") | tostring) == "write")
    | "\(.file)#\(.id)"'

  expect_empty W04 'nenhum job declara actions/id-token/pull-requests/security-events:write' '
    jobs
    | select([.effective_permissions | to_entries[]
        | select((.value | tostring) == "write") | .key]
        | any(. == "actions" or . == "id-token" or . == "pull-requests"
              or . == "security-events" or . == "deployments" or . == "issues"))
    | "\(.file)#\(.id)"'

  expect_empty W05 'nenhum job de build/teste publica imagem para registry' '
    builders | select(.publishes_image == true) | "\(.file)#\(.id)"'
fi

# ---------------------------------------------------------------------------
# W06-W07: persistencia de credenciais no checkout
# ---------------------------------------------------------------------------
if (( PARSER == 1 )); then
  expect_empty W06 'todo checkout de release-artifacts usa persist-credentials: false' '
    [ .[] | select(.path == "'"$ART_WF"'") ] | .[0] as $wf
    | $wf.jobs | to_entries[] | . as $job
    | $job.value.checkouts[]
    | select(.persist_credentials != false)
    | "\($job.key) step \(.step) \(.uses) persist-credentials=\(.persist_credentials // "ausente")"'

  expect_empty W07 'todo checkout de release-preflight usa persist-credentials: false' '
    [ .[] | select(.path == "'"$PRE_WF"'") ] | .[0] as $wf
    | $wf.jobs | to_entries[] | . as $job
    | $job.value.checkouts[]
    | select(.persist_credentials != false)
    | "\($job.key) step \(.step) \(.uses) persist-credentials=\(.persist_credentials // "ausente")"'
fi

# ---------------------------------------------------------------------------
# W08-W11: separacao do publicador
# ---------------------------------------------------------------------------
if (( PARSER == 1 )); then
  expect_value W08 'existe exatamente um job publicador' '1' '
    [ publishers ] | length'

  expect_empty W09 'somente o job publicador possui contents:write' '
    writers | select(.is_publisher != true) | "\(.file)#\(.id)"'

  expect_empty W10 'job publicador e dedicado, sem toolchain de build' '
    publishers | select(.has_build_tooling == true)
    | "\(.file)#\(.id) acumula build/teste e publicacao"'

  expect_empty W11 'job publicador nao executa instaladores' '
    publishers | select(.runs_installer == true) | "\(.file)#\(.id)"'
fi

# ---------------------------------------------------------------------------
# W12-W15: pureza do publicador
# ---------------------------------------------------------------------------
if (( PARSER == 1 )); then
  expect_empty W12 'publicador nao usa setup-go/setup-node/docker build' '
    publishers | . as $job | .steps[]
    | select((.uses // "") | test("^(actions/setup-go|actions/setup-node|actions/setup-python|docker/)"))
    | "\($job.id): \(.name // .uses)"'

  expect_empty W13 'publicador nao executa npm, go ou dependencias de build' '
    publishers | . as $job | .steps[]
    | select((.run // "") | test("(^|[[:space:]])(go|npm|make|node)[[:space:]]|release-validation\\.sh|build-css\\.sh|scripts/tests/"))
    | "\($job.id): \(.name // "run")"'

  expect_value W14 'publicador possui apenas contents:write como escrita' 'contents' '
    publishers
    | [ .effective_permissions | to_entries[]
        | select((.value | tostring) == "write") | .key ] | sort | join(",")'

  expect_value W15 'publicador nao aciona action de publicacao mais de uma vez' '1' '
    [ publishers | .steps[] | select((.uses // "") | test("^softprops/action-gh-release")) ] | length'
fi

# ---------------------------------------------------------------------------
# W16-W21: contrato de entrega de artefatos
# ---------------------------------------------------------------------------
if (( PARSER == 1 )); then
  expect_value W16 'job de build publica os tres assets canonicos' '1' '
    [ builders | .uploads[]
      | select((.path // "")
          | test("contabase-linux-amd64\\.tar\\.gz")
          and test("contabase-linux-arm64\\.tar\\.gz")
          and test("checksums\\.txt")) ] | length'

  expect_value W17 'publicador baixa artefato por nome explicito' '1' '
    [ publishers | .downloads[] | select((.name // "") != "") ] | length'

  expect_value W18 'nome baixado pelo publicador corresponde a um upload unico' '1' '
    . as $root
    | ([ $root | publishers | .downloads[] | .name ]) as $downloaded
    | ([ $root | jobs | .uploads[].name ]) as $uploaded
    | if ($downloaded | length) == 1
      then ([ $uploaded[] | select(. == $downloaded[0]) ] | length)
      else 0 end'

  expect_value W19 'nome do artefato e vinculado a versao ou commit validado' '1' '
    [ jobs | .uploads[]
      | select((.name // "")
          | test("\\$\\{\\{[^}]*outputs\\.(version|commit)[^}]*\\}\\}|\\$\\{\\{[^}]*github\\.sha[^}]*\\}\\}")) ] | length'

  expect_value W20 'publicador depende de exatamente um job de build que publica o artefato' '1' '
    [ publishers | .needs[] | select(. != "prepare") ] as $deps
    | if ($deps | length) == 1
      then ([ builders | select(.id == $deps[0]) | select(.upload_artifact_capable == true) ] | length)
      else 0 end'

  expect_value W21 'publicador nao baixa todos os artefatos indiscriminadamente' '0' '
    [ publishers | .downloads[] | select((.name // "") == "") ] | length'
fi

# ---------------------------------------------------------------------------
# W22-W24: manifesto deterministico e verificavel
# ---------------------------------------------------------------------------
w22_manifest_generation() {
  local missing=()
  grep -Fq 'sha256sum contabase-linux-amd64.tar.gz contabase-linux-arm64.tar.gz > checksums.txt' "$VALIDATION" \
    || missing+=('geracao sem lista explicita e ordenada (sha256sum <amd64> <arm64> > checksums.txt)')
  grep -Fq 'expected_names=' "$VALIDATION" \
    || missing+=('sem assercao de nomes/ordem esperados')
  grep -Fq 'sha256sum -c checksums.txt' "$VALIDATION" \
    || missing+=('sem verificacao sha256sum -c do manifesto')
  if (( ${#missing[@]} == 0 )); then
    printf 'manifesto: geracao explicita, ordenada e verificada no build\n'
    return 0
  fi
  printf '%s\n' "${missing[@]}"
  return 1
}

w23_manifest_names() {
  grep -Fq 'expected_names=$'"'"'contabase-linux-amd64.tar.gz\ncontabase-linux-arm64.tar.gz'"'"'' "$VALIDATION" \
    || { printf 'conjunto esperado do manifesto nao fixa exatamente os dois tarballs\n'; return 1; }
  printf 'manifesto: exatamente dois tarballs em ordem fixa\n'
}

w24_publisher_reverifies() {
  local problems status=0
  problems="$(jqf 'publisher_checksum_problems')" || status=$?
  if (( status != 0 )); then
    printf 'erro ao avaliar publisher_checksum_problems (status=%s)\n' "$status"
    return 1
  fi
  if [[ -z "$problems" ]]; then
    printf 'verificacao real e fail-closed de checksums.txt no publicador\n'
    return 0
  fi
  printf '%s\n' "$problems"
  return 1
}

run_test() {
  local id="$1" desc="$2" fn="$3" out status=0
  out="$("$fn" 2>&1)" || status=$?
  if (( status == 0 )); then
    pass "$id $desc"
  else
    fail "$id $desc"
    [[ -n "$out" ]] && printf '      %s\n' "$out"
  fi
}

if (( PARSER == 1 )); then
  run_test W22 'manifesto gerado de forma deterministica e verificavel' w22_manifest_generation
  run_test W23 'manifesto cobre exatamente os dois tarballs esperados' w23_manifest_names
  run_test W24 'publicador revalida o manifesto apos receber o artefato' w24_publisher_reverifies
fi

# ---------------------------------------------------------------------------
# W25-W27: dependencia GHCR -> publicacao de assets (D1)
# ---------------------------------------------------------------------------
if (( PARSER == 1 )); then
  expect_value W25 'existe um unico job dedicado de publicacao de assets' '1' '
    [ publishers | select(.has_build_tooling != true) ] | length'

  expect_value W26 'job ghcr declara needs do publicador dedicado' '1' '
    [ publishers | select(.has_build_tooling != true) | .id ] as $dedicated
    | if ($dedicated | length) == 1
      then ([jobs | select(.id == "ghcr") | .needs[] | select(. == $dedicated[0])] | length)
      else -1 end'

  expect_empty W27 'ghcr nao ignora falha ou ausencia do publicador com always()' '
    jobs | select(.id == "ghcr") | select((.if // "") | test("always\\(\\)"))
    | "\(.file)#\(.id)"'
fi

# ---------------------------------------------------------------------------
# W28-W33: binding entre jobs e protecoes de publicacao
# ---------------------------------------------------------------------------
if (( PARSER == 1 )); then
  expect_value W28 'publicador declara needs do job prepare' '1' '
    [ publishers | .needs[] | select(. == "prepare") ] | length'

  expect_value W29 'publisher usa tag_name a partir de outputs.version' 'true' '
    publishers | .steps[] | select((.uses // "") | test("^softprops/action-gh-release"))
    | (.with.tag_name // "") | test("outputs\\.version")'

  expect_value W30 'publisher usa target_commitish a partir de outputs.commit' 'true' '
    publishers | .steps[] | select((.uses // "") | test("^softprops/action-gh-release"))
    | (.with.target_commitish // "") | test("outputs\\.commit")'

  expect_value W31 'publicacao mantem draft: true' 'true' '
    publishers | .steps[] | select((.uses // "") | test("^softprops/action-gh-release"))
    | .with.draft'

  expect_value W32 'publicacao mantem prerelease: true' 'true' '
    publishers | .steps[] | select((.uses // "") | test("^softprops/action-gh-release"))
    | .with.prerelease'

  expect_value W33 'nenhum passo forca draft: false' '0' '
    [ .[] | .jobs | to_entries[] | .value.steps[]
      | select((.with.draft // "") == "false") ] | length'
fi

if (( PARSER == 1 )); then
  expect_value W34 'release-artifacts mantem concurrency sem cancelamento' 'false' '
    [ .[] | select(.path == "'"$ART_WF"'") | .concurrency.cancel_in_progress ] | .[0]'

  expect_value W35 'concurrency do release-artifacts inclui repositorio e ref' 'true' '
    [ .[] | select(.path == "'"$ART_WF"'") | .concurrency.group ] | .[0]
    | test("github\\.repository") and test("github\\.ref")'
fi

# ---------------------------------------------------------------------------
# W36-W38: reutilizacao de release-state.sh a partir da referencia validada (D2)
# ---------------------------------------------------------------------------
if (( PARSER == 1 )); then
  expect_value W36 'publicador invoca .github/scripts/release-state.sh' '1' '
    [ publishers | .steps[]
      | select((.run // "") | contains(".github/scripts/release-state.sh")) ] | length'

  expect_empty W37 'publicador nao reimplementa release-state inline (gh api)' '
    publishers | . as $job | .steps[]
    | select((.run // "") | test("gh[[:space:]]+api"))
    | "\($job.id): \(.name // "run")"'

  expect_value W38 'publicador faz checkout da referencia Git previamente validada' '1' '
    [ publishers | .checkouts[] | select((.ref // "") | test("outputs\\.commit")) ] | length'
fi

# ---------------------------------------------------------------------------
# W39: softprops/action-gh-release pinada por SHA completo (D3)
# ---------------------------------------------------------------------------
if (( PARSER == 1 )); then
  expect_empty W39 'softprops/action-gh-release usa SHA de 40 hexadecimais' '
    publishers | . as $job | .steps[]
    | select((.uses // "") | test("^softprops/action-gh-release"))
    | (.uses | sub("^softprops/action-gh-release@"; ""))
    | select(test("^[0-9a-f]{40}$") | not)
    | "\($job.id): uses=\(.)"'

  expect_value W40 'referencia do softprops nao usa tag ou branch mutavel' '0' '
    [ publishers | .steps[]
      | select((.uses // "") | test("^softprops/action-gh-release"))
      | (.uses | sub("^softprops/action-gh-release@"; ""))
      | select((test("^[0-9a-f]{40}$") | not) and (test("^v?[0-9]") or test("^(main|master|beta|stable)$"))) ]
    | length'
fi

# ---------------------------------------------------------------------------
# W41-W42: ordem obrigatoria e binding do download no job publicador (A2/A3)
# ---------------------------------------------------------------------------
if (( PARSER == 1 )); then
  expect_empty W41 'ordem do publicador: download, checksums, estado e anexacao' 'publisher_order_problems'

  expect_empty W42 'download do publicador usa o run corrente e o artefato do produtor' 'publisher_download_problems'
fi

# ---------------------------------------------------------------------------
# W43-W45: parser YAML fail-closed e normalizacao da chave on (A4)
# ---------------------------------------------------------------------------
if (( PARSER == 1 )); then
  printf 'jobs:\n  build: [unterminated\n' > "$WORK/parser-malformed.yml"
  printf -- '--- !ruby/object:OpenStruct\na: 1\n' > "$WORK/parser-object.yml"

  parser_probe() {
    local id="$1" desc="$2" fixture="$3" out status=0
    out="$(ruby "$PARSER_SCRIPT" "$fixture" 2>"$WORK/parser.err")" || status=$?
    if (( status != 0 )) && [[ -z "$out" ]] && grep -Fq 'yaml-parse-error' "$WORK/parser.err"; then
      pass "$id $desc"
    else
      fail "$id $desc"
      printf '      status=%s stdout=%s stderr=%s\n' "$status" "${out:0:120}" \
        "$(head -n 1 "$WORK/parser.err")"
    fi
  }

  parser_probe W43 'parser rejeita YAML malformado sem fallback inseguro' "$WORK/parser-malformed.yml"
  parser_probe W44 'parser rejeita marcador de objeto Ruby nao permitido' "$WORK/parser-object.yml"

  expect_value W45 'parser preserva a normalizacao da chave on' '2' '
    [ .[] | select(.on != null and ((.notes // []) | index("on-key-coerced") != null)) ] | length'
fi

# ---------------------------------------------------------------------------
# W64-W69: cadeia estrutural do manifesto produtor -> publicador
# ---------------------------------------------------------------------------
if (( PARSER == 1 )); then
  expect_empty W64 'job artifacts possui exatamente um step com id manifest' '
    artifact_jobs as $jobs
    | if ($jobs | length) != 1 then "job artifacts ausente ou duplicado"
      else
        ([ $jobs[0].steps[] | select((.id // "") == "manifest") ] | length) as $count
        | if $count != 1 then "job artifacts deve ter exatamente um step id=manifest (encontrado: \($count))" else empty end
      end'

  expect_empty W65 'step manifest escreve o output manifest em GITHUB_OUTPUT' '
    manifest_producer_steps as $steps
    | if ($steps | length) != 1 then "step id=manifest ausente ou duplicado"
      else
        $steps[0] as $step
        | ($step | non_comment_lines) as $lines
        | if ([ $lines[] | select(test(">>") and test("GITHUB_OUTPUT")) ] | length) == 0 then
            "step manifest nao redireciona para GITHUB_OUTPUT"
          elif ([ $lines[] | select(test("^(echo|printf)[[:blank:]]+[^#]*manifest<<")) ] | length) == 0 then
            "step manifest nao emite manifest<< no GITHUB_OUTPUT"
          else empty end
      end'

  expect_empty W66 'artifacts.outputs.manifest referencia steps.manifest.outputs.manifest' '
    artifact_jobs as $jobs
    | if ($jobs | length) != 1 then "job artifacts ausente ou duplicado"
      else
        (($jobs[0].outputs // {})["manifest"] // "") as $expr
        | ($expr | gsub("[[:blank:]]";"")) as $normalized
        | if $normalized != "${{steps.manifest.outputs.manifest}}" then
            "artifacts.outputs.manifest nao referencia steps.manifest.outputs.manifest (obtido: \($expr))"
          else empty end
      end'

  expect_empty W67 'job publish declara needs artifacts' '
    publisher_jobs as $jobs
    | if ($jobs | length) != 1 then "publicador ausente ou duplicado"
      else
        if (($jobs[0].needs // []) | index("artifacts")) == null then
          "job publish nao declara needs artifacts"
        else empty end
      end'

  expect_empty W68 'publicador consome PRODUCER_MANIFEST de needs.artifacts.outputs.manifest e compara no mesmo step' '
    manifest_verify_steps as $steps
    | if ($steps | length) == 0 then
        "publicador sem step que receba PRODUCER_MANIFEST de needs.artifacts.outputs.manifest e o compare"
      else empty end'

  expect_empty W69 'verificacao do manifesto ocorre depois do download e antes da publicacao' '
    manifest_chain_order_problems'
fi

# ---------------------------------------------------------------------------
# Fixtures offline do gate de publicacao (release-state.sh).
# Nenhum acesso a GitHub/GHCR: gh e substituido por shim local.
# ---------------------------------------------------------------------------
FIXTURE_ROOT="$WORK/fixture"
FIXTURE_REPO="$FIXTURE_ROOT/repo"
FIXTURE_ASSETS="$FIXTURE_REPO/release-dist"
SHIM_DIR="$FIXTURE_ROOT/shim"
GH_LOG="$FIXTURE_ROOT/gh.log"
FIXTURE_SKIP=0

if [[ ! -f "$STATE_HELPER" ]]; then
  skip 'W50-W63 helper release-state.sh ausente'
  FIXTURE_SKIP=1
fi

if (( FIXTURE_SKIP == 0 )); then
  mkdir -p "$FIXTURE_REPO" "$FIXTURE_ASSETS" "$SHIM_DIR"

  git -C "$FIXTURE_REPO" init -q
  git -C "$FIXTURE_REPO" config user.email 'workflow-hardening@example.invalid'
  git -C "$FIXTURE_REPO" config user.name 'workflow hardening fixture'

  FIXTURE_TAG='v1.0.0-beta.8'
  printf '%s\n' "$FIXTURE_TAG" > "$FIXTURE_REPO/VERSION"
  git -C "$FIXTURE_REPO" add VERSION
  git -C "$FIXTURE_REPO" commit -qm 'synthetic release fixture'
  git -C "$FIXTURE_REPO" tag -a "$FIXTURE_TAG" -m 'synthetic release fixture'
  FIXTURE_COMMIT="$(git -C "$FIXTURE_REPO" rev-parse "$FIXTURE_TAG^{commit}")"

  hash_file() {
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum "$1" | awk '{print $1}'
    else
      shasum -a 256 "$1" | awk '{print $1}'
    fi
  }

  manifest_verifies_clean() {
    (
      cd "$FIXTURE_ASSETS" || exit 1
      if command -v sha256sum >/dev/null 2>&1; then
        sha256sum -c checksums.txt >/dev/null 2>&1
      else
        shasum -a 256 -c checksums.txt >/dev/null 2>&1
      fi
    )
  }

  EXPECTED_ASSETS=(
    contabase-linux-amd64.tar.gz
    contabase-linux-arm64.tar.gz
    checksums.txt
  )

  printf 'synthetic amd64 payload\n' > "$FIXTURE_ASSETS/contabase-linux-amd64.tar.gz"
  printf 'synthetic arm64 payload\n' > "$FIXTURE_ASSETS/contabase-linux-arm64.tar.gz"
  write_valid_manifest() {
    {
      printf '%s  contabase-linux-amd64.tar.gz\n' "$(hash_file "$FIXTURE_ASSETS/contabase-linux-amd64.tar.gz")"
      printf '%s  contabase-linux-arm64.tar.gz\n' "$(hash_file "$FIXTURE_ASSETS/contabase-linux-arm64.tar.gz")"
    } > "$FIXTURE_ASSETS/checksums.txt"
  }
  write_valid_manifest

  cat > "$SHIM_DIR/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${GH_LOG:?}"
case "${GH_MODE:-fixture}" in
  fixture) sed -n '1,$p' "${GH_FIXTURE:?}" ;;
  error) exit 1 ;;
  *) exit 2 ;;
esac
EOF
  chmod +x "$SHIM_DIR/gh"

  asset_json() {
    local name="$1"
    local state="${2:-uploaded}"
    local digest="${3-}"
    if [[ -z "$digest" ]]; then
      digest="sha256:$(hash_file "$FIXTURE_ASSETS/$name")"
    fi
    printf '{"name":"%s","state":"%s","digest":"%s"}' "$name" "$state" "$digest"
  }

  all_assets_json() {
    local result=''
    local asset
    for asset in "${EXPECTED_ASSETS[@]}"; do
      [[ -n "$result" ]] && result+=','
      result+="$(asset_json "$asset")"
    done
    printf '[%s]' "$result"
  }

  write_release_fixture() {
    local name="$1" draft="$2" prerelease="$3" assets_json="$4"
    GH_FIXTURE="$WORK/$name.json"
    printf '[{"id":1,"tag_name":"%s","draft":%s,"prerelease":%s,"assets":%s}]\n' \
      "$FIXTURE_TAG" "$draft" "$prerelease" "$assets_json" > "$GH_FIXTURE"
  }

  write_empty_release_list() {
    GH_FIXTURE="$WORK/$1.json"
    printf '[]\n' > "$GH_FIXTURE"
  }

  reset_gate() {
    printf '%s\n' "$FIXTURE_TAG" > "$FIXTURE_REPO/VERSION"
    printf 'synthetic amd64 payload\n' > "$FIXTURE_ASSETS/contabase-linux-amd64.tar.gz"
    printf 'synthetic arm64 payload\n' > "$FIXTURE_ASSETS/contabase-linux-arm64.tar.gz"
    write_valid_manifest
    GATE_EVENT='push'
    GATE_REF_TYPE='tag'
    GATE_REF="refs/tags/$FIXTURE_TAG"
    GH_MODE='fixture'
    : > "$GH_LOG"
  }

  run_gate() {
    local tag="$1" commit="$2"
    (
      cd "$FIXTURE_REPO" || exit 1
      RELEASE_EVENT_NAME="$GATE_EVENT" \
      RELEASE_REF_TYPE="$GATE_REF_TYPE" \
      RELEASE_REF="$GATE_REF" \
      GITHUB_REPOSITORY='synthetic/workflow-hardening' \
      GH_BIN="$SHIM_DIR/gh" \
      GH_MODE="$GH_MODE" \
      GH_FIXTURE="$GH_FIXTURE" \
      GH_LOG="$GH_LOG" \
        "$STATE_HELPER" "$tag" "$commit" "$FIXTURE_ASSETS"
    )
  }

  capture_gate() {
    GATE_RESULT=0
    GATE_OUTPUT="$(run_gate "$@" 2>"$WORK/gate.stderr")" || GATE_RESULT=$?
    GATE_STATE="$(printf '%s' "$GATE_OUTPUT" | jq -r '.state // empty' 2>/dev/null)"
    GATE_REASON="$(printf '%s' "$GATE_OUTPUT" | jq -r '.reason // empty' 2>/dev/null)"
    GATE_UPLOADS="$(printf '%s' "$GATE_OUTPUT" | jq -r '.upload | length' 2>/dev/null)"
  }

  gate_case() {
    local id="$1" desc="$2" want_state="$3" want_exit="$4" want_uploads="${5:-}"
    local state_ok=0 uploads_ok=0
    [[ "$GATE_STATE" == "$want_state" && "$GATE_RESULT" == "$want_exit" ]] && state_ok=1
    if [[ -z "$want_uploads" ]]; then
      uploads_ok=1
    elif [[ "$GATE_UPLOADS" == "$want_uploads" ]]; then
      uploads_ok=1
    fi
    if (( state_ok == 1 && uploads_ok == 1 )); then
      pass "$id $desc"
    else
      fail "$id $desc"
      printf '      esperado state=%s exit=%s uploads=%s; obtido state=%s exit=%s uploads=%s (%s)\n' \
        "$want_state" "$want_exit" "${want_uploads:-*}" \
        "${GATE_STATE:-<vazio>}" "$GATE_RESULT" "${GATE_UPLOADS:-<vazio>}" "${GATE_REASON:-<sem motivo>}"
    fi
  }

  # W50: release draft completa e coerente
  reset_gate
  write_release_fixture w50 true true "$(all_assets_json)"
  capture_gate "$FIXTURE_TAG" "$FIXTURE_COMMIT"
  gate_case W50 'artefato valido: draft compativel libera publicacao' draft-compatible 0 0

  # W51: release inexistente agenda os tres assets
  reset_gate
  write_empty_release_list w51
  capture_gate "$FIXTURE_TAG" "$FIXTURE_COMMIT"
  gate_case W51 'release ausente agenda exatamente os assets canonicos' missing 0 3

  # W52: artefato local ausente bloqueia
  reset_gate
  write_release_fixture w52 true true "$(all_assets_json)"
  rm -f "$FIXTURE_ASSETS/contabase-linux-arm64.tar.gz"
  capture_gate "$FIXTURE_TAG" "$FIXTURE_COMMIT"
  gate_case W52 'artefato ausente bloqueia publicacao' incompatible 1
  reset_gate

  # W53: arquivo adulterado bloqueia
  reset_gate
  write_release_fixture w53 true true "$(all_assets_json)"
  printf 'tampered payload\n' > "$FIXTURE_ASSETS/contabase-linux-amd64.tar.gz"
  capture_gate "$FIXTURE_TAG" "$FIXTURE_COMMIT"
  gate_case W53 'arquivo adulterado bloqueia publicacao' incompatible 1
  reset_gate

  # W54: manifesto adulterado bloqueia
  reset_gate
  write_release_fixture w54 true true "$(all_assets_json)"
  printf '%064d  contabase-linux-amd64.tar.gz\n' 0 > "$FIXTURE_ASSETS/checksums.txt"
  capture_gate "$FIXTURE_TAG" "$FIXTURE_COMMIT"
  gate_case W54 'manifesto adulterado bloqueia publicacao' incompatible 1
  reset_gate

  # W55: nome inesperado no release bloqueia
  reset_gate
  write_release_fixture w55 true true "[$(all_assets_json | sed 's/^\[//; s/\]$//'),{\"name\":\"unexpected.txt\",\"state\":\"uploaded\",\"digest\":\"sha256:0000000000000000000000000000000000000000000000000000000000000000\"}]"
  capture_gate "$FIXTURE_TAG" "$FIXTURE_COMMIT"
  gate_case W55 'nome inesperado de asset bloqueia publicacao' incompatible 1
  reset_gate

  # W56: ref/tag divergente bloqueia
  reset_gate
  write_release_fixture w56 true true "$(all_assets_json)"
  GATE_REF='refs/tags/v1.0.0-beta.7'
  capture_gate "$FIXTURE_TAG" "$FIXTURE_COMMIT"
  gate_case W56 'ref Git divergente da tag bloqueia publicacao' incompatible 1
  reset_gate

  # W57: VERSION divergente da tag bloqueia
  reset_gate
  write_release_fixture w57 true true "$(all_assets_json)"
  printf '%s\n' 'v1.0.0-beta.7' > "$FIXTURE_REPO/VERSION"
  capture_gate "$FIXTURE_TAG" "$FIXTURE_COMMIT"
  gate_case W57 'VERSION divergente da tag bloqueia publicacao' incompatible 1
  reset_gate

  # W58: commit divergente do commit da tag bloqueia
  reset_gate
  write_release_fixture w58 true true "$(all_assets_json)"
  printf 'divergent commit\n' > "$FIXTURE_REPO/marker"
  git -C "$FIXTURE_REPO" add marker
  git -C "$FIXTURE_REPO" commit -qm 'divergent commit'
  DIVERGENT_COMMIT="$(git -C "$FIXTURE_REPO" rev-parse HEAD)"
  capture_gate "$FIXTURE_TAG" "$DIVERGENT_COMMIT"
  gate_case W58 'commit divergente da tag bloqueia publicacao' incompatible 1
  reset_gate

  # W59: release publicada quando deveria ser draft bloqueia
  reset_gate
  write_release_fixture w59 false true "$(all_assets_json)"
  capture_gate "$FIXTURE_TAG" "$FIXTURE_COMMIT"
  gate_case W59 'release publicada quando deveria ser draft bloqueia' published 1

  # W60: draft sem prerelease bloqueia
  reset_gate
  write_release_fixture w60 true false "$(all_assets_json)"
  capture_gate "$FIXTURE_TAG" "$FIXTURE_COMMIT"
  gate_case W60 'draft sem marcador prerelease bloqueia' incompatible 1

  # W61: execucao fora de tag push bloqueia
  reset_gate
  write_release_fixture w61 true true "$(all_assets_json)"
  GATE_REF_TYPE='branch'
  GATE_REF='refs/heads/beta'
  capture_gate "$FIXTURE_TAG" "$FIXTURE_COMMIT"
  gate_case W61 'execucao sem tag push bloqueia publicacao' incompatible 1
  reset_gate

  # W62: manifesto internamente divergente nao pode chegar ao release.
  # checksums.txt declara hash falso para o amd64; os digests remotos coincidem
  # com os arquivos locais (inclusive o manifesto adulterado), portanto
  # release-state.sh isolado aceita. O gate de publicacao precisa barrar.
  reset_gate
  printf '%064d  contabase-linux-amd64.tar.gz\n%s  contabase-linux-arm64.tar.gz\n' \
    0 "$(hash_file "$FIXTURE_ASSETS/contabase-linux-arm64.tar.gz")" > "$FIXTURE_ASSETS/checksums.txt"
  write_release_fixture w62 true true "$(all_assets_json)"
  capture_gate "$FIXTURE_TAG" "$FIXTURE_COMMIT"
  if (( PARSER == 0 )); then
    skip 'W62 manifesto internamente divergente barrado no publicador'
  else
    if [[ "$GATE_RESULT" == '0' ]]; then
      classifier_verdict='aceita'
    else
      classifier_verdict='rejeita'
    fi
    recheck_count="$(jqf '[ checksum_step_verifications | select(.fail_closed == true) ] | length')"
    if manifest_verifies_clean; then
      manifest_verdict='intacto'
    else
      manifest_verdict='divergente (sha256sum -c reprova)'
    fi
    if [[ "${recheck_count:-0}" -ge 1 && "$manifest_verdict" != 'intacto' ]]; then
      pass "W62 manifesto internamente divergente barrado no publicador (classificador isolado $classifier_verdict; verificacao real detecta manifesto $manifest_verdict)"
    else
      fail 'W62 manifesto internamente divergente barrado no publicador'
      printf '      release-state.sh isolado %s; manifesto %s; verificacoes reais no publicador=%s\n' \
        "$classifier_verdict" "$manifest_verdict" "${recheck_count:-0}"
    fi
  fi

  # W63: o gate nao escreve no remoto
  reset_gate
  write_release_fixture w63 true true "$(all_assets_json)"
  capture_gate "$FIXTURE_TAG" "$FIXTURE_COMMIT"
  if grep -Eq 'POST|PUT|PATCH|DELETE|upload|--method' "$GH_LOG"; then
    fail 'W63 gate de classificacao nao executa escrita remota'
    printf '      comandos observados: %s\n' "$(tr '\n' ';' < "$GH_LOG")"
  else
    pass 'W63 gate de classificacao nao executa escrita remota'
  fi
fi

# ---------------------------------------------------------------------------
# Resumo
# ---------------------------------------------------------------------------
printf '\n=== Resultado: %d passaram, %d falharam, %d ignorados ===\n' "$PASS" "$FAIL" "$SKIP"

if (( SKIP > 0 )); then
  printf '%s\n' 'PARCIAL: parser ou fixtures indisponiveis; suite NAO aprovada integralmente.' >&2
  exit 2
fi
if (( FAIL > 0 )); then
  printf '%s\n' 'RED: contratos de endurecimento pendentes.' >&2
  exit 1
fi
printf '%s\n' 'workflow hardening: PASS'
exit 0
