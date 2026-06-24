# Configuração

Este guia explica as configurações mais importantes do ContaBase.

Na maioria das instalações, você não precisa editar tudo manualmente.

O instalador já pergunta o essencial e cria os secrets automaticamente.

Use este guia quando precisar conferir ou ajustar alguma configuração.

## Onde fica o arquivo de configuração?

### LXC/VPS

```text
/etc/contabase/contabase.env
```

Para editar:

```bash
sudo nano /etc/contabase/contabase.env
```

Depois de editar, reinicie:

```bash
sudo systemctl restart contabase
```

### Docker

```text
.env.docker
```

Depois de editar, recrie a stack:

```bash
docker compose up -d --build
```

## Configurações mais importantes

| Variável | Explicação simples | Exemplo |
|---|---|---|
| `PORT` | Porta local onde o ContaBase roda | `PORT=8080` |
| `APP_BASE_URL` | URL que você abre no navegador | `APP_BASE_URL=https://financeiro.seudominio.com` |
| `ALLOWED_HOSTS` | Domínios ou IPs aceitos | `ALLOWED_HOSTS=financeiro.seudominio.com,localhost,127.0.0.1` |
| `TRUSTED_PROXIES` | Proxy reverso confiável | `TRUSTED_PROXIES=127.0.0.1,::1` |
| `DATABASE_URL` | Caminho do banco SQLite | `DATABASE_URL=file:/app/data/contabase.db` |
| `DATA_DIR` | Pasta principal de dados | `DATA_DIR=/app/data` |
| `UPLOADS_DIR` | Pasta de uploads | `UPLOADS_DIR=/app/data/uploads` |
| `AUTH_ENCRYPTION_KEY` | Chave secreta de autenticação | gerada pelo instalador |
| `SECURITY_MASTER_KEY` | Chave mestra de segurança | gerada pelo instalador |
| `CONTABASE_SETUP_TOKEN` | Token para criar o primeiro admin | gerado pelo instalador |

## APP_BASE_URL

É a URL principal do ContaBase.

Se você acessa por IP:

```env
APP_BASE_URL=http://192.168.1.50:8080
```

Se você acessa por domínio:

```env
APP_BASE_URL=https://financeiro.seudominio.com
```

Use a mesma URL que você digita no navegador.

## ALLOWED_HOSTS

Lista os domínios ou IPs que podem acessar o ContaBase.

Exemplo com domínio:

```env
ALLOWED_HOSTS=financeiro.seudominio.com,localhost,127.0.0.1
```

Exemplo com IP local:

```env
ALLOWED_HOSTS=192.168.1.50,localhost,127.0.0.1
```

Se o domínio não estiver aqui, o acesso pode ser bloqueado.

## TRUSTED_PROXIES

Use quando o ContaBase fica atrás de proxy reverso.

Exemplos de proxy reverso:

- Nginx Proxy Manager
- Caddy
- Traefik
- Cloudflare Tunnel
- Nginx manual

Se o proxy está no mesmo servidor, normalmente use:

```env
TRUSTED_PROXIES=127.0.0.1,::1
```

Se você não usa proxy reverso, pode deixar vazio:

```env
TRUSTED_PROXIES=
```

## PORT

Porta local do ContaBase.

Padrão:

```env
PORT=8080
```

Em instalação LXC/VPS, se `CONTABASE_PORT` existir, ele tem prioridade sobre `PORT`.

## CONTABASE_SETUP_TOKEN

Esse token serve para criar o primeiro administrador.

Depois de criar o primeiro administrador, remova ou comente esta linha:

```env
CONTABASE_SETUP_TOKEN=...
```

Depois reinicie o ContaBase.

### LXC/VPS

```bash
sudo systemctl restart contabase
```

### Docker

```bash
docker compose up -d --build
```

## Secrets de segurança

Estas variáveis são sensíveis:

```env
AUTH_ENCRYPTION_KEY=
SECURITY_MASTER_KEY=
CONTABASE_SETUP_TOKEN=
```

Regras:

- não compartilhe esses valores;
- não cole esses valores em chats públicos;
- não envie esses valores para Git;
- não troque `AUTH_ENCRYPTION_KEY` sem entender o impacto;
- não troque `SECURITY_MASTER_KEY` sem entender o impacto.

O instalador gera esses valores automaticamente em instalações novas.

## Gerar secrets manualmente

Use apenas se estiver fazendo instalação manual.

```bash
openssl rand -base64 32
openssl rand -hex 16
openssl rand -base64 48
```

Uso sugerido:

| Comando | Variável |
|---|---|
| `openssl rand -base64 32` | `AUTH_ENCRYPTION_KEY` |
| `openssl rand -hex 16` | `SECURITY_MASTER_KEY` |
| `openssl rand -base64 48` | `CONTABASE_SETUP_TOKEN` |

## Exemplo simples com domínio

```env
APP_ENV=production
APP_DEBUG=false
PORT=8080
APP_BASE_URL=https://financeiro.seudominio.com
ALLOWED_HOSTS=financeiro.seudominio.com,localhost,127.0.0.1
TRUSTED_PROXIES=127.0.0.1,::1
```

## Exemplo simples por IP local

```env
APP_ENV=production
APP_DEBUG=false
PORT=8080
APP_BASE_URL=http://192.168.1.50:8080
ALLOWED_HOSTS=192.168.1.50,localhost,127.0.0.1
TRUSTED_PROXIES=
```

## O que fazer depois de mudar configuração?

### LXC/VPS

```bash
sudo systemctl restart contabase
curl http://127.0.0.1:8080/health
```

### Docker

```bash
docker compose up -d --build
curl http://127.0.0.1:8080/health
```

## Veja também

- [Instalação rápida](instalacao.md)
- [Instalação em LXC/VPS](instalacao-lxc-vps.md)
- [Instalação com Docker](instalacao-docker.md)
- [Atualização](atualizacao.md)
- [Backup e restauração](backup-restauracao.md)
- [Segurança](../SECURITY.md)
