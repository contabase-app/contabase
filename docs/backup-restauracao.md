# ContaBase - Backup e Restore

## Objetivo

Este guia mostra como guardar e recuperar dados do ContaBase com seguranca.
O banco principal e SQLite, entao os dados ficam concentrados em arquivos.

## Onde ficam os dados

- Banco SQLite: `data/contabase.db`
- Uploads: `data/uploads/`
- Backups: `data/backups/`

## Backup pelo painel

O painel administrativo pode exportar o banco atual em formato `.db`.
Esse backup inclui os dados do banco, mas nao inclui os arquivos de upload nem os segredos do ambiente.

Use quando quiser uma copia rapida do banco.

## Backup operacional

O backup operacional e o mais completo para restore de rotina.
Ele deve guardar:

- banco SQLite;
- uploads;
- arquivo de ambiente real, como `.env.docker`;
- chaves e segredos usados pela instancia.

Exemplo:

```bash
chmod +x scripts/ops/backup.sh scripts/ops/restore.sh
./scripts/ops/backup.sh /caminho/seguro/para/backups
```

## O que precisa ser preservado

Guarde fora do Git e em local seguro:

- `data/contabase.db`
- `data/uploads/`
- `.env.docker`
- `AUTH_ENCRYPTION_KEY`
- `SECURITY_MASTER_KEY`
- `CONTABASE_SETUP_TOKEN`, enquanto o setup nao foi concluido

## Como restaurar

1. Pare a aplicacao.
2. Restaure o banco e os uploads.
3. Recrie a stack.

```bash
docker compose down
cp /caminho/do/backup/contabase.db data/contabase.db
rm -f data/contabase.db-wal data/contabase.db-shm
cp -R /caminho/do/backup/uploads data/uploads
docker compose up -d --build
```

Se voce usa o script de restore, leia os avisos dele antes de executar.

## Dicas praticas

- Nunca copie o `.db` enquanto a aplicacao estiver gravando sem usar um metodo seguro.
- Sempre teste o restore em ambiente de teste antes de depender do backup.
- Depois de restaurar, confira se o login e o healthcheck voltaram.
- Se o 2FA mudar de chave, preserve `AUTH_ENCRYPTION_KEY` para evitar perda de acesso.

## Erros comuns

| Problema | O que verificar |
|---|---|
| Anexos somem apos restore | Copie tambem `data/uploads/`. |
| 2FA antigo nao funciona apos restore | Confirme se `AUTH_ENCRYPTION_KEY` e o `.env.docker` original foram preservados. |
| Banco volta mas o app nao sobe | Veja `docker compose logs --tail 100 contabase`. |
| Backup nao inclui o que voce esperava | Confirme se voce fez backup operacional, nao apenas exportacao do painel. |

## Veja tambem

- [Configuracao](configuracao.md)
- [Atualizacao](atualizacao.md)
- [Seguranca](seguranca.md)
