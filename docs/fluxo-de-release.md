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
- `v1.0.0-beta.3` — terceira candidata da linha 1.0, materializada como prerelease pública com artifacts, GHCR e canal Beta.
- `v1.0.0-beta.4` — quarta candidata da linha 1.0, materializada como prerelease pública.
- `v1.0.0-beta.5` — quinta candidata da linha 1.0, publicada como prerelease.
- `v1.0.0-beta.6` — sexta candidata da linha 1.0, publicada como prerelease.
- `v1.0.0-beta.7` — sétima candidata da linha 1.0, publicada como prerelease com artifacts Linux amd64/arm64, checksums e imagem GHCR; a promoção do canal Beta depende do smoke real de instalação e atualização.

## ContaBase v1.0.0-beta.8

- **Novidades da Beta 8:** remoção de Reservas e avanço do schema para 83, com updater autenticado por commit/manifesto, `flock`, proveniência de instalação, backup pré-migration verificável e correções de concorrência. A instalação exige verificar a disponibilidade da tag, GitHub Release, artefatos, GHCR e canal, além da homologação do método; esta seção não comprova publicação.

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

A partir da Beta 8, o canal de instalação também publica o manifesto autenticado do updater em `updaters/vX.Y.Z-beta.N/updater.sha256`, com commit, versão, SHA-256 do bundle e métodos suportados. Ele não é um asset da GitHub Release e só pode apontar para uma tag/release existente.

Assets são comparados por checksum antes do upload. Assets idênticos são preservados; apenas assets ausentes são enviados. Assets divergentes, inesperados ou incompletos bloqueiam o pipeline. Nenhum asset é apagado ou sobrescrito automaticamente.

## Instalação por método

- **Docker via GHCR:** tag fixa de versão e `beta` como canal mutável; sem `latest`;
- **release artifact:** recomendado para VPS/LXC Debian sem Docker;
- **source/build local (Source legado):** avançado, para desenvolvimento e customização.

A entrada recomendada é o [Guia Completo de Instalação](https://contabase.pages.dev/setup/) no site oficial. A URL de distribuição do script abaixo é um endpoint separado; a mudança do endereço do site não altera esse endpoint.

```bash
curl -fsSLo /tmp/contabase-install.sh https://get-contabase.pages.dev/install.sh && bash /tmp/contabase-install.sh
```

## Atualizações

Os updates Docker e Release exigem backup canônico e verificam o healthcheck. Backup não significa rollback automático: nos fluxos legados e no Release, a recuperação só pode usar uma cópia qualificada da Beta 6, com identidade exata, prova administrativa local válida e antes de qualquer tentativa de startup da candidata. Beta 7, Beta 8 e versões desconhecidas não são destinos de rollback automático; a recuperação automática avançada da Beta 8 fica adiada.

O método `docker-image`, fixado por digest, não realiza rollback automático nem emite ou consome provas de recuperação. O update do Source legado possui script próprio e preserva as mesmas restrições da recuperação histórica.

O Source Bootstrap é experimental e não possui atualização automática suportada; para novas instalações VPS/LXC, prefira o release artifact.

Após tentativa de startup da candidata, falhas não autorizam executar automaticamente código antigo sobre dados potencialmente migrados. Pode haver indisponibilidade até intervenção administrativa especializada com artefato verificado compatível com o schema atual. Preserve banco, WAL, uploads, configuração, chaves e backup. Restore de dados é uma operação separada, exige decisão explícita e nunca é automático.

## Onde acompanhar

- [CHANGELOG](../CHANGELOG.md)
- [Release Notes](../RELEASE_NOTES.md)
- [GitHub Releases](https://github.com/contabase-app/contabase/releases)
- [GHCR](https://github.com/contabase-app/contabase/pkgs/container/contabase)
- [Nota da v1.0.0-beta.8](releases/v1.0.0-beta.8.md)
- [Nota da v1.0.0-beta.7 — publicada](releases/v1.0.0-beta.7.md)
- [Nota da v1.0.0-beta.5 — publicada](releases/v1.0.0-beta.5.md)
- [Nota da v1.0.0-beta.4 — publicada](releases/v1.0.0-beta.4.md)
- [Nota da v1.0.0-beta.3 — publicada](releases/v1.0.0-beta.3.md)
- [Nota da v1.0.0-beta.2 — publicada](releases/v1.0.0-beta.2.md)
- [Nota da v0.1.0-beta.9](releases/v0.1.0-beta.9.md)
- [Nota da v0.1.0-beta.8](releases/v0.1.0-beta.8.md)
- [Nota da v0.1.0-beta.7](releases/v0.1.0-beta.7.md)
- [Acessar a Página Oficial](https://contabase.pages.dev/)
