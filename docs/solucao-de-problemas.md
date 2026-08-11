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
