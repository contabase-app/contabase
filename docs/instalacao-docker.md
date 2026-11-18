# Instalação com Docker

Este guia mostra como instalar o ContaBase com Docker.

Comece pelo [Guia Completo de Instalação](https://contabase.pages.dev/setup/) para escolher a versão e o método. Este documento preserva os comandos e os detalhes técnicos do Docker.

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
- `flock` (util-linux), obrigatório para a atualização segura da Beta 8;
- porta `8080` livre, ou outra porta escolhida.

Se Docker não estiver instalado e você usa Debian/Ubuntu, o instalador pode perguntar se você deseja instalar as dependências.

**Debian 13 (Trixie):** o pacote `docker-compose-plugin` pode não estar disponível nos repositórios oficiais. Nesse caso, o instalador tenta automaticamente o pacote `docker-compose` (standalone). Se nenhum dos dois estiver disponível, instale o plugin manualmente seguindo a [documentação oficial do Docker](https://docs.docker.com/compose/install/linux/) ou use o binário standalone.

## Caminho 1: instalador guiado com Docker

Este caminho usa o repositório completo e o helper legado da tag. O Bootstrap Beta é um fluxo separado: fixa imagem por digest, exige catálogo homologado por versão/arquitetura e não instala dependências automaticamente. A central [de instalação](https://contabase.pages.dev/setup/) informa seus gates. Compose manual não recebe estado administrativo nem o comando `contabase-update`.

Clone a versão desejada:

```bash
export CONTABASE_VERSION=vX.Y.Z

git clone --branch "${CONTABASE_VERSION}" https://github.com/contabase-app/contabase.git
cd contabase
```

Execute o instalador:

```bash
chmod +x scripts/install.sh scripts/install-contabase-docker.sh scripts/update-contabase-docker.sh
./scripts/install.sh
```

Escolha:

```text
1) Instalar via Docker Compose
```

O instalador verifica:

- Docker;
- Docker Compose;
- `curl`;
- `openssl`;
- `ca-certificates`;
- `python3`.

Se faltar Docker ou Compose em Debian/Ubuntu, ele pergunta se pode instalar.

## Perguntas do fluxo guiado

| Quando | Pergunta literal | Padrão |
|---|---|---|
| Docker/Compose ausentes | `Docker/Compose nao estao instalados. Deseja instalar as dependencias agora? [s/N]:` | Não |
| Sem override, com exemplo disponível | `Deseja criar docker-compose.override.yml para customizacoes? (s/N)` | Não |
| Secret ausente em configuração existente | `Gerar novo valor para ${key}? (s/N)` | Não; `${key}` é o nome da chave, nunca seu valor |
| Seleção de perfil | `Opcao (1/2/3/4):` | Sem padrão indicado; vazio ou inválido usa defaults locais |
| Perfil 2 | `IP privado LAN:` | Sem padrão; IP privado obrigatório |
| Perfil 3 | `Dominio HTTPS (ex: app.seudominio.com):` | Sem padrão; vazio exige configuração manual |
| Perfil 3 | `TRUSTED_PROXIES (pode deixar vazio para configurar manualmente):` | Vazio; configure antes de expor |

O menu de perfil é:

```text
Selecione o perfil de instalacao:
1) Local Docker nesta maquina (http://localhost)
2) Rede local/LAN por IP privado
3) Domínio com HTTPS via proxy/tunnel/CDN
4) Avançado / manual
Opcao (1/2/3/4):
```

O fluxo não pergunta porta, URL completa nem confirmação final. O perfil local usa `8080`; os perfis LAN/proxy derivam URL e hosts do IP/domínio. Secrets existentes válidos são preservados; a escolha do perfil regrava as variáveis de acesso no `.env.docker`. Para portas, redes e exposição, revise `.env.docker` e `docker-compose.override.yml`; os perfis `3` e `4` criam o override quando necessário, mesmo após recusar a criação inicial. Não há a pergunta “Vai usar reverse proxy?”.

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
4. Restringe `.env.docker` a `0600` e `data/` a `0700`, com arquivos regulares `0600` e ownership dos dados `1000:1000`.
5. Valida o `docker-compose.yml`.
6. Sobe os containers.
7. Consulta o estado do container: saudável, inicializando ou falha. Só `healthy` confirma a saúde; `starting` pede espera e não anuncia aplicação operacional.
8. Indica onde está o token de setup.

O token de setup fica em:

```text
.env.docker
```

O valor não é impresso pelo instalador. Consulte-o localmente com `sudo nano .env.docker` (arquivo `0600`), procurando a chave:

```text
CONTABASE_SETUP_TOKEN=
```

Use esse token em `/setup`.
Não envie o valor em logs ou mensagens. Se o container estiver inicializando, acompanhe `docker compose ps --all`; em falha, consulte `docker compose logs --tail=80 contabase`. A consulta final é um retrato do estado, sem espera adicional nem recuperação automática.

Depois de criar o primeiro administrador, remova ou comente essa linha e recrie a stack:

```bash
docker compose up -d --force-recreate
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
sudo contabase-update vX.Y.Z
```

O comando detecta o modo Docker automaticamente e chama o script de update
no diretório onde o ContaBase foi instalado.

Opcional: `sudo contabase-update --yes vX.Y.Z` ou `sudo contabase-update --dry-run vX.Y.Z`

Na Beta 8, o dispatcher autentica canal, Release, identidade da tag/commit e manifesto, verificando o bundle antes da delegação. O dispatcher Beta 7 executa o helper local e repassa a versão, sem essa autenticação completa. Trocar o checkout não troca o comando global instalado; veja a transição abaixo e os [contratos dos dispatchers](atualizacao.md#contratos-dos-dispatchers-beta-7-e-beta-8).

A Beta 9 mantém o mesmo dispatcher autenticado e não aplica migration nova. Para atualizar da Beta 8, informe `v1.0.0-beta.9` somente depois que a versão estiver publicada e homologada no catálogo do canal; até lá o comando falha fechado. A transição por tag descrita abaixo é específica da Beta 7 e não é necessária para instalações que já usam o dispatcher autenticado.

### Fallback legado sem comando global

Entre na pasta do projeto:

```bash
cd contabase
```

Execute:

```bash
CONTABASE_VERSION=vX.Y.Z \
CONTABASE_INSTALL_METHOD=update-docker \
CONTABASE_ASSUME_YES=1 \
./scripts/install.sh
```

Ou chame o updater direto:

```bash
CONTABASE_VERSION=vX.Y.Z ./scripts/update-contabase-docker.sh
```

Use a versão publicada desejada. A variável é obrigatória em checkout por tag e, em branch, deve corresponder ao `VERSION` do alvo. Esse fallback não equivale à autenticação do bundle antes da execução feita pelo dispatcher Beta 8. O helper Beta 7 continua incompatível com HEAD destacado, mesmo com a variável; nesse caso, siga a transição abaixo.

O updater preserva:

- `.env.docker`;
- banco;
- uploads;
- backups;
- volumes;
- secrets.

### Transição única de instalação criada por tag (beta.7 → beta.8)

Execute somente após a disponibilização oficial da Beta 8 para esse método e da tag `v1.0.0-beta.8`. A documentação não comprova publicação nem promoção do canal. Este procedimento é histórico e se aplica apenas a checkouts criados pela tag da Beta 7; instalações que já usam o dispatcher autenticado atualizam pela versão fixa publicada.

Se você instalou com `git clone --branch v1.0.0-beta.7`, o checkout está com
HEAD destacado. O updater da beta.7 recusa esse estado e o `contabase-update`
da beta.7 executa o updater do próprio checkout; por isso nenhum dos dois
atualiza essa instalação enquanto ela estiver na beta.7. Faça a transição uma
única vez, sem `--force`, depois de cumprir todos os pré-requisitos:

- Origem oficial, worktree limpo e diretório igual ao registrado pela instalação.
- Estado legado válido em `/etc/contabase/install-state.env`, com método `docker` e versão instalada Beta 7. Não apague nem edite o estado para forçar a transição.
- Container Beta 7 em execução e saudável: Docker health `healthy`, `/health` com `{"status":"healthy"}` e `/app/VERSION` igual a `v1.0.0-beta.7`. A versão efetiva vem do runtime, não do checkout.
- [Backup adequado](backup-restauracao.md) do banco, uploads, configuração e chaves, com restauração conhecida, antes de atualizar.
- Dispatcher Beta 7 instalado, `sudo`, Docker/Compose, `flock` (util-linux) e dependências do helper disponíveis.

Faça as [conferências de origem, estado e runtime antes do checkout](atualizacao.md#transição-única-de-checkout-por-tag-beta7--beta8). Se a identidade ou a saúde do runtime anterior não puder ser comprovada, a atualização permanece bloqueada.

```bash
cd /caminho/do/contabase

# Precisa estar vazio. Se houver alterações locais, pare e resolva antes.
git status --porcelain

git fetch origin tag v1.0.0-beta.8
git checkout v1.0.0-beta.8

sudo contabase-update v1.0.0-beta.8
```

O `git checkout` move o checkout para a tag nova e troca os scripts locais
pelos da Beta 8: o `VERSION` local passa a Beta 8, enquanto o runtime e o estado instalado ainda representam Beta 7. A comprovação do runtime Beta 7 saudável é necessária para o helper reconhecer essa transição; sem ela, pode recusar a divergência entre estado e checkout.

O comando global ainda é o **dispatcher Beta 7**. Ele detecta o modo Docker no estado e executa o helper Beta 8 do checkout com `CONTABASE_VERSION=v1.0.0-beta.8`, exigida no modo tag. Esse primeiro passo segue o contrato legado e **não autentica completamente o bundle antes da execução**. A validação posterior dentro do helper não equivale à autenticação antecipada do dispatcher Beta 8.

O helper valida a tag na
origem oficial, compara a tag local com a remota, reconhece HEAD destacado em
tag oficial, cria o pacote canônico antes de alterar o runtime, atualiza a
imagem, valida o healthcheck, confirma o checkout na tag e reinstala o
`contabase-update` da versão nova. Ele preserva banco, uploads, `.env.docker`
e volumes. Se o healthcheck falhar depois do novo runtime iniciar, ele não
reativa a imagem antiga automaticamente; o serviço para e o restore exige
decisão explícita.

Execução não interativa: `sudo contabase-update --yes v1.0.0-beta.8`.

Não use `git checkout --force`, `git reset --hard` ou `git clean` para
contornar alterações locais. Não chame `./scripts/update-contabase-docker.sh`
direto neste cenário: em checkout por tag ele falha sem `CONTABASE_VERSION`
explícita e, executado por conta própria, não equivale à autenticação de
canal, Release, commit e manifesto feita pelo **dispatcher Beta 8**. Invocar o dispatcher Beta 7 também não acrescenta essa autenticação ao helper local. Não troque este
fluxo por `docker compose pull && docker compose up -d`: esse atalho não cria
o backup canônico, não valida a identidade do runtime e não executa o
rollback do updater.

Se preferir manter uma branch local em vez de HEAD destacado, cumpra os mesmos pré-requisitos e confirme que `origin/beta` aponta exatamente para a tag Beta 8 desejada; uma branch pode avançar depois da publicação. Se divergir, use o procedimento por tag, sem forçar o checkout:

```bash
git fetch origin beta
git fetch origin tag v1.0.0-beta.8
# Os dois commits abaixo precisam ser idênticos antes de continuar.
git rev-parse 'origin/beta^{commit}' 'v1.0.0-beta.8^{commit}'
git switch -c beta --track origin/beta
# se a branch beta local já existir:
# git switch beta && git merge --ff-only origin/beta
sudo contabase-update v1.0.0-beta.8
```

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
      - "127.0.0.1:${PORT:-8080}:8080"
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
CONTABASE_TIMEZONE=America/Sao_Paulo

APP_BASE_URL=http://localhost:8080
ALLOWED_HOSTS=localhost,127.0.0.1,::1
TRUSTED_PROXIES=
CONTABASE_ACCESS_MODE=local-docker

CONTABASE_FEATURE_INVESTMENTS=false
CONTABASE_SETUP_TOKEN=troque-por-um-token-aleatorio-min-32-caracteres
AUTH_ENCRYPTION_KEY=troque-por-uma-chave-base64-com-32-bytes
SECURITY_MASTER_KEY=troque-por-uma-chave-hex-32-caracteres

DATA_DIR=/app/data
DB_FILE=/app/data/contabase.db
UPLOADS_DIR=/app/data/uploads
```

Se você opera no Brasil, pode manter `CONTABASE_TIMEZONE=America/Sao_Paulo`. Só altere se o seu fuso financeiro for diferente.

`CONTABASE_FEATURE_INVESTMENTS=false` é o valor usado pelo instalador em instalações novas: o módulo de Investimentos nasce desativado e pode ser ativado depois. A flag é lida no startup e mudanças exigem recriar o serviço; veja [Investimentos](investimentos.md).

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

Depois remova ou comente o token no `.env.docker` e recrie a stack por imagem GHCR (sem `--build`):

```bash
docker compose up -d
```

## Usar domínio com Docker

Se você vai usar domínio, ajuste no `.env.docker`.

Exemplo:

```env
APP_BASE_URL=https://financeiro.seudominio.com
ALLOWED_HOSTS=financeiro.seudominio.com
TRUSTED_PROXIES=127.0.0.1,::1
CONTABASE_ACCESS_MODE=proxy
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

Docker em produção com domínio deve usar `proxy`, não `local-docker`. Mantenha a porta publicada no loopback ou em rede privada do proxy.

## Usar IP privado na LAN

Para abrir por HTTP a partir de outro dispositivo da rede privada, use um IP privado do servidor. `Host: localhost` vindo de outra máquina continua bloqueado.

```env
APP_BASE_URL=http://192.168.1.50:8080
ALLOWED_HOSTS=192.168.1.50
TRUSTED_PROXIES=
CONTABASE_ACCESS_MODE=lan
```

Não use modo `lan` com IP público ou domínio público em HTTP.

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

O container já possui `HEALTHCHECK` integrado, que utiliza o endpoint `/health`. O healthcheck verifica se o processo HTTP está respondendo e se o acesso básico ao SQLite está funcionando. O status `healthy` indica que o servidor e o banco estão operacionais, mas não representa uma auditoria completa dos dados financeiros.

Um container marcado como `unhealthy` não é reiniciado automaticamente apenas pelo Docker healthcheck. Para consultar o estado detalhado do healthcheck:

```bash
docker inspect \
  --format='{{json .State.Health}}' \
  contabase
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

Confira `ALLOWED_HOSTS` e `CONTABASE_ACCESS_MODE` no `.env.docker`.

### Domínio não funciona

Confira:

1. `APP_BASE_URL`;
2. `ALLOWED_HOSTS`;
3. `TRUSTED_PROXIES`;
4. `CONTABASE_ACCESS_MODE=proxy`;
5. proxy reverso;
6. DNS;
7. HTTPS.

## Leia também

- [Instalação rápida](instalacao.md)
- [Instalação em LXC/VPS](instalacao-lxc-vps.md)
- [Configuração](configuracao.md)
- [Atualização](atualizacao.md)
- [Backup e restauração](backup-restauracao.md)
- [Solução de problemas](solucao-de-problemas.md)
