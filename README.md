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
- [Candidata v1.0.0-beta.2 — publicação beta em andamento](docs/releases/v1.0.0-beta.2.md)
- [Notas da Beta.9 publicada](docs/releases/v0.1.0-beta.9.md)
- [Última beta publicada](docs/releases/v0.1.0-beta.9.md)
- [Histórico da primeira beta](docs/releases/v0.1.0-beta.1.md)

## Destaques da candidata v1.0.0-beta.2

- Ao criar ou editar uma receita ou despesa com data futura e confirmar o status como pago, o formulário informa que a confirmação será agendada para aquela data. O lançamento permanece pendente até a execução e não altera o saldo antecipadamente; datas futuras mantidas como pendentes continuam sem mudança de comportamento.
- A área administrativa de Backups identifica o local lógico `DATA_DIR/backups/` e mostra até sete snapshots diários protegidos. A rotina cria no máximo um arquivo por dia civil, nunca substitui o snapshot daquele dia após importações ou reinícios e mantém backups pré-restore em uma área separada. A presença de um arquivo não substitui o teste periódico de restauração.
- O PWA usa ícones instaláveis com fundo grafite integral e uma variante `maskable` dentro da zona segura. O manifest recebe versão por conteúdo para evitar a reutilização dos ícones antigos pelo navegador.
- Em recorrências, a exclusão "Este e os próximos" encerra futuras ocorrências comuns mesmo se foram marcadas como pagas ou a cadeia legada está inconsistente; faturas protegidas e exceções manuais permanecem intactas.
- No mobile, valor e data fazem parte do toque que abre a edição; os resumos de fatura isolam título e valores para evitar sobreposição.

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
