# ContaBase — Versões e releases

O ContaBase usa SemVer. A tag pública, o arquivo `VERSION`, o changelog e a nota da release devem representar a mesma versão.

## Estágios

- `alpha`: validação inicial;
- `beta`: validação pública controlada, com limitações conhecidas;
- `stable`: versão considerada adequada para uso amplo.

## Versões publicadas

- `v0.1.0-beta.1` — primeira Beta pública. Tag, GitHub Release, GHCR e assets disponíveis nos canais públicos.
- `v0.1.0-beta.7` — Beta pública anterior, preservada como caminho de atualização suportado.
- `v0.1.0-beta.8` — Beta publicada e posteriormente retirada; tag, Release e assets permanecem como histórico imutável.
- `v0.1.0-beta.9` — última Beta pública da linha 0.1. Tag, GitHub Release, GHCR e assets permanecem disponíveis.
- `v1.0.0-beta.2` — segunda candidata da linha 1.0, materializada como prerelease pública com artifacts, GHCR e canal Beta.

## Candidata em publicação

- `v1.0.0-beta.3` — candidata da linha 1.0 em preparação local, ainda dependente dos gates separados de branch pública, Release Preflight, tag, draft prerelease, artifacts, GHCR, smokes e canal.
- `v1.0.0-beta.2` permanece como versão materializada no canal Beta até a conclusão de todos os gates e a atualização validada do canal.

## Processo de publicação

As releases públicas são preparadas e revisadas pelos mantenedores antes de
serem disponibilizadas. Cada snapshot passa por validações de versão,
documentação, artifacts, checksums e instalação em ambiente descartável.

Uma versão é considerada materializada assim que qualquer superfície oficial
de distribuição existir: Release publicada ou com assets, artifacts finais,
imagem GHCR, canal ou promoção para instalação. A partir daí, sua tag e seu
identificador são imutáveis. Uma tag de tentativa que falhou no CI antes de
qualquer superfície oficial pode ser removida somente após auditoria formal.

### Preparação pública

Os gates `check`, `export`, `diff` e `sync` preparam o snapshot público.
Eles não dependem do canal de instalação — a preparação pode ocorrer enquanto
o canal Beta ainda aponta para a versão anterior.

A validação do canal é feita separadamente com a ferramenta interna de
publicação, que verifica se o manifest do canal está coerente com a versão alvo.
Ela é usada como gate pós-release, não durante a preparação do snapshot.

### Ordem final de publicação

1. preparar e revisar o snapshot público (`check`, `export`, `diff`, `sync`);
2. enviar a branch pública com autorização explícita;
3. aguardar o `Release Preflight` automático para o SHA exato da branch;
4. criar a tag somente se o preflight estiver verde e a branch remota ainda apontar para o mesmo SHA;
5. enviar somente a nova tag (`git push origin <tag>`);
6. o workflow da tag reconfirma o preflight e cria ou reutiliza um **draft prerelease** — nunca publica;
7. validar draft, assets e checksums (SHA-256);
8. validar a imagem GHCR com tag fixa;
9. executar smokes de instalação e atualização;
10. publicar o draft manualmente com autorização específica;
11. validar o canal com `validate-channel`;
12. avançar o canal Beta por commit forward e autorização própria.

Enquanto isso, o canal Beta permanece na última versão publicada e o canal
stable permanece `null`. Nenhuma etapa anterior antecipa o canal.

### Release Preflight

Todo push candidato para `beta` executa os mesmos gates críticos usados pelo
release: versão, dependências, CSS, `TZ=UTC go test ./... -count=1`, vet, build,
backup/restore GNU, BSD e BusyBox, packages amd64/arm64, checksums e conteúdo dos
archives. O preflight não cria tag, Release, GHCR ou canal.

### Disparo do workflow de publicação

Somente push de tag Beta válida (`v*`). O workflow não aceita
`workflow_dispatch`. Reruns para a mesma tag ocorrem pelo mecanismo nativo do
GitHub Actions. Tags materializadas são imutáveis — nunca retaggear ou usar
force push. Nenhuma tag é criada apenas para testar o workflow.

Antes de anexar assets, o workflow classifica o estado da release:

- `missing`: cria draft prerelease;
- `draft-compatible`: reutiliza draft existente;
- `published`: bloqueia (skip upload);
- `incompatible`: bloqueia com erro.

A release permanece como **draft** e **prerelease** durante todo o pipeline.
A publicação final exige autorização humana separada.

### Stable pública

Uma Stable é promovida somente depois da validação controlada de uma Beta.
Assim como na Beta, a publicação exige artifacts e checksums válidos, smoke de
instalação e atualização do canal apenas após a existência da release pública.

Cada etapa exige validação. A criação dos artifacts não publica automaticamente uma GitHub Release.

## Artifacts

A release contém:

- `contabase-linux-amd64.tar.gz`;
- `contabase-linux-arm64.tar.gz`;
- `checksums.txt`.

O workflow compila CSS, executa testes e `go vet`, gera os binários Linux, valida a estrutura dos bundles e confere SHA-256.

Assets são comparados por checksum antes do upload. Assets idênticos são preservados; apenas assets ausentes são enviados. Assets divergentes, inesperados ou incompletos bloqueiam o pipeline. Nenhum asset é apagado ou sobrescrito automaticamente.

## Instalação por método

- **Docker via GHCR:** tag fixa de versão e `beta` como canal mutável; sem `latest`;
- **release artifact:** recomendado para VPS/LXC Debian sem Docker;
- **source/build local:** avançado, para desenvolvimento e customização.

A entrada recomendada é via canal público de instalação em https://get-contabase.pages.dev.

```bash
curl -fsSLo /tmp/contabase-install.sh https://get-contabase.pages.dev/install.sh && bash /tmp/contabase-install.sh
```

## Atualizações

O update Docker possui backup, healthcheck e rollback de runtime.
O update source possui script próprio.
O update por release artifact segue o mesmo contrato de segurança (backup canônico obrigatório, healthcheck e rollback por restore explícito).

## Onde acompanhar

- [CHANGELOG](../CHANGELOG.md)
- [Release Notes](../RELEASE_NOTES.md)
- [GitHub Releases](https://github.com/contabase-app/contabase/releases)
- [GHCR](https://github.com/contabase-app/contabase/pkgs/container/contabase)
- [Nota da v1.0.0-beta.3 — candidata](releases/v1.0.0-beta.3.md)
- [Nota da v1.0.0-beta.2 — publicada](releases/v1.0.0-beta.2.md)
- [Nota da v0.1.0-beta.9](releases/v0.1.0-beta.9.md)
- [Nota da v0.1.0-beta.8](releases/v0.1.0-beta.8.md)
- [Nota da v0.1.0-beta.7](releases/v0.1.0-beta.7.md)
- [Canal de instalação](https://get-contabase.pages.dev)
