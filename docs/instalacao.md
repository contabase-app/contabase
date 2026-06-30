# Instalação rápida

Este é o guia rápido para instalar o ContaBase pelo instalador principal.

A ideia é simples:

1. Escolha a versão.
2. Baixe o instalador.
3. Execute o instalador.
4. Responda as perguntas.
5. Guarde o token de setup.
6. Abra o ContaBase no navegador.

Os detalhes completos ficam nos guias específicos:

- [Instalação em LXC/VPS](instalacao-lxc-vps.md)
- [Instalação com Docker](instalacao-docker.md)
- [Configuração](configuracao.md)
- [Atualização](atualizacao.md)

## Qual opção devo escolher?

| Se você quer... | Escolha no menu |
|---|---:|
| Instalar em VPS/LXC sem Docker | `2` |
| Instalar com Docker Compose | `1` |
| Atualizar instalação VPS/LXC sem Docker | `6` |
| Atualizar instalação Docker | `4` |

Se você não sabe qual escolher:

- use `2` para VPS/LXC sem Docker;
- use `1` se você já usa Docker.

## 1. Use o canal de instalação

O canal recomendado aponta para a versão pública recomendada:

```bash
curl -fsSLo /tmp/contabase-install.sh https://get-contabase.pages.dev/install.sh && bash /tmp/contabase-install.sh
```

O canal beta aponta para a beta pública atual:

```bash
curl -fsSLo /tmp/contabase-install.sh https://get-contabase.pages.dev/beta/install.sh && bash /tmp/contabase-install.sh
```

Para fixar uma versão específica, informe `CONTABASE_VERSION` ao executar o instalador:

```bash
curl -fsSLo /tmp/contabase-install.sh https://get-contabase.pages.dev/install.sh && CONTABASE_VERSION=vX.Y.Z bash /tmp/contabase-install.sh
```

Exemplo: substitua `vX.Y.Z` por uma tag publicada, como `v0.1.0-beta.3`.

Não use `main`.

Não use `latest`.

Use sempre uma versão publicada no GitHub Releases.

## 2. Baixe o instalador

Copie e cole no terminal do servidor:

```bash
curl -fsSLo /tmp/contabase-install.sh https://get-contabase.pages.dev/install.sh
```

## 3. Execute o instalador

Se você está como `root`:

```bash
bash /tmp/contabase-install.sh
```

Se você não está como `root`, use `sudo`:

```bash
sudo bash /tmp/contabase-install.sh
```

## 4. Escolha no menu

O instalador vai mostrar um menu parecido com este:

```text
1) Instalar via Docker Compose
2) Instalar via binário da Release
3) Instalar via código-fonte/systemd
4) Atualizar via Docker Compose
5) Atualizar via binário da Release
6) Atualizar via código-fonte/systemd
7) Sair
```

As opções e a numeração podem variar conforme a versão; o menu inclui uma opção para sair.

Para a maioria dos usuários:

- digite `2` para instalar em VPS/LXC sem Docker;
- digite `1` para instalar com Docker;
- digite `6` para atualizar VPS/LXC;
- digite `4` para atualizar Docker.

## 5. Responda as perguntas

O instalador pode perguntar:

```text
Porta local do ContaBase [8080]:
URL pública do ContaBase [http://servidor:8080]:
Hosts permitidos / ALLOWED_HOSTS [localhost,127.0.0.1,servidor]:
Vai usar reverse proxy como Nginx Proxy Manager, Caddy, Traefik ou Cloudflare Tunnel? [s/N]:
TRUSTED_PROXIES [127.0.0.1,::1]:
```

Para uma instalação simples em rede local, informe o IP privado do servidor em `APP_BASE_URL`, por exemplo `http://192.168.1.50:8080`. Quando você responde que não usará proxy, o instalador grava `CONTABASE_ACCESS_MODE=lan` somente se esse host for IP privado RFC1918. `ALLOWED_HOSTS` precisa conter o IP, mas não libera sozinho HTTP remoto.

Se você tem domínio com HTTPS, use algo assim:

```text
https://financeiro.seudominio.com
```

Se ainda não tem domínio, pode usar o IP do servidor:

```text
http://192.168.1.50:8080
```

## 6. Guarde o token de setup

Em uma instalação nova, o instalador mostra um token parecido com este:

```text
CONTABASE_SETUP_TOKEN=xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

Guarde esse token.

Você vai usar esse token para criar o primeiro usuário administrador.

Depois de criar o primeiro administrador, remova ou comente o token no arquivo de configuração.

## 7. Acesse no navegador

Abra a URL informada na instalação.

Exemplos:

```text
http://192.168.1.50:8080/setup
```

ou:

```text
https://financeiro.seudominio.com/setup
```

Cole o `CONTABASE_SETUP_TOKEN` e crie o primeiro administrador.

## 8. Verifique se está funcionando

Em VPS/LXC sem Docker:

```bash
curl http://127.0.0.1:8080/health
systemctl status contabase
```

Em Docker:

```bash
docker compose ps
curl http://127.0.0.1:8080/health
```

## Instalação sem perguntas

Use este modo somente se você já sabe todos os valores.

Exemplo para VPS/LXC sem Docker:

```bash
curl -fsSLo /tmp/contabase-install.sh https://get-contabase.pages.dev/install.sh

CONTABASE_INSTALL_METHOD=release \
CONTABASE_VERSION=vX.Y.Z \
CONTABASE_ASSUME_YES=1 \
PORT=8080 \
APP_BASE_URL=https://financeiro.exemplo.com \
ALLOWED_HOSTS=financeiro.exemplo.com \
TRUSTED_PROXIES=127.0.0.1,::1 \
CONTABASE_ACCESS_MODE=proxy \
bash /tmp/contabase-install.sh
```

Exemplo LAN privada sem proxy:

```bash
curl -fsSLo /tmp/contabase-install.sh https://get-contabase.pages.dev/install.sh

CONTABASE_INSTALL_METHOD=release \
CONTABASE_VERSION=vX.Y.Z \
CONTABASE_ASSUME_YES=1 \
PORT=8080 \
APP_BASE_URL=http://192.168.1.50:8080 \
ALLOWED_HOSTS=192.168.1.50 \
TRUSTED_PROXIES= \
CONTABASE_ACCESS_MODE=lan \
bash /tmp/contabase-install.sh
```

Para entender cada variável, leia [Configuração](configuracao.md).

## Docker pelo instalador

Para Docker pelo instalador, use um clone completo do repositório:

```bash
export CONTABASE_VERSION=vX.Y.Z

git clone --branch "${CONTABASE_VERSION}" https://github.com/contabase-app/contabase.git
cd contabase
./scripts/install.sh
```

Escolha a opção `1`.

Guia completo:

[Instalação com Docker](instalacao-docker.md)

## Docker manual com GHCR

Use esta opção se você quer copiar um `docker-compose.yml` pronto para:

- Dockge
- Portainer
- CasaOS
- Docker Compose manual

Guia completo:

[Instalação com Docker](instalacao-docker.md)

## VPS/LXC sem Docker

Use esta opção se você quer instalar direto no sistema com `systemd`.

Guia completo:

[Instalação em LXC/VPS](instalacao-lxc-vps.md)

## Atualização

Após instalar, você pode atualizar o ContaBase com um comando simples:

```bash
sudo contabase-update vX.Y.Z
```

O comando detecta automaticamente o modo de instalação (binary, docker ou source)
e chama o script de update correto. Opcionalmente use `sudo cb-update`.

Também é possível usar a nova versão em `CONTABASE_VERSION` e escolher a opção de atualização no menu do instalador.

Guia completo:

[Atualização](atualizacao.md)

## Problemas comuns

### Erro: CONTABASE_VERSION é obrigatório

Use `export`:

```bash
export CONTABASE_VERSION=vX.Y.Z
bash /tmp/contabase-install.sh
```

Não faça apenas isto:

```bash
CONTABASE_VERSION=vX.Y.Z
bash /tmp/contabase-install.sh
```

### Docker não está instalado

Escolha uma destas opções:

1. Autorize o instalador a instalar Docker/Compose.
2. Instale Docker manualmente.
3. Use a instalação VPS/LXC sem Docker.

### Não consigo acessar pelo domínio

Confira:

1. `APP_BASE_URL` está com a URL correta.
2. `ALLOWED_HOSTS` contém seu domínio.
3. Seu proxy reverso aponta para o ContaBase.
4. O HTTPS está funcionando.
5. `CONTABASE_ACCESS_MODE=proxy` para domínio HTTPS por proxy, ou `lan` somente para IP privado RFC1918.
6. O firewall libera o acesso necessário.

Leia [Configuração](configuracao.md) para ajustar essas variáveis.

## Próximos guias

- [Instalação em LXC/VPS](instalacao-lxc-vps.md)
- [Instalação com Docker](instalacao-docker.md)
- [Configuração](configuracao.md)
- [Atualização](atualizacao.md)
- [Backup e restauração](backup-restauracao.md)
- [Segurança](../SECURITY.md)
