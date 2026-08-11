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

O pacote operacional canonico reune, em um unico diretorio versionado:

- banco SQLite;
- uploads;
- `manifest.txt`, com `backup_id`, versao anterior e metadados;
- `checksums.sha256`, para verificacao de integridade.

Antes de finalizar o pacote, o helper exige `integrity_check` e
`foreign_key_check` sem erros. O restore valida manifesto, inventario e
checksums antes de substituir o estado ativo.

O pacote nao inclui `.env.docker`, chaves ou segredos. Preserve esses itens
separadamente, fora do Git e com acesso restrito.

Exemplo:

```bash
chmod +x scripts/ops/backup.sh scripts/ops/restore.sh
./scripts/ops/backup.sh /caminho/seguro/para/backups
```

O script imprime como ultima linha o caminho exato do pacote `backup-*` criado.
Nao selecione o backup por glob, timestamp aproximado ou "mais recente".

## Backup automatico antes de update

`scripts/update-contabase-docker.sh` para a stack e, quando encontra
`data/contabase.db`, cria obrigatoriamente um pacote canonico antes de alterar
configuracao, checkout ou runtime. O helper vem do commit Git alvo e sua
identidade e conferida antes do uso.

- banco e uploads ausentes: estado inexistente, update pode continuar sem pacote;
- banco ausente e uploads presentes: estado inconsistente, update falha;
- banco invalido, ambiguo ou ilegivel: update falha;
- `--no-backup`: recusado; nao existe bypass de backup.

O resumo mostra o caminho do pacote, o `backup_id` e a versao anterior gravada.

## O que precisa ser preservado

Guarde fora do Git e em local seguro:

- `data/contabase.db`
- `data/uploads/`
- `.env.docker`
- `AUTH_ENCRYPTION_KEY`
- `SECURITY_MASTER_KEY`
- `CONTABASE_SETUP_TOKEN`, enquanto o setup nao foi concluido

## Como restaurar

O restore e uma decisao operacional explicita. Pare a aplicacao e use o pacote
exato informado pelo update:

```bash
docker compose down
CONFIRM_APP_STOPPED=yes \
  DATA_DIR="$PWD/data" \
  DB_FILE="$PWD/data/contabase.db" \
  UPLOADS_DIR="$PWD/data/uploads" \
  ./scripts/ops/restore.sh /caminho/exato/do/pacote/backup-YYYYMMDDTHHMMSSZ-IDENTIFICADOR
docker compose up -d --build
```

O restore substitui banco e uploads e descarta alteracoes posteriores ao backup.
Leia os avisos do script e valide que o pacote pertence a essa instalacao.

## Falha durante update

Antes de o runtime novo iniciar, o atualizador pode reativar somente o runtime
anterior. Depois que o runtime novo tiver sido iniciado, ele pode ter executado
migrations. Nesse caso o atualizador:

- para o runtime novo;
- nao reativa automaticamente o runtime antigo;
- preserva o estado que falhou para diagnostico;
- informa o pacote pre-update e o comando oficial de restore;
- termina com erro para exigir decisao explicita do operador.

Nunca inicie o runtime antigo sobre um banco possivelmente migrado sem avaliar
compatibilidade ou restaurar conscientemente o pacote pre-update.

## Retorno da Beta 8 para a Beta 7

Nao existe downgrade in-place suportado da Beta 8 para a Beta 7 depois que o
banco foi aberto pela Beta 8.

Para retornar:

1. pare a Beta 8;
2. preserve separadamente o estado atual para diagnostico;
3. selecione pelo caminho/ID o pacote pre-update imutavel;
4. execute o `restore.sh` oficial e confirme integridade e foreign keys;
5. ative exatamente o runtime Beta 7;
6. valide o healthcheck e execute um smoke funcional;
7. so entao reabra o acesso.

Essa escolha descarta deliberadamente alteracoes feitas depois do pacote
pre-update. Nunca modifique ou reutilize o pacote como area de trabalho.

## Dicas praticas

- Nunca copie o `.db` enquanto a aplicacao estiver gravando sem usar um metodo seguro.
- Sempre teste o restore em ambiente de teste antes de depender do backup.
- Depois de restaurar, confira se o login e o healthcheck voltaram.
- Se o 2FA mudar de chave, preserve `AUTH_ENCRYPTION_KEY` para evitar perda de acesso.

## Erros comuns

| Problema | O que verificar |
|---|---|
| Anexos somem apos restore | Use o pacote canonico, que inclui `uploads/`, pelo `restore.sh`. |
| 2FA antigo nao funciona apos restore | Confirme se `AUTH_ENCRYPTION_KEY` e o `.env.docker` original foram preservados. |
| Banco volta mas o app nao sobe | Veja `docker compose logs --tail 100 contabase`. |
| Backup nao inclui o que voce esperava | Confirme se voce fez backup operacional, nao apenas exportacao do painel. |

## Veja tambem

- [Configuracao](configuracao.md)
- [Atualizacao](atualizacao.md)
- [Seguranca](seguranca.md)
