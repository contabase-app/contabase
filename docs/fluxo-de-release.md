# ContaBase — Versões e releases

O ContaBase usa SemVer. A tag pública, o arquivo `VERSION`, o changelog e a nota da release devem representar a mesma versão.

## Estágios

- `alpha`: validação inicial;
- `beta`: validação pública controlada, com limitações conhecidas;
- `stable`: versão considerada adequada para uso amplo.

## Versões publicadas

- `v0.1.0-beta.1` — primeira Beta pública. Tag, GitHub Release, GHCR e assets disponíveis nos canais públicos.
- `v0.1.0-beta.3` — Beta pública atual/preparada para publicação.

## Fluxo de publicação

### Beta pública

1. desenvolvimento e validação na branch de desenvolvimento (ambiente privado);
2. atualização de `VERSION`, `CHANGELOG.md` e `RELEASE_NOTES.md`;
3. validações Go, diff check e export público limpo;
4. export e sync para a branch `beta` do repositório público (`contabase`);
5. criação da tag beta pública (ex: `v0.1.0-beta.3`);
6. push da branch `beta` e da tag para o repositório público;
7. execução do workflow de artifacts e GHCR;
8. criação da GitHub Release beta (`prerelease=true`);
9. smoke da imagem GHCR e dos artifacts em ambiente descartável;
10. atualização do canal `contabase-canal` para apontar para a nova tag, somente depois de release, assets e checksums validados.

### Stable pública

1. Beta pública validada em produção controlada;
2. export e sync para a branch `main` do repositório público;
3. criação da tag stable (ex: `v0.1.0`);
4. push da branch `main` e da tag para o repositório público;
5. execução do workflow de artifacts e GHCR;
6. criação da GitHub Release stable (`prerelease=false`);
7. atualização do canal `contabase-canal` (stable/recommended).

- `contabase/beta` é o destino de releases beta.
- `contabase/main` é o destino de releases stable.
- O repositório público **nunca** é fonte da verdade para desenvolvimento.

Cada etapa exige validação. A criação dos artifacts não publica automaticamente uma GitHub Release.

## Artifacts

A release contém:

- `contabase-linux-amd64.tar.gz`;
- `contabase-linux-arm64.tar.gz`;
- `checksums.txt`.

O workflow compila CSS, executa testes e `go vet`, gera os binários Linux, valida a estrutura dos bundles e confere SHA-256.

## Instalação por método

- **Docker via GHCR:** tag fixa de versão e `beta` como canal mutável; sem `latest`;
- **release artifact:** recomendado para VPS/LXC Debian sem Docker;
- **source/build local:** avançado, para desenvolvimento e customização.

A entrada recomendada é via canal público de instalação. O endpoint temporário é `https://get-contabase.pages.dev`; `https://get.contabase.net` é o domínio futuro e só deve virar comando principal quando estiver ativo.

```bash
curl -fsSLo /tmp/contabase-install.sh https://get-contabase.pages.dev/install.sh && bash /tmp/contabase-install.sh
```

## Atualizações

O update Docker possui backup, healthcheck e rollback de runtime.
O update source possui script próprio.
O update automático por release artifact ainda não existe; até lá, a troca de versão deve ser uma reinstalação controlada.

## Onde acompanhar

- [CHANGELOG](../CHANGELOG.md)
- [Release Notes](../RELEASE_NOTES.md)
- [GitHub Releases](https://github.com/contabase-app/contabase/releases)
- [GHCR](https://github.com/contabase-app/contabase/pkgs/container/contabase)
- [Nota da v0.1.0-beta.3](releases/v0.1.0-beta.3.md)
- [Canal de instalação](https://get-contabase.pages.dev)
