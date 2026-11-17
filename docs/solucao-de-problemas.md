# ContaBase - Troubleshooting

## Servico nao sobe

Verifique:

```bash
docker compose ps
docker compose logs --tail 100 contabase
```

Se a causa for configuracao, revise `APP_ENV`, `AUTH_ENCRYPTION_KEY`, `APP_BASE_URL`, `ALLOWED_HOSTS` e `CONTABASE_ACCESS_MODE`.

## Healthcheck falha

Confirme se o container esta no ar e se o banco esta acessivel:

```bash
curl -i http://localhost:8080/health
docker compose logs --tail 100 contabase
```

Possiveis respostas do endpoint `/health`:

| Resultado        | Significado                                          |
| ---------------- | ---------------------------------------------------- |
| 200 `healthy`    | HTTP e SQLite básico disponíveis                     |
| 503 `unhealthy`  | processo responde, mas o banco não está acessível    |
| conexão recusada | servidor não está escutando ou container está parado |
| 426              | acesso HTTP remoto recusado; usar HTTPS canônico     |
| 405              | método incorreto; usar GET                           |

O endpoint `/health` nao diagnostica migrations específicas, jobs, backup ou integridade financeira. Ele valida apenas a disponibilidade do servidor HTTP e do banco de dados.

Se o app usa proxy, confira tambem `TRUSTED_PROXIES` e `CONTABASE_ACCESS_MODE=proxy`.

Se o app roda em Docker local e `http://localhost:8080` mostra bloqueio remoto, use `CONTABASE_ACCESS_MODE=local-docker`, `APP_BASE_URL=http://localhost:8080` e `ALLOWED_HOSTS=localhost,127.0.0.1,::1`.

## Monitoramento com Uptime Kuma

### Monitor interno

Para instalar o Uptime Kuma no mesmo servidor ou na mesma rede Docker, configure um monitor:

```text
Tipo: HTTP(s)
Método: GET
URL: endereço privado alcançável pelo Uptime Kuma
Status esperado: 200
Palavra-chave: "status":"healthy"
Intervalo: 60 segundos
Timeout: 5 segundos
Tentativas: 3
Intervalo entre tentativas: 20 segundos
Autenticação: nenhuma
```

Se o Uptime Kuma estiver em outro container, não use `127.0.0.1`. Use o nome do serviço na rede Docker apropriada, o IP privado do servidor ou o proxy interno.

Exemplo de URL para containers na mesma rede Docker:

```text
http://contabase:8080/health
```

### Monitor externo

Para instalações com domínio, aponte para a URL HTTPS canônica:

```text
https://dominio-configurado/health
```

Configuração recomendada:

- Método: GET;
- Status esperado: 200;
- Palavra-chave: `"status":"healthy"`;
- Verificação de certificado TLS ativada;
- Não depender de redirecionamento HTTP → HTTPS; use a URL HTTPS final diretamente.

O monitor externo valida também DNS, proxy reverso, TLS, certificado e o alcance completo da aplicação.

## Imagem ou upload nao aparece

- Confirme se os arquivos existem em `data/uploads/`.
- Verifique se o restore copiou os uploads.
- Confira se a permissao do diretorio esta correta.

## Loop entre login, setup e inicio apos restore

Sintoma: depois de restaurar um backup legado, o login parece concluir, mas a pagina inicial volta para `/setup` ou `/login`, criando um ciclo.

Em versoes afetadas, o servidor podia manter a conexao SQLite anterior a restauracao e interpretar a falha de consulta como banco sem usuarios. A Beta 8 corrige esse fluxo: a pagina inicial e as rotas de transacoes usam a conexao atual, falha de leitura na pagina inicial responde 503 e o primeiro setup continua funcionando.

Recuperacao operacional temporaria em instalacao afetada:

```bash
docker compose restart contabase
docker compose logs --tail 100 contabase
```

Em instalacao systemd, reinicie a unidade e confira o estado:

```bash
sudo systemctl restart contabase
sudo systemctl status contabase --no-pager
```

Reiniciar nao e a solucao definitiva: atualize para a versao corrigida assim que ela estiver disponivel. Se o ciclo continuar, preserve os logs, confirme a integridade do backup e revise [Backup e Restore](backup-restauracao.md). Nao repita o restore nem remova `data/` sem autorizacao.

## Alterei o env e nada mudou

Depois de mudar `.env.docker`, recrie a stack:

```bash
docker compose up -d --build
```

## Docker nao atualiza

- Use o comando global `contabase-update` com uma versao publicada. **O dispatcher Beta 8** autentica canal, Release, tag, commit e manifesto e verifica o bundle antes de executar o updater; o updater cria o backup canonico antes de alterar o runtime, valida a identidade da imagem e executa o healthcheck.
- **O dispatcher Beta 7**, no modo Docker, executa o helper presente no checkout e repassa a versao explicita em `CONTABASE_VERSION`, sem autenticar completamente o bundle antes da execucao. O checkout da tag Beta 8 nao substitui o comando global instalado; ele so e reinstalado ao final de uma atualizacao bem-sucedida. A validacao da identidade da tag dentro do helper ja iniciado nao equivale a autenticacao antecipada do dispatcher Beta 8.
- Nao use `git pull` seguido de `docker compose up -d --build` como substituto do updater: esse atalho nao cria o backup canonico e nao valida a identidade do runtime.

```bash
git status --short
sudo contabase-update vX.Y.Z
docker compose logs --tail 100 contabase
```

- Pre-requisitos: worktree limpo, estado da instalacao em `/etc/contabase/install-state.env` e `flock` (util-linux). O comando fica em `/usr/local/bin/contabase-update`, instalado pelo instalador e pelos updaters; em instalacoes antigas sem ele, use o fallback unico descrito em [Atualizacao](atualizacao.md).
- Instalacoes criadas por tag na Beta 7 precisam primeiro da [transicao unica de checkout](atualizacao.md#transição-única-de-checkout-por-tag-beta7--beta8), condicionada a disponibilizacao oficial da versao. Exija origem oficial, worktree limpo, estado legado valido, backup adequado e runtime Beta 7 em execucao, com Docker health `healthy`, `/health` saudavel e `/app/VERSION` igual a `v1.0.0-beta.7`. Depois do checkout, o `VERSION` local passa a Beta 8 enquanto o runtime ainda e Beta 7. Sem comprovar a identidade e a saude anteriores, a atualizacao permanece bloqueada; preserve o estado e os dados.
- `scripts/update-contabase-docker.sh` e o helper interno do checkout. Chamado direto, ele nao faz a validacao de canal, Release, commit autorizado e manifesto do dispatcher Beta 8 e, em checkout por tag, exige `CONTABASE_VERSION` explicita. Nem a chamada direta nem o dispatcher Beta 7 equivalem ao dispatcher autenticado Beta 8.
- Para escolher o metodo e verificar a disponibilidade da versao, consulte o [Guia Completo de Instalacao](https://contabase.pages.dev/setup/).

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
