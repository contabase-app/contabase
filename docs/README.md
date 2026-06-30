# Documentação do ContaBase

Esta pasta reúne os guias públicos do ContaBase.

Se você está começando, leia nesta ordem:

1. [Instalação rápida](instalacao.md)
2. [Primeiros passos](primeiros-passos.md)
3. [Backup e restauração](backup-restauracao.md)
4. [Atualização](atualizacao.md)

## Guias principais

- [Instalação rápida](instalacao.md) — caminho curto com o instalador principal.
- [Instalação em LXC/VPS](instalacao-lxc-vps.md) — instalação sem Docker, com binário pronto e systemd.
- [Instalação com Docker](instalacao-docker.md) — Docker Compose, Docker guiado e Docker manual com GHCR.
- [Configuração](configuracao.md) — variáveis como `APP_BASE_URL`, `ALLOWED_HOSTS`, `TRUSTED_PROXIES`, `CONTABASE_ACCESS_MODE` e secrets.
- [Atualização](atualizacao.md) — como trocar de versão.
- [Backup e restauração](backup-restauracao.md) — como proteger os dados.
- [Solução de problemas](solucao-de-problemas.md) — erros comuns.
- [Segurança](../SECURITY.md) — política de segurança.

## Qual guia devo abrir?

| Quero... | Abrir |
|---|---|
| Instalar rápido | [Instalação rápida](instalacao.md) |
| Instalar em VPS/LXC sem Docker | [Instalação em LXC/VPS](instalacao-lxc-vps.md) |
| Instalar com Docker | [Instalação com Docker](instalacao-docker.md) |
| Usar Dockge, Portainer ou CasaOS | [Instalação com Docker](instalacao-docker.md) |
| Ajustar domínio/proxy | [Configuração](configuracao.md) |
| Atualizar versão | [Atualização](atualizacao.md) |
| Fazer backup | [Backup e restauração](backup-restauracao.md) |

## Versão atual

A versão recomendada deve ser obtida pelo canal de instalação ou pelas notas de release.
Para versão travada, use uma tag publicada no formato:

```text
vX.Y.Z
```

Use sempre uma versão publicada.

Não use `main`.

Não use `latest`.

## Referências

- [Requisitos](requisitos.md)
- [Arquitetura](arquitetura.md)
- [Permissões](permissoes.md)
- [Regras financeiras](regras-financeiras.md)
- [Retenção de dados](retencao-de-dados.md)
- [CLI administrativo](cli-admin.md)
- [Recuperação de senha de admin](recuperacao-senha-admin.md)
- [Checklist de deploy](checklist-deploy.md)
- [Segurança de deploy](seguranca-deploy.md)
- [Resposta a incidentes](resposta-a-incidentes.md)
- [Fluxo de release](fluxo-de-release.md)

## Notas de versão

- [v0.1.0-beta.3 — Beta pública controlada](releases/v0.1.0-beta.3.md)
- [v0.1.0-beta.2 — Beta pública controlada](releases/v0.1.0-beta.2.md)
- [v0.1.0-beta.1 — primeira Beta pública controlada](releases/v0.1.0-beta.1.md)
- [v0.1.0-alpha.1 — Primeira validação pública](releases/v0.1.0-alpha.1.md)
