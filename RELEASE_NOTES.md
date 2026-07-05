# Release Notes

Este documento é o índice principal das notas de lançamento do ContaBase.
Para o histórico técnico detalhado, consulte o [CHANGELOG.md](CHANGELOG.md).

## Em preparação

- **v0.1.0-beta.5** — *Relatórios financeiros com Fluxo de Caixa, DRE, Extrato e exportações*

  A área de Relatórios foi reorganizada para separar melhor três perguntas diferentes: o que já entrou ou saiu do caixa, o que pertence ao resultado do período e quais movimentos já foram confirmados.

  **Personal mais simples.** Workspaces pessoais mostram Fluxo de Caixa e Extrato. A visão de Competência/DRE fica fora do Personal para reduzir ruído.

  **Business mais completo.** Workspaces empresariais mostram Fluxo de Caixa, Competência/DRE e Extrato. A DRE serve para análise gerencial e contábil; o Fluxo de Caixa serve para acompanhar dinheiro; o Extrato serve para conferir movimentos já confirmados.

  **Fluxo de Caixa Realizado.** Mostra apenas dinheiro que já entrou ou saiu. Receitas recebidas e despesas pagas entram aqui. Pagamento de fatura de cartão aparece como saída. Compras individuais no cartão não aparecem como saída de caixa nesse momento, porque o dinheiro sai quando a fatura é paga. Transferências internas aparecem no extrato por conta, mas não alteram o resultado consolidado.

  **Fluxo de Caixa Previsão.** Combina o que já aconteceu com o que ainda está pendente por vencimento. Faturas abertas ou fechadas entram pelo saldo ainda pendente, sem duplicar as compras individuais do cartão. A previsão mostra o mês selecionado e os 12 meses seguintes.

  **Saldo Projetado.** A Previsão mostra uma estimativa de saldo futuro usando o saldo atual das contas reais, somando valores futuros a receber e subtraindo valores futuros a pagar e faturas pendentes. Contas de cartão não entram como saldo real, e meses passados não recebem saldo histórico inventado.

  **Competência/DRE.** No Business, a DRE considera o período econômico do lançamento. Compras no cartão entram como despesa do período da compra. O pagamento da fatura não entra de novo na DRE, evitando duplicidade.

  **Extrato.** Exibe movimentos confirmados, com visão de movimentos e resumo diário. Transferências aparecem como saída em uma conta e entrada em outra, para facilitar conferência, sem distorcer o resultado consolidado.

  **Exportações.** CSV e PDF foram revisados para Fluxo de Caixa, Competência/DRE e Extrato. Os PDFs usam a identidade visual do workspace, fonte embutida, acentuação corrigida, paginação revisada e tabelas mais estáveis.

  Exemplos práticos: uma compra no cartão em julho aparece na DRE de julho, mas só sai do caixa quando a fatura é paga em agosto; uma receita pendente aparece na Previsão até ser recebida; uma fatura parcialmente paga mantém apenas o saldo restante na Previsão; e meses futuros mostram uma projeção de saldo baseada nas pendências conhecidas.

- **v0.1.0-beta.4** — *Agendamento automático, data efetiva de pagamento, conta padrão para fatura e hotfix de atualização*

  Ajustes finais após a validação da beta.4: em `/lancamentos`, as faturas agora seguem os filtros da tela; faturas parcialmente pagas exibem o saldo pendente como valor principal, com badge `Parcial` e texto `Total ... · Pago ...`; faturas não aparecem em `Agendado`; o controle `Selecionar` ficou abaixo dos cartões/faturas; e o modal de pagamento passou a mostrar `Pagar agora` e `Agendar` com submit coerente.

  **Pagamento/recebimento agendado.** Lançamentos comuns INCOME/EXPENSE pendentes podem ser agendados com data futura. O lançamento continua pendente até a data escolhida, quando o executor automático o marca como pago — sem intervenção manual. Transferências, faturas e compras de cartão não entram no agendamento.

  **Executor automático.** Worker inicia com o servidor e executa periodicamente. Ao processar um agendamento vencido, o UPDATE é atômico com verificação de `RowsAffected == 1`, prevenindo execução duplicada. O histórico registra `source=automatic` e uma notificação é criada com deduplicação (`INSERT OR IGNORE`).

  **Filtro "Agendado".** Nova opção de situação nos Lançamentos separa agendados de pendentes comuns e vencidos. Incluído nos filtros padrão. Agendados não aparecem em vencidos, atrasados ou inadimplência.

  **Status visual unificado.** Ícones e cores padronizados: relógio amarelo (pendente no prazo), alerta vermelho (atrasado), azul (transferência pendente), timer roxo (agendado) e check verde (pago). Valores mantêm cor por tipo: receita verde, despesa vermelha, transferência azul.

  **Data efetiva de pagamento.** Lançamentos agora registram a data real do pagamento sem alterar a competência nem o vencimento. Toggle individual e operação em lote permitem escolher a data exata antes de confirmar.

  **Histórico de pagamento.** Toda mudança de status ou data de pagamento fica registrada em `transaction_status_history` — com usuário, data/hora, valores anterior/novo e fonte (manual/automático) — visível no detalhe do lançamento.

  **Exibição discreta.** Cards de lançamentos pagos mostram "Pago em DD/MM/AAAA" abaixo dos metadados, em texto verde pequeno, sem badge colorido que competia com o título no mobile.

  **Swipe com modal.** Swipe mobile abre o modal de confirmação com data, sem marcar pago direto. FAB oculta durante seleção em lote.

  **Conta padrão para pagamento de fatura.** Cada cartão pode ter uma conta CHECKING/SAVINGS padrão para pagamento da fatura. Ao quitar ou pagar parcialmente, o modal pré-seleciona essa conta, mantendo a opção de escolha manual. Conta inválida ou de outro workspace é ignorada com fallback para o comportamento atual.

  **Botões de modal mais curtos.** Os botões dos modais de quitação e pagamento parcial agora exibem apenas "Confirmar", eliminando quebra de linha em telas mobile.

  **Timezone financeiro.** `CONTABASE_TIMEZONE` (padrão `America/Sao_Paulo`) define o fuso para datas civis de pagamento e agendamento. Prioridade: `CONTABASE_TIMEZONE` > `TZ` > `America/Sao_Paulo`. `time/tzdata` embutido no binário para não depender do sistema operacional. Scripts de install/update fazem backfill automático da chave quando ausente.

  **Hotfix de atualização.** `cb-update` e updaters release/Docker aceitam instalações existentes sem `CONTABASE_SETUP_TOKEN` (one-time/bootstrap). `AUTH_ENCRYPTION_KEY` e `SECURITY_MASTER_KEY` continuam obrigatórios.

  Usuários da beta.3 devem atualizar para a beta.4 para update sem restaurar o setup token. A release ainda não foi publicada. Backup e teste de restore continuam obrigatórios antes de atualizar ambientes com dados importantes.

- **[v0.1.0-beta.3](docs/releases/v0.1.0-beta.3.md)** — *Beta pública controlada*

  Terceira Beta pública do ContaBase, com novo Dashboard unificado ("Posição Financeira"), proteção visual de tendências percentuais, melhorias nos históricos rápidos de Reservas e Limites, novo modo de acesso `local-docker`, revisão da política de modos de acesso (`local`, `local-docker`, `lan`, `proxy`) e fluxo guiado de instalação com pergunta de modo de acesso conforme tipo de instalação.

  A release ainda não foi publicada. Backup e teste de restore são obrigatórios antes de usar dados importantes.

- **[v0.1.0-beta.2](docs/releases/v0.1.0-beta.2.md)** — *Beta pública controlada*

  Segunda Beta pública do ContaBase, com redesign visual do financeiro (Dense Financial UI), correção do cálculo de limite de cartão por saldo pendente das faturas, correção de duplicidade na edição de lançamentos recorrentes, busca de categorias sem acento, seletor manual de fatura no formulário de lançamento e scripts de atualização revisados.

  A release ainda não foi publicada. Backup e teste de restore são obrigatórios antes de usar dados importantes.

## Histórico

- **[v0.1.0-beta.1](docs/releases/v0.1.0-beta.1.md)** — *22 de junho de 2026*

  Primeira Beta pública controlada do ContaBase. Imagem Docker multi-arch via `ghcr.io/contabase-app/contabase`, artifacts de GitHub Release para Linux `amd64`/`arm64` com checksum obrigatório e build local/source avançado.

  Recorrências/parcelamentos (B4) e pagamento parcial de faturas (B5) foram resolvidos na v0.1.0-beta.2.

- **[v0.1.0-alpha.1](docs/releases/v0.1.0-alpha.1.md)** — *03 de junho de 2026*

  Primeira release pública de validação, preservada como registro histórico.
