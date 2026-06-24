# Primeiros passos

Este guia ajuda a escolher como instalar o ContaBase antes de você sair copiando comandos.

## O que é a Beta?

A `v0.1.0-beta.1` é a primeira Beta pública controlada. Ela está funcional, mas **não é uma versão stable**. Algumas áreas ainda estão em validação (recorrências/parcelamentos e pagamento parcial de faturas, por exemplo).

A release `v0.1.0-beta.1` **já está publicada** como Beta pública controlada (prerelease). A imagem Docker no GHCR e os artifacts da GitHub Release estão disponíveis.

## Qual método devo escolher?

Recomendado começar pelo `scripts/install.sh` — ele oferece um menu interativo ou modo não interativo e chama o script correto automaticamente. Veja [Instalação](instalacao.md).

| Se você... | Use este método | Por quê |
|------------|-----------------|---------|
| Quer começar pelo instalador guiado | [install.sh (menu interativo)](instalacao.md) | Entrada única; escolhe o método certo por você. |
| Quer testar rápido em homelab ou máquina local | [Docker (instalador guiado)](instalacao-docker.md) | Mais simples; o instalador guiado cuida de secrets, permissões e containers. |
| Já usa Docker, Dockge, Portainer ou CasaOS | [Docker manual com GHCR](instalacao-docker.md#opção-manual-docker-compose-com-ghcr-sem-clonar-o-repositório) | Imagem pronta do GHCR; compose/env copiáveis; sem clone. |
| Quer rodar em VPS/LXC sem Docker | [Release/LXC/VPS (instalador guiado)](instalacao-lxc-vps.md) | Binário pronto com systemd e validação de checksum; sem Go/Node. |
| Vai alterar código, templates ou CSS | [Build local/source](instalacao.md#build-localsource) | Exige Go e Node; dá controle total. |

## Antes de colocar dados reais

Independentemente do método:

1. **Faça backup** do banco, uploads e configuração.
2. **Teste o restore** em outro lugar.
3. **Valide o healthcheck** (`/health`).
4. **Use HTTPS** se expuser para a internet (via proxy, tunnel ou CDN).
5. **Remova o token de setup** após criar o primeiro administrador.

Leia também:

- [Segurança](../SECURITY.md)
- [Backup e restauração](backup-restauracao.md)
- [Atualização](atualizacao.md)

## Próximo passo

Comece pelo instalador guiado ou escolha um método específico:

- [Instalação (install.sh)](instalacao.md) — entrada recomendada
- [Instalação com Docker](instalacao-docker.md) — inclui modo manual com GHCR para Dockge/Portainer
- [Instalação em LXC/VPS](instalacao-lxc-vps.md)
- [Build local/source](instalacao.md#build-localsource)
