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

## Instalação

Acesse o canal público de instalação em https://get-contabase.pages.dev/ e siga o instalador guiado.

→ [Guia completo de instalação](docs/instalacao.md)
→ [Instalação com Docker](docs/instalacao-docker.md)
→ [Instalação em LXC/VPS](docs/instalacao-lxc-vps.md)
→ [Guia de atualização](docs/atualizacao.md)
→ [Primeiros passos](docs/primeiros-passos.md)

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
- [Última beta publicada — v1.0.0-beta.7](docs/releases/v1.0.0-beta.7.md)
- [Beta publicada anterior — v1.0.0-beta.6](docs/releases/v1.0.0-beta.6.md)
- [Beta publicada anterior — v1.0.0-beta.5](docs/releases/v1.0.0-beta.5.md)
- [Beta publicada anterior — v1.0.0-beta.4](docs/releases/v1.0.0-beta.4.md)
- [Histórico da beta.3](docs/releases/v1.0.0-beta.3.md)
- [Notas da Beta.9 publicada](docs/releases/v0.1.0-beta.9.md)
- [Histórico da beta.2](docs/releases/v1.0.0-beta.2.md)
- [Histórico da primeira beta](docs/releases/v0.1.0-beta.1.md)

## Destaques da Beta v1.0.0-beta.7

A Beta 7 conclui o ciclo de Investimentos: cancelamento integral de lotes com transferências, estorno individual de compras adicionais, cancelamento de registro inicial record-only e aplicações sem transferência automática. A migration 82 corrige o bloqueio de contas e lotes após um estorno individual. A entrada monetária de criação, reaplicação e posição passa a usar os dois últimos dígitos digitados como centavos: `1644041` exibe `R$ 16.440,41`. A correção de valores já registrados exige uma operação financeira própria. Instalações novas começam com Investimentos desativado; atualizações preservam a configuração existente. Lançamentos, Contas a pagar/receber e Lançamentos das faturas ganharam cards individuais, logos das instituições nas transferências e dica de swipe no mobile. Consulte as [notas da versão](docs/releases/v1.0.0-beta.7.md).

## Destaques da Beta v1.0.0-beta.6

- Investimentos disponíveis por padrão quando a flag do módulo está ausente, em instalações novas e atualizadas. Desativações explícitas são preservadas.
- Aplicações com conta de origem, transferência atômica e histórico vinculado; adoção histórica e resgates financeiros das novas aplicações permanecem indisponíveis. Consulte [Investimentos](docs/investimentos.md).
- Relatórios, Contatos, Configurações e Administração com componentes Premium, navegação responsiva e ajustes de acessibilidade.
- Controles de privacidade em Contas, Investimentos e Notificações; máscaras financeiras, alinhamento dos formulários e áreas de toque revisados.
- O menu de ações em Metas e Reservas permanece acima dos cartões vizinhos.
- Faturas abre no próximo mês com fatura em aberto quando o mês atual já está pago.

Esses destaques pertencem à Beta 6. A Beta 7 é a versão publicada mais recente; a promoção do canal de instalação depende do smoke real de instalação e atualização da tag.

## Avisos da Beta

A Beta pública não é uma versão stable. Ainda não existe release stable pública do ContaBase.

- A primeira beta permanece disponível apenas como histórico.
- Os instaladores guiados (Docker e LXC/VPS) geram secrets fortes automaticamente e os updaters preservam configuração, banco, uploads e backups. O token de setup é one-time e não é exigido em updates depois do primeiro administrador.
- O fuso financeiro usado por datas civis pode ser definido com `CONTABASE_TIMEZONE`; no Brasil, normalmente o padrão `America/Sao_Paulo` já basta.
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
