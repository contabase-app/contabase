<p align="center">
  <img src="assets/branding/logo-light.png" width="260" alt="ContaBase">
</p>

<p align="center"><strong>Sua base financeira privada.</strong><br>Open-source, self-hosted e multiusuário.</p>

---

## O que é

O ContaBase é uma base financeira open-source (AGPL-3.0) para organizar contas pessoais e de negócios no seu próprio servidor. Os dados ficam em SQLite na instalação que você administra. Não há telemetria, analytics ou envio de dados financeiros para terceiros.

O projeto é feito para quem quer sair de planilhas e apps por assinatura sem entregar o controle dos dados a um SaaS. A aplicação roda em Docker, VPS ou LXC, com interface web e PWA.

## O que você controla

- **Privacidade:** os dados financeiros ficam na sua instalação. O projeto não coleta telemetria nem envia informações para serviços externos.
- **Self-hosted:** você escolhe onde a aplicação roda e administra servidor, banco, chaves e backups.
- **Multiusuário:** pessoas da mesma casa ou equipe podem compartilhar a organização financeira com permissões por workspace.
- **Workspaces:** ambientes Pessoal e Business separados, cada um com seus dados, contas e contexto.
- **Organização financeira:** contas, cartões, lançamentos, faturas, metas, limites e relatórios no mesmo lugar.
- **Investimentos (experimental):** o cadastro e as novas aplicações estão em evolução. Adoção histórica e resgates financeiros das novas aplicações ainda não estão disponíveis.

## Situação atual

O ContaBase está em **Beta pública controlada**. Ainda não existe versão Stable pública.

- A branch `main` é a entrada institucional do projeto. O código funcional e a documentação viva ficam na branch [`beta`](https://github.com/contabase-app/contabase/tree/beta).
- Releases publicadas são tags imutáveis no [GitHub Releases](https://github.com/contabase-app/contabase/releases).
- O canal Beta é o caminho recomendado para instalar e atualizar.
- O operador da instância é responsável por backup, restore, HTTPS, proxy, domínio e segredos.

## Instalação pela Beta

**[Acessar a página oficial de instalação do ContaBase](https://get-contabase.pages.dev/)**

A página oficial apresenta os métodos de instalação disponíveis, os comandos atualizados e permite consultar o código do instalador antes de executá-lo.

O instalador guiado consulta o canal Beta e encaminha para Docker, binário de release ou source.

```bash
curl -fsSLo /tmp/contabase-install.sh https://get-contabase.pages.dev/beta/install.sh && bash /tmp/contabase-install.sh
```

O instalador pergunta o método e os dados de acesso quando necessário. Para acompanhar o passo a passo, leia o [guia de instalação](https://github.com/contabase-app/contabase/blob/beta/docs/instalacao.md).

### Versão fixa

```bash
curl -fsSLo /tmp/contabase-install.sh https://get-contabase.pages.dev/beta/install.sh
CONTABASE_VERSION=vX.Y.Z bash /tmp/contabase-install.sh
```

Substitua `vX.Y.Z` por uma tag publicada. Não use `main`, `beta`, `stable` ou `latest` como versão de instalação.

## Métodos suportados

| Método | Como funciona | Guia |
| --- | --- | --- |
| Docker Compose | Clone da tag publicada e execução de `./scripts/install.sh` | [Instalação com Docker](https://github.com/contabase-app/contabase/blob/beta/docs/instalacao-docker.md) |
| Linux, VPS ou LXC | Binário oficial da GitHub Release com systemd | [Instalação em LXC/VPS](https://github.com/contabase-app/contabase/blob/beta/docs/instalacao-lxc-vps.md) |
| Source | Compilação local com Go e Node | [Instalação](https://github.com/contabase-app/contabase/blob/beta/docs/instalacao.md) |
| Atualização | `sudo contabase-update vX.Y.Z` em uma instalação existente | [Atualização](https://github.com/contabase-app/contabase/blob/beta/docs/atualizacao.md) |

Para acompanhar o canal Beta em uma instalação existente:

```bash
sudo contabase-update --channel beta
```

## Transparência do instalador

A instalação pública é uma cadeia de arquivos verificáveis:

1. O bootstrap do canal, [`/beta/install.sh`](https://get-contabase.pages.dev/beta/install.sh), consulta o manifest.
2. O manifest [`/channels/beta.json`](https://get-contabase.pages.dev/channels/beta.json) declara a tag Beta atual.
3. O instalador da tag baixa o helper e valida a versão e os artefatos da release.
4. O instalador guiado encaminha para Docker, binário de release ou source.

Para baixar, inspecionar e só depois executar:

```bash
curl -fsSLo /tmp/contabase-install.sh https://get-contabase.pages.dev/beta/install.sh
less /tmp/contabase-install.sh
bash /tmp/contabase-install.sh
```

Ler apenas o bootstrap não audita a instalação inteira. Confira também o manifest, o instalador da tag e os checksums dos artefatos. O código do canal está em [`contabase-canal`](https://github.com/contabase-app/contabase-canal).

## Documentação

- [Primeiros passos](https://github.com/contabase-app/contabase/blob/beta/docs/primeiros-passos.md)
- [Instalação](https://github.com/contabase-app/contabase/blob/beta/docs/instalacao.md)
- [Instalação com Docker](https://github.com/contabase-app/contabase/blob/beta/docs/instalacao-docker.md)
- [Instalação em LXC/VPS](https://github.com/contabase-app/contabase/blob/beta/docs/instalacao-lxc-vps.md)
- [Atualização](https://github.com/contabase-app/contabase/blob/beta/docs/atualizacao.md)
- [Backup e restauração](https://github.com/contabase-app/contabase/blob/beta/docs/backup-restauracao.md)
- [Investimentos](https://github.com/contabase-app/contabase/blob/beta/docs/investimentos.md)
- [Arquitetura](https://github.com/contabase-app/contabase/blob/beta/docs/arquitetura.md)

## Segurança e privacidade

- [Política de segurança](SECURITY.md)
- [Política de privacidade de referência](PRIVACY.md)
- [Termos de uso](TERMS.md)

O ContaBase não promete segurança absoluta nem conformidade legal automática. A instalação é self-hosted e a responsabilidade operacional é do administrador da instância. Antes de usar dados importantes, faça backup, teste o restore, configure HTTPS e mantenha as chaves protegidas.

## Licença

Distribuído sob a licença [AGPL-3.0](LICENSE).

## Repositórios

- [Código e documentação](https://github.com/contabase-app/contabase/tree/beta)
- [GitHub Releases](https://github.com/contabase-app/contabase/releases)
- [Canal de instalação](https://get-contabase.pages.dev/)
- [Issues](https://github.com/contabase-app/contabase/issues)
