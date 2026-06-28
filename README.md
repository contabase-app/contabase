<div align="center">
  <h1>ContaBase</h1>
  <p>Base financeira privada, self-hosted e pensada para quem quer manter os dados sob controle.</p>
  <p><strong>Beta pública controlada:</strong> use o canal de instalação para receber a versão recomendada.</p>
</div>

---

## O que é

O ContaBase é um sistema financeiro de código aberto (AGPL-3.0) para uso próprio, familiar ou pequeno negócio.
Ele roda no seu servidor, guarda os dados em SQLite e não depende de uma nuvem externa para funcionar.

Não é SaaS, não possui telemetria e não envia seus dados financeiros para terceiros.

## Para quem é

- Quer controlar finanças sem depender de serviços externos.
- Sabe usar Docker ou consegue rodar um Linux básico (VPS/LXC).
- Aceita que esta é uma **Beta** e que backup/restore são responsabilidade do operador.

## Instalação rápida com o instalador guiado

O canal público temporário de instalação é `https://get-contabase.pages.dev`.
Quando o domínio final `https://get.contabase.net` estiver ativo, ele substituirá o endpoint temporário na documentação.

Baixe o instalador pelo canal recomendado e execute:

```bash
curl -fsSLo /tmp/contabase-install.sh https://get-contabase.pages.dev/install.sh && bash /tmp/contabase-install.sh
```

O script abre um **menu interativo** com todas as opções (instalar via Docker, instalar em LXC/VPS, atualizar, etc.) e chama automaticamente o script correto por baixo. Quando necessário, ele pergunta versão, porta, URL pública, hosts permitidos e proxy. No método Docker, quando executado dentro de um checkout completo, ele valida dependências e pode instalar Docker/Compose em Debian/Ubuntu se você autorizar.

Para fixar uma versão específica:

```bash
curl -fsSLo /tmp/contabase-install.sh https://get-contabase.pages.dev/install.sh && CONTABASE_VERSION=vX.Y.Z bash /tmp/contabase-install.sh
```

Exemplo: substitua `vX.Y.Z` por uma tag publicada, como `v0.1.0-beta.2`.

### Modo não interativo

Defina `CONTABASE_INSTALL_METHOD` para pular o menu:

```bash
# Instalar em LXC/VPS (binário pronto) — funciona sem clone
curl -fsSLo /tmp/contabase-install.sh https://get-contabase.pages.dev/install.sh

CONTABASE_INSTALL_METHOD=release \
CONTABASE_VERSION=vX.Y.Z \
CONTABASE_ASSUME_YES=1 \
PORT=8080 \
APP_BASE_URL=https://financeiro.exemplo.com \
ALLOWED_HOSTS=financeiro.exemplo.com,localhost,127.0.0.1 \
TRUSTED_PROXIES=127.0.0.1,::1 \
bash /tmp/contabase-install.sh
```

Para Docker headless em Debian/Ubuntu com instalação assistida de dependências, use um checkout completo:

```bash
git clone --branch vX.Y.Z https://github.com/contabase-app/contabase.git
cd contabase

CONTABASE_INSTALL_METHOD=docker \
CONTABASE_ASSUME_YES=1 \
CONTABASE_INSTALL_DEPS=1 \
./scripts/install.sh
```

```bash
# Atualizar instalação LXC/VPS existente
CONTABASE_INSTALL_METHOD=update-release \
CONTABASE_VERSION=vX.Y.Z \
CONTABASE_ASSUME_YES=1 \
bash /tmp/contabase-install.sh
```

Os métodos `docker`, `source`, `update-docker` e `update-source` exigem um clone completo do repositório. Clone primeiro e execute `./scripts/install.sh` de dentro do checkout.

### Caminhos disponíveis

| Método | O que faz | Precisa de clone? |
|--------|-----------|-------------------|
| `release` | Instalar em LXC/VPS com binário da GitHub Release | Não |
| `update-release` | Atualizar instalação LXC/VPS existente | Não |
| `docker` | Instalar com Docker Compose | Sim |
| `update-docker` | Atualizar instalação Docker existente | Sim |
| `source` | Build local (Go/Node) | Sim |
| `update-source` | Atualizar build local | Sim |

Se não tem certeza de qual método usar, leia [Primeiros passos](docs/primeiros-passos.md).

### Atualização rápida

Após instalar, use o comando global registrado no sistema:

```bash
sudo contabase-update vX.Y.Z
```

O comando detecta automaticamente o modo de instalação (binary, docker ou source)
e chama o script de update correto. Também funciona como `sudo cb-update`.

→ [Guia completo de atualização](docs/atualizacao.md)

→ [Guia completo de instalação](docs/instalacao.md)

### Docker manual sem clonar o repositório

Se você já usa Docker, Dockge, Portainer ou CasaOS e quer subir o ContaBase direto pela imagem GHCR, veja o passo a passo copiável em [`docs/instalacao-docker.md`](docs/instalacao-docker.md).

## Documentação principal

- [Primeiros passos](docs/primeiros-passos.md)
- [Instalação](docs/instalacao.md)
- [Instalação com Docker](docs/instalacao-docker.md)
- [Instalação em LXC/VPS](docs/instalacao-lxc-vps.md)
- [Backup e restauração](docs/backup-restauracao.md)
- [Atualização](docs/atualizacao.md)
- [Segurança](SECURITY.md)
- [Privacidade](PRIVACY.md)
- [Termos de uso](TERMS.md)
- [Release notes da beta atual](docs/releases/v0.1.0-beta.2.md)
- [Histórico da primeira beta](docs/releases/v0.1.0-beta.1.md)

## Avisos da Beta

A Beta pública não é uma versão stable. Ainda não existe release stable pública do ContaBase.

- A primeira beta permanece disponível apenas como histórico.
- Os instaladores guiados (Docker e LXC/VPS) geram secrets fortes automaticamente e os updaters preservam configuração, banco, uploads e backups.
- A imagem Docker no GHCR e os artifacts da nova beta devem ser validados antes de atualizar o canal público.
- O operador é responsável por backup, restore, HTTPS, proxy, domínio e segredos.

Antes de usar dados importantes:

1. Faça backup.
2. Teste o restore.
3. Valide o healthcheck.
4. Configure proxy, domínio e HTTPS.
5. Remova o token de setup após o primeiro acesso.

## Licença

Distribuído sob AGPL-3.0. Leia [LICENSE](LICENSE) para os termos completos.
