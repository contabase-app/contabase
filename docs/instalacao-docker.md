# Instalação com Docker

Este guia mostra como instalar o ContaBase com Docker.

Existem dois caminhos:

| Caminho | Quando usar |
|---|---|
| Instalador guiado com clone | Para quem quer que o script configure tudo |
| Docker manual com GHCR | Para Dockge, Portainer, CasaOS ou Compose manual |

Se você quer o guia rápido, leia:

[Instalação rápida](instalacao.md)

## Antes de começar

Você precisa de:

- Linux com Docker;
- Docker e Docker Compose (`docker compose` ou `docker-compose` standalone).
- Git, se usar o instalador guiado;
- porta `8080` livre, ou outra porta escolhida.

Se Docker não estiver instalado e você usa Debian/Ubuntu, o instalador pode perguntar se você deseja instalar as dependências.

**Debian 13 (Trixie):** o pacote `docker-compose-plugin` pode não estar disponível nos repositórios oficiais. Nesse caso, o instalador tenta automaticamente o pacote `docker-compose` (standalone). Se nenhum dos dois estiver disponível, instale o plugin manualmente seguindo a [documentação oficial do Docker](https://docs.docker.com/compose/install/linux/) ou use o binário standalone.

## Caminho 1: instalador guiado com Docker

Este caminho usa o repositório completo.

Clone a versão desejada:

```bash
export CONTABASE_VERSION=vX.Y.Z

git clone --branch "${CONTABASE_VERSION}" https://github.com/contabase-app/contabase.git
cd contabase
```

Execute o instalador:

```bash
./scripts/install.sh
```

Escolha:

```text
1) Instalar com Docker Compose
```

O instalador verifica:

- Docker;
- Docker Compose;
- `curl`;
- `openssl`;
- `ca-certificates`;
- `python3`.

Se faltar Docker ou Compose em Debian/Ubuntu, ele pergunta se pode instalar.

## Caminho 2: instalador Docker sem perguntas

Dentro da pasta clonada:

```bash
CONTABASE_INSTALL_METHOD=docker \
CONTABASE_ASSUME_YES=1 \
./scripts/install.sh
```

Se você também quer autorizar instalação de dependências ausentes:

```bash
CONTABASE_INSTALL_METHOD=docker \
CONTABASE_ASSUME_YES=1 \
CONTABASE_INSTALL_DEPS=1 \
./scripts/install.sh
```

Atenção: `CONTABASE_INSTALL_DEPS=1` autoriza o script a instalar pacotes no sistema.

Não use se você não quer que o instalador mexa nas dependências do servidor.

## O que o instalador Docker faz

Ele:

1. Confere as dependências.
2. Cria `.env.docker` se não existir.
3. Gera secrets fortes.
4. Ajusta permissões.
5. Valida o `docker-compose.yml`.
6. Sobe os containers.
7. Mostra a URL de acesso.
8. Indica onde está o token de setup.

O token de setup fica em:

```text
.env.docker
```

Procure:

```text
CONTABASE_SETUP_TOKEN=
```

Use esse token em `/setup`.

Depois de criar o primeiro administrador, remova ou comente essa linha e recrie a stack:

```bash
docker compose up -d --build
```

## Apenas validar

Dentro da pasta clonada:

```bash
./scripts/install-contabase-docker.sh --check
```

Esse comando valida pré-requisitos e Compose. Ele não sobe containers.

## Atualizar Docker

Após instalar, use o comando global:

```bash
contabase-update
```

O comando detecta o modo Docker automaticamente e chama o script de update
no diretório onde o ContaBase foi instalado.

Opcional: `contabase-update --yes` ou `contabase-update --dry-run`

### Atualização manual

Entre na pasta do projeto:

```bash
cd contabase
```

Execute:

```bash
CONTABASE_INSTALL_METHOD=update-docker \
CONTABASE_ASSUME_YES=1 \
./scripts/install.sh
```

Ou chame o updater direto:

```bash
./scripts/update-contabase-docker.sh
```

O updater preserva:

- `.env.docker`;
- banco;
- uploads;
- backups;
- volumes;
- secrets.

## Docker manual com GHCR

Use este caminho se você quer copiar um compose pronto em ferramentas como:

- Dockge
- Portainer
- CasaOS
- Docker Compose manual

Neste caminho, você não precisa clonar o repositório.

### 1. Crie uma pasta

```bash
mkdir -p ~/contabase
cd ~/contabase
```

### 2. Crie `docker-compose.yml`

Copie este conteúdo:

```yaml
services:
  contabase:
    image: ghcr.io/contabase-app/contabase:vX.Y.Z
    container_name: contabase
    restart: unless-stopped
    env_file:
      - .env.docker
    ports:
      - "${PORT:-8080}:8080"
    volumes:
      - ./data:/app/data
      - ./uploads:/app/uploads
      - ./backups:/app/backups
```

### 3. Crie `.env.docker`

Copie este conteúdo e troque os secrets:

```env
APP_ENV=production
APP_DEBUG=false
PORT=8080
DATABASE_URL=file:/app/data/contabase.db

APP_BASE_URL=http://localhost:8080
ALLOWED_HOSTS=localhost,127.0.0.1
TRUSTED_PROXIES=

CONTABASE_SETUP_TOKEN=troque-por-um-token-aleatorio-min-32-caracteres
AUTH_ENCRYPTION_KEY=troque-por-uma-chave-base64-com-32-bytes
SECURITY_MASTER_KEY=troque-por-uma-chave-hex-32-caracteres

DATA_DIR=/app/data
DB_FILE=/app/data/contabase.db
UPLOADS_DIR=/app/data/uploads
```

Não use esses valores de exemplo em produção.

Gere secrets fortes com Python:

```bash
python3 - <<'PY'
import secrets
import base64

print("CONTABASE_SETUP_TOKEN=" + secrets.token_hex(16))
print("AUTH_ENCRYPTION_KEY=" + base64.b64encode(secrets.token_bytes(32)).decode())
print("SECURITY_MASTER_KEY=" + secrets.token_hex(16))
PY
```

Copie os valores gerados para `.env.docker`.

Não compartilhe esses valores.

Não coloque `.env.docker` no Git.

### 4. Suba o container

O container roda como usuário não-root (UID 1000), então crie as pastas de dados e dê permissão de escrita a esse usuário antes de subir:

```bash
mkdir -p data uploads backups
chown -R 1000:1000 data uploads backups
docker compose pull
docker compose up -d
docker compose ps
```

### 5. Acesse

Abra:

```text
http://localhost:8080/setup
```

ou a URL definida em `APP_BASE_URL`.

Use o `CONTABASE_SETUP_TOKEN` para criar o primeiro administrador.

Depois remova ou comente o token no `.env.docker` e recrie a stack:

```bash
docker compose up -d
```

## Usar domínio com Docker

Se você vai usar domínio, ajuste no `.env.docker`.

Exemplo:

```env
APP_BASE_URL=https://financeiro.seudominio.com
ALLOWED_HOSTS=financeiro.seudominio.com,localhost,127.0.0.1
TRUSTED_PROXIES=127.0.0.1,::1
```

Seu proxy reverso deve apontar para:

```text
http://IP_DO_SERVIDOR:8080
```

ou:

```text
http://127.0.0.1:8080
```

se o proxy estiver no mesmo host.

O ContaBase não configura o proxy automaticamente.

## Onde ficam os dados

| Caminho | Uso |
|---|---|
| `.env.docker` | configuração e secrets |
| `./data` | banco e dados |
| `./uploads` | uploads |
| `./backups` | backups |

Não apague essas pastas.

## Verificar se está funcionando

Containers:

```bash
docker compose ps
```

Healthcheck:

```bash
curl http://127.0.0.1:8080/health
```

Logs:

```bash
docker compose logs -f contabase
```

Parar:

```bash
docker compose down
```

## Versionamento da imagem

Use tag fixa:

```text
ghcr.io/contabase-app/contabase:vX.Y.Z
```

A tag `beta` pode mudar.

A tag `latest` não é publicada nesta fase de pré-release.

## Problemas comuns

### Docker não está instalado

Use o instalador guiado em Debian/Ubuntu e autorize a instalação das dependências, ou instale Docker manualmente.

### Healthcheck não responde

Veja os logs:

```bash
docker compose logs --tail 100 contabase
```

### Erro de host

Confira `ALLOWED_HOSTS` no `.env.docker`.

### Domínio não funciona

Confira:

1. `APP_BASE_URL`;
2. `ALLOWED_HOSTS`;
3. `TRUSTED_PROXIES`;
4. proxy reverso;
5. DNS;
6. HTTPS.

## Leia também

- [Instalação rápida](instalacao.md)
- [Instalação em LXC/VPS](instalacao-lxc-vps.md)
- [Configuração](configuracao.md)
- [Atualização](atualizacao.md)
- [Backup e restauração](backup-restauracao.md)
- [Solução de problemas](solucao-de-problemas.md)
