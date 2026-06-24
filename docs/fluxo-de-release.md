# ContaBase — Versões e releases

O ContaBase usa SemVer. A tag pública, o arquivo `VERSION`, o changelog e a nota da release devem representar a mesma versão.

## Estágios

- `alpha`: validação inicial;
- `beta`: validação pública controlada, com limitações conhecidas;
- `stable`: versão considerada adequada para uso amplo.

`v0.1.0-beta.1` **já está publicada**. A tag, a GitHub Release e os assets estão disponíveis nos canais públicos.

## Fluxo de publicação

1. desenvolvimento e testes;
2. preparação e validação do snapshot candidato;
3. export público limpo;
4. criação da tag pública;
5. execução do workflow de artifacts;
6. geração de `amd64`, `arm64` e `checksums.txt`;
7. build e push da imagem multi-arch no GHCR;
8. criação da GitHub Release em draft;
9. smoke da imagem GHCR e dos artifacts em ambiente descartável;
10. publicação da release;
11. uso dos mesmos canais públicos pelos ambientes que adotarem a versão.

Cada etapa exige validação. A criação dos artifacts não publica automaticamente uma GitHub Release.

## Artifacts

Para `v0.1.0-beta.1`, a release deve conter:

- `contabase-linux-amd64.tar.gz`;
- `contabase-linux-arm64.tar.gz`;
- `checksums.txt`.

O workflow compila CSS, executa testes e `go vet`, gera os binários Linux, valida a estrutura dos bundles e confere SHA-256.

## Instalação por método

- **Docker via GHCR:** `v0.1.0-beta.1` como tag fixa e `beta` como canal mutável; sem `latest`;
- **release artifact:** recomendado para VPS/LXC Debian sem Docker;
- **source/build local:** avançado, para desenvolvimento e customização.

## Atualizações

O update Docker possui backup, healthcheck e rollback de runtime.
O update source possui script próprio.
O update automático por release artifact ainda não existe; até lá, a troca de versão deve ser uma reinstalação controlada.

## Onde acompanhar

- [CHANGELOG](../CHANGELOG.md)
- [Release Notes](../RELEASE_NOTES.md)
- [GitHub Releases](https://github.com/contabase-app/contabase/releases)
- [GHCR](https://github.com/contabase-app/contabase/pkgs/container/contabase)
- [Nota da v0.1.0-beta.1](releases/v0.1.0-beta.1.md)
