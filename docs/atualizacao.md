# Atualização

Este guia mostra como atualizar o ContaBase.

Regra principal:

> Antes de atualizar, faça backup.

Leia também:

[Backup e restauração](backup-restauracao.md)

## 1. Escolha a nova versão

Exemplo:

```bash
export CONTABASE_VERSION=v0.1.0-beta.1
```

Quando sair uma versão nova, troque somente o valor:

```bash
export CONTABASE_VERSION=v0.1.0-beta.2
```

Não use `main`.

Não use `latest`.

Use uma versão publicada no GitHub Releases.

## 2. Verifique a versão atual

### LXC/VPS

```bash
cat /opt/contabase/VERSION
```

### Docker

Entre na pasta onde está o `docker-compose.yml` e rode:

```bash
docker compose exec contabase cat /app/VERSION
```

## 3. Atualizar LXC/VPS

Use este método se você instalou sem Docker, com binário pronto e systemd.

Baixe o instalador da versão desejada:

```bash
export CONTABASE_VERSION=v0.1.0-beta.1

curl -fsSL -o /tmp/contabase-install.sh \
  "https://raw.githubusercontent.com/contabase-app/contabase/${CONTABASE_VERSION}/scripts/install.sh"
```

Execute a atualização:

```bash
CONTABASE_INSTALL_METHOD=update-release \
CONTABASE_VERSION=v0.1.0-beta.1 \
CONTABASE_ASSUME_YES=1 \
bash /tmp/contabase-install.sh
```

O updater preserva:

- `/etc/contabase/contabase.env`;
- secrets;
- token existente;
- banco SQLite;
- uploads;
- backups;
- `/var/lib/contabase`.

Ele troca o binário e os arquivos da aplicação.

Ele não restaura banco automaticamente.

## 4. Atualizar Docker

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

O updater Docker preserva:

- `.env.docker`;
- secrets;
- banco;
- uploads;
- backups;
- volumes.

## 5. Atualizar Docker manual com GHCR

Se você usa o compose manual com imagem GHCR:

```bash
docker compose pull
docker compose up -d
```

Se quiser trocar a versão fixa, edite o `docker-compose.yml`.

Exemplo:

```yaml
image: ghcr.io/contabase-app/contabase:v0.1.0-beta.1
```

Depois rode:

```bash
docker compose pull
docker compose up -d
```

## 6. Conferir se funcionou

### LXC/VPS

```bash
systemctl status contabase
curl http://127.0.0.1:8080/health
cat /opt/contabase/VERSION
```

### Docker

```bash
docker compose ps
curl http://127.0.0.1:8080/health
docker compose exec contabase cat /app/VERSION
```

## 7. Se algo der errado

### LXC/VPS

Veja os logs:

```bash
journalctl -u contabase -n 100 --no-pager
```

Siga os logs:

```bash
journalctl -u contabase -f
```

### Docker

Veja os logs:

```bash
docker compose logs --tail 100 contabase
```

## Dry-run e validação

### LXC/VPS

Se você clonou o repositório ou tem o script de update local:

```bash
sudo env CONTABASE_VERSION=v0.1.0-beta.1 ./scripts/update-contabase-release.sh --dry-run
```

### Docker

```bash
./scripts/update-contabase-docker.sh --dry-run
```

Dry-run valida o que seria feito sem atualizar de verdade.

## O que a atualização não faz

A atualização não deve:

- apagar banco;
- apagar uploads;
- apagar backups;
- sobrescrever secrets;
- trocar chaves de segurança;
- restaurar banco automaticamente.

Se precisar restaurar backup, faça isso manualmente e com cuidado.

## Leia também

- [Instalação rápida](instalacao.md)
- [Instalação em LXC/VPS](instalacao-lxc-vps.md)
- [Instalação com Docker](instalacao-docker.md)
- [Configuração](configuracao.md)
- [Backup e restauração](backup-restauracao.md)
- [Solução de problemas](solucao-de-problemas.md)
