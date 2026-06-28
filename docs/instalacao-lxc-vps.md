# Instalação em LXC/VPS

Este guia é para instalar o ContaBase em uma VPS ou LXC sem Docker.

Use este método se você quer:

- instalar direto no sistema;
- usar `systemd`;
- não instalar Go;
- não instalar Node.js;
- não usar Docker.

Se você quer apenas o guia rápido, leia primeiro:

[Instalação rápida](instalacao.md)

## O que o instalador faz

O instalador:

1. Baixa o binário pronto da GitHub Release.
2. Valida o checksum.
3. Cria o usuário `contabase`.
4. Cria as pastas do sistema.
5. Cria o arquivo de configuração.
6. Gera secrets fortes.
7. Pergunta porta, URL pública, hosts e proxy.
8. Cria o serviço `systemd`.
9. Inicia o ContaBase.
10. Testa o `/health`.

## Antes de começar

Você precisa de:

- Debian ou Ubuntu;
- acesso `root` ou `sudo`;
- internet no servidor;
- `systemd`;
- porta `8080` livre, ou outra porta que você escolher.

O servidor precisa ter ferramentas básicas como `curl`, `tar` e `sha256sum`.

## Instalação pelo menu guiado

Baixe o instalador:

```bash
curl -fsSLo /tmp/contabase-install.sh https://get-contabase.pages.dev/install.sh
```

Execute:

```bash
bash /tmp/contabase-install.sh
```

Se não estiver como `root`:

```bash
sudo bash /tmp/contabase-install.sh
```

Escolha a opção:

```text
2) Instalar via binário da Release
```

## Perguntas feitas pelo instalador

O instalador pode perguntar:

```text
Porta local do ContaBase [8080]:
```

Use `8080`, a menos que essa porta já esteja ocupada.

Depois:

```text
URL pública do ContaBase [http://servidor:8080]:
```

Se você vai acessar por IP local:

```text
http://192.168.1.50:8080
```

Se você vai acessar por domínio:

```text
https://financeiro.seudominio.com
```

Depois:

```text
Domínios/IPs permitidos para acessar o ContaBase, separados por vírgula [localhost,127.0.0.1,servidor]:
```

Inclua o domínio ou IP que você vai usar no navegador.

Exemplo com domínio:

```text
financeiro.seudominio.com,localhost,127.0.0.1
```

Depois:

```text
Vai usar reverse proxy? Ex.: Nginx Proxy Manager, Caddy, Traefik ou Cloudflare Tunnel [s/N]:
```

Responda:

- `s` se vai usar domínio com HTTPS por proxy;
- `n` se vai acessar direto por IP e porta.

Se responder `s`, o instalador pergunta:

```text
IP(s) do proxy confiável, separados por vírgula [127.0.0.1,::1]:
```

Se o proxy roda no mesmo servidor, pode aceitar o padrão.

## Token de setup

Em uma instalação nova, o instalador mostra:

```text
CONTABASE_SETUP_TOKEN=xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

Guarde esse token.

Ele serve para criar o primeiro usuário administrador em:

```text
http://SEU_SERVIDOR:8080/setup
```

ou:

```text
https://financeiro.seudominio.com/setup
```

Depois de criar o administrador, remova ou comente o token:

```bash
sudo nano /etc/contabase/contabase.env
```

Procure a linha:

```text
CONTABASE_SETUP_TOKEN=
```

Comente ou remova a linha. Depois reinicie:

```bash
sudo systemctl restart contabase
```

## Instalação sem perguntas

Use este modo se você já sabe os valores.

```bash
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

Notas:

- `CONTABASE_ASSUME_YES=1` faz o instalador não perguntar.
- `PORT` define a porta local.
- `APP_BASE_URL` é a URL que você abre no navegador.
- `ALLOWED_HOSTS` deve conter seu domínio ou IP.
- `TRUSTED_PROXIES` é usado quando há proxy reverso.

Se `CONTABASE_PORT` e `PORT` existirem ao mesmo tempo, `CONTABASE_PORT` vence.

## Usar o script específico diretamente

Este modo é avançado. Use apenas se você já clonou o repositório.

```bash
git clone --branch vX.Y.Z https://github.com/contabase-app/contabase.git
cd contabase
sudo env CONTABASE_VERSION=vX.Y.Z ./scripts/install-contabase-release.sh
```

Na maioria dos casos, prefira o `install.sh` mostrado no início deste guia.

## Apenas validar, sem instalar

Se você clonou o repositório, pode validar o pacote sem instalar:

```bash
./scripts/install-contabase-release.sh --validate-only
```

Isso baixa e valida o artifact, mas não instala no sistema.

## Atualizar LXC/VPS

Após instalar, use o comando global:

```bash
sudo contabase-update vX.Y.Z
```

O comando baixa o instalador da versão desejada, valida e executa a atualização.
Opcional: `sudo cb-update vX.Y.Z`

### Atualização manual

Baixe o instalador da versão desejada:

```bash
curl -fsSLo /tmp/contabase-install.sh https://get-contabase.pages.dev/install.sh
```

Execute:

```bash
CONTABASE_INSTALL_METHOD=update-release \
CONTABASE_VERSION=vX.Y.Z \
CONTABASE_ASSUME_YES=1 \
bash /tmp/contabase-install.sh
```

O updater preserva:

- configuração;
- secrets;
- token existente;
- banco SQLite;
- uploads;
- backups;
- dados persistentes.

O updater troca apenas o runtime da aplicação.

Ele não restaura banco automaticamente.

## Onde ficam os arquivos

| Caminho | Uso |
|---|---|
| `/opt/contabase` | binário, templates e assets |
| `/etc/contabase/contabase.env` | configuração e secrets |
| `/var/lib/contabase` | banco e dados |
| `/var/lib/contabase/uploads` | uploads |
| `/var/lib/contabase/backups` | backups |
| `/etc/systemd/system/contabase.service` | serviço systemd |

## Verificar se está funcionando

Status do serviço:

```bash
sudo systemctl status contabase --no-pager
```

Healthcheck:

```bash
curl http://127.0.0.1:8080/health
```

Logs recentes:

```bash
sudo journalctl -u contabase -n 100 --no-pager
```

Seguir logs ao vivo:

```bash
sudo journalctl -u contabase -f
```

## Usar com domínio e HTTPS

O instalador grava as variáveis:

- `APP_BASE_URL`
- `ALLOWED_HOSTS`
- `TRUSTED_PROXIES`

Mas ele não configura o proxy reverso externo.

Você precisa configurar seu proxy, por exemplo:

- Nginx Proxy Manager
- Caddy
- Traefik
- Cloudflare Tunnel
- Nginx manual

O proxy deve apontar para o ContaBase local:

```text
http://127.0.0.1:8080
```

ou para a porta que você escolheu.

## Backup

Antes de atualizar ou mexer no servidor, faça backup de:

```text
/etc/contabase/contabase.env
/var/lib/contabase
```

Guia completo:

[Backup e restauração](backup-restauracao.md)

## Problemas comuns

### O serviço não subiu

Veja os logs:

```bash
sudo journalctl -u contabase -n 100 --no-pager
```

### Não consigo acessar pelo domínio

Confira:

1. `APP_BASE_URL` está correto.
2. `ALLOWED_HOSTS` contém o domínio.
3. O proxy reverso aponta para `http://127.0.0.1:8080`.
4. O DNS aponta para o servidor.
5. O HTTPS está configurado.

### Perdi o token de setup

Antes do primeiro setup, veja o arquivo:

```bash
sudo nano /etc/contabase/contabase.env
```

Procure:

```text
CONTABASE_SETUP_TOKEN=
```

Depois de usar o token, remova ou comente a linha.

## Leia também

- [Instalação rápida](instalacao.md)
- [Instalação com Docker](instalacao-docker.md)
- [Configuração](configuracao.md)
- [Atualização](atualizacao.md)
- [Backup e restauração](backup-restauracao.md)
- [Solução de problemas](solucao-de-problemas.md)
