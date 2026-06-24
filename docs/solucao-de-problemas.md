# ContaBase - Troubleshooting

## Servico nao sobe

Verifique:

```bash
docker compose ps
docker compose logs --tail 100 contabase
```

Se a causa for configuracao, revise `APP_ENV`, `AUTH_ENCRYPTION_KEY`, `APP_BASE_URL` e `ALLOWED_HOSTS`.

## Healthcheck falha

Confirme se o container esta no ar e se o banco esta acessivel:

```bash
curl -i http://localhost:8080/health
docker compose logs --tail 100 contabase
```

Se o app usa proxy, confira tambem `TRUSTED_PROXIES`.

## Imagem ou upload nao aparece

- Confirme se os arquivos existem em `data/uploads/`.
- Verifique se o restore copiou os uploads.
- Confira se a permissao do diretorio esta correta.

## Alterei o env e nada mudou

Depois de mudar `.env.docker`, recrie a stack:

```bash
docker compose up -d --build
```

## Docker nao atualiza

- Confirme se voce fez `git pull`.
- Confirme se voce fez `docker compose up -d --build`.
- Veja os logs.

```bash
git status --short
docker compose up -d --build
docker compose logs --tail 100 contabase
```

## Erro de permissao

Verifique se o usuario que roda a aplicacao consegue escrever em:

- `data/`
- `data/uploads/`
- `data/backups/`

## Setup token continua ativo

Depois do primeiro acesso, remova ou comente `CONTABASE_SETUP_TOKEN` e recrie a stack.

```bash
docker compose up -d --build
```

## Quando escalar

Se o problema persistir depois de conferir logs, env, permissões e backup, trate como incidente operacional e revise [Seguranca](seguranca.md) e [Backup e Restore](backup-restauracao.md).
