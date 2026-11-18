# Instalação rápida

Comece pelo [Guia Completo de Instalação](https://contabase.pages.dev/setup/) no site oficial. Este guia técnico local detalha os métodos, comandos e requisitos necessários para instalar o ContaBase.

A ideia é simples:

1. Escolha a versão.
2. Baixe o instalador.
3. Execute o instalador.
4. Responda as perguntas.
5. Consulte o token de setup no arquivo local de configuração.
6. Abra o ContaBase no navegador.

Os detalhes completos ficam nos guias específicos:

- [Instalação em LXC/VPS](instalacao-lxc-vps.md)
- [Instalação com Docker](instalacao-docker.md)
- [Configuração](configuracao.md)
- [Atualização](atualizacao.md)

O comportamento de Investimentos na versão pública documentada está no [guia do módulo](investimentos.md): instalações novas pelos instaladores oficiais nascem com a flag desativada; atualizações preservam o valor existente. Instale sempre uma tag já distribuída; o checkout local não substitui os gates de publicação.

## Qual opção devo escolher?

| Se você quer... | Escolha no menu ou comando |
|---|---:|
| Instalar em VPS/LXC sem Docker | `2` |
| Instalar com Docker Compose | `1` |
| Instalar compilando localmente (Source legado) | `3` |
| Ver os scripts manuais | `4` |
| Sair | `5` |
| Atualizar uma instalação existente | `contabase-update` |

Se você não sabe qual escolher:

- use `2` para VPS/LXC sem Docker;
- use `1` se você já usa Docker.

## 1. Use a entrada Beta do Bootstrap

Stable permanece indisponível. A entrada `/install.sh` é exclusiva de Stable; para Beta, use `/beta/install.sh`. O Bootstrap novo exige Linux, Python 3.9+, dependências já instaladas e admissão homologada por versão/método/arquitetura. Sem catálogo válido, encerra sem instalar. Docker tem despacho implementado; Release Bootstrap aguarda seu gate independente e Source Bootstrap permanece bloqueado.

A central [de instalação](https://contabase.pages.dev/setup/) separa esse fluxo dos helpers legados da tag e do Compose manual. O comando Beta é:

```bash
curl -fsSLo /tmp/contabase-install.sh https://contabase.pages.dev/beta/install.sh && bash /tmp/contabase-install.sh
```

Para fixar uma versão específica, informe `CONTABASE_VERSION` ao executar o instalador:

```bash
curl -fsSLo /tmp/contabase-install.sh https://contabase.pages.dev/beta/install.sh && CONTABASE_VERSION=vX.Y.Z-beta.N bash /tmp/contabase-install.sh
```

Substitua `vX.Y.Z-beta.N` por uma Beta publicada e admitida para o método e a arquitetura. A existência de uma tag não comprova compatibilidade com o Bootstrap novo; sem admissão, use o procedimento do helper da tag ou o Compose manual correspondente.

Não use `main`.

Não use `latest`.

Use sempre uma versão publicada no GitHub Releases.

## 2. Use o menu dos scripts de uma tag (fluxo legado)

As etapas seguintes descrevem `scripts/install.sh` de um checkout completo, não o menu do Bootstrap. O exemplo da Beta 9 só deve ser executado após confirmar a tag e os artefatos publicados para o método escolhido, sem alterar instalações existentes:

```bash
export CONTABASE_VERSION=v1.0.0-beta.9
git clone --branch "$CONTABASE_VERSION" --depth 1 https://github.com/contabase-app/contabase.git contabase
cd contabase
chmod +x scripts/install.sh
```

## 3. Execute o instalador

Se você está como `root`:

```bash
bash scripts/install.sh
```

Se você não está como `root`, use `sudo`:

```bash
sudo bash scripts/install.sh
```

## 4. Escolha no menu

O instalador vai mostrar um menu parecido com este:

```text
1) Instalar via Docker Compose
2) Instalar via binário da Release
3) Instalar via código-fonte/systemd (Source legado)
4) Avançado/manual
5) Sair
```

O menu de cinco opções corresponde ao snapshot deste guia. Tags anteriores preservam seu menu publicado, inclusive opções de atualização. Para atualizar uma instalação gerenciada que já possui o comando, use `contabase-update`.

Para a maioria dos usuários:

- digite `2` para instalar em VPS/LXC sem Docker;
- digite `1` para instalar com Docker;
- use `contabase-update` para atualizar uma instalação existente.

A opção `3` instala o **Source legado**, que exige Go e Node e mantém um checkout Git persistente. Para novas instalações VPS/LXC, prefira a opção `2` (binário da Release), que não exige toolchain de build e possui atualização suportada.

O **Source Bootstrap** é um método separado, adiado e indisponível nesta fase. Seu instalador recusa a execução antes de alterar arquivos ou serviços. Ele não possui atualização suportada; não use o updater Source legado em instalações experimentais anteriores.

## 5. Responda as perguntas

As perguntas dependem do método e do modo de acesso. Na opção `2` (Release), uma instalação nova pergunta a tag, se ainda não foi definida, e a porta (padrão `8080`). Em seguida:

```text
Como voce vai acessar o ContaBase?
1) Local somente nesta maquina
2) Rede local/LAN por IP privado
3) Dominio HTTPS/proxy reverso
4) Avancado/manual
Opcao (1/2/3/4) [3]:
```

O padrão desse menu é `3` (proxy). Para acesso direto por IP privado, escolha `2` e informe o IP RFC1918: URL, hosts e `CONTABASE_ACCESS_MODE=lan` são derivados desse IP. A opção `1` configura localhost. A opção `3` pede domínio e proxies confiáveis; a `4` pede URL, hosts e proxies manualmente. O resumo termina com `Continuar com a instalacao? (s/N)`, cujo padrão é não.

No Docker, há uma pergunta opcional de override (padrão não) e um menu de perfil sem padrão indicado. Não há pergunta de porta: o perfil local usa `8080`. No Source legado, o menu de acesso tem padrão `3`; não há pergunta de porta nem confirmação final, e os perfis local/LAN usam `8080`. Configurações manuais exigem conferir porta, URL e hosts no arquivo local.

Textos completos, perguntas condicionais e configuração: [Release/LXC](instalacao-lxc-vps.md#perguntas-feitas-pelo-instalador), [Docker](instalacao-docker.md#perguntas-do-fluxo-guiado) e [Source legado](instalacao-lxc-vps.md#source-legado).

## 6. Consulte o token de setup localmente

O instalador gera o token sem exibir seu valor. Consulte `CONTABASE_SETUP_TOKEN` no arquivo local, em um editor no servidor:

- Release e Source legado: `sudo nano /etc/contabase/contabase.env`.
- Docker: `sudo nano .env.docker`, no diretório da instalação.

Esses arquivos são restritos a `0600`. Use o valor somente em `/setup` para criar o primeiro administrador; não o envie em logs ou mensagens.

Depois de criar o primeiro administrador, remova ou comente o token no arquivo de configuração.
Reinicie o serviço com `sudo systemctl restart contabase`. Para GHCR, recrie o container com `docker compose up -d --force-recreate`, usando o mesmo arquivo Compose da instalação; não há build local. Em instalações com build local, use o procedimento do checkout. Confirme o healthcheck e a ausência de `CONTABASE_SETUP_TOKEN` no ambiente do container após a recriação.

O instalador também gera `SECURITY_MASTER_KEY`, usada para cifrar a senha SMTP. Guarde essa chave junto com os demais segredos: ela não tem fallback público e deve ser preservada em atualizações e restaurações. Sem a chave correspondente, uma senha SMTP já cifrada não pode ser lida, embora o restante da aplicação continue disponível.

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

## Instalação sem perguntas pelo helper da tag

Este é o contrato legado de variáveis do helper Release, não o contrato de flags do Bootstrap. Substitua `vX.Y.Z` por uma tag publicada.

Use este modo somente se você já sabe todos os valores.

Exemplo para VPS/LXC sem Docker:

```bash
export CONTABASE_VERSION=vX.Y.Z
curl -fsSLo /tmp/contabase-install.sh "https://raw.githubusercontent.com/contabase-app/contabase/${CONTABASE_VERSION}/scripts/install.sh"

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
export CONTABASE_VERSION=vX.Y.Z
curl -fsSLo /tmp/contabase-install.sh "https://raw.githubusercontent.com/contabase-app/contabase/${CONTABASE_VERSION}/scripts/install.sh"

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
chmod +x scripts/install.sh
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

O comando detecta automaticamente o modo de instalação (binary, docker ou Source
legado com checkout persistente) e chama o script de update correto. Opcionalmente
use `sudo cb-update`.

Instalações Source Bootstrap não instalam esse comando e não possuem atualização
automática suportada. Para novas instalações VPS/LXC, prefira a opção `2`
(binário da Release).

Para compatibilidade, os métodos não interativos `CONTABASE_INSTALL_METHOD=update-release`, `update-docker` e `update-source` e os scripts manuais continuam disponíveis. Eles não aparecem no menu guiado. Veja os comandos por método no [guia de atualização](atualizacao.md).

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
