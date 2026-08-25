# Release Notes

Este documento é o índice principal das notas de lançamento do ContaBase.
Para o histórico técnico detalhado, consulte o [CHANGELOG.md](CHANGELOG.md).

## Candidata atual

- **v1.0.0-beta.1 — publicação beta em andamento — 25 de agosto de 2026** — *Primeira candidata da linha 1.0*

  Esta candidata inicia a preparação da futura versão estável `v1.0.0`. O foco atual é consolidar identidade visual, leitura correta dos resumos de despesas e segurança das operações sobre recorrências antes do congelamento funcional.

  A identidade do ContaBase passa a usar o monograma `CB` no sistema, PWA, favicons, autenticação e carregamentos. O ícone instalado mantém fundo grafite integral, variante `maskable` dentro da zona segura e manifest versionado por conteúdo para evitar o cache da identidade anterior. Em `/lancamentos`, Pago, Pendente, Vencido e Agendado passam a representar somente despesas e obrigações de fatura visíveis.

  O formulário de lançamentos passa a explicar a confirmação futura no momento da entrada: receitas e despesas fora de cartão, com data futura e status pago, são registradas com pagamento agendado para a data escolhida e sem movimentar o saldo antes dela. Manter o status pendente preserva exatamente o fluxo atual.

  Em Configurações → Backups, o administrador global pode confirmar o local lógico `DATA_DIR/backups/` e consultar até sete snapshots diários protegidos. A rotina verifica a cobertura a cada hora, mas publica apenas um arquivo por dia civil: reinícios e imports não substituem o snapshot daquele dia. Na atualização, o arquivo automático legado mais antigo de cada dia é preservado e as duplicatas só são removidas depois da adoção protegida. Backups pré-restore continuam separados. A visualização não abre os arquivos nem revela caminhos absolutos; existência ainda não equivale a um restore validado. A ação de importar usa o violeta do sistema, deixando o vermelho para os avisos de risco.

  Nas recorrências, o status de pagamento torna-se local à ocorrência selecionada mesmo quando o usuário escolhe "Este e futuros" ou "Toda a recorrência". Os demais campos continuam seguindo o escopo escolhido. A única regra estrutural preservada é a de despesas vinculadas a cartão de crédito, cuja liquidação individual é representada pela fatura.

  O materializador passa a reconhecer de forma controlada primeiras ocorrências legadas com propriedades locais diferentes da regra e exceções antigas sem `date_civil`, sem reparar ou sobrescrever dados automaticamente. Divergências em sequências posteriores, associações entre workspaces e inconsistências de fatura continuam falhando de forma fechada.

  **Upgrade pretendido:** `v0.1.0-beta.9` → `v1.0.0-beta.1`. Esse caminho ainda precisa passar pelos gates completos e por smoke real antes de ser declarado suportado.

  **Validação local:** a suíte canônica completa em `TZ=UTC`, a matriz financeira/recorrente repetida, `go vet`, build, política do atualizador e validações de sintaxe estão verdes. Os testes de fatura foram estabilizados para não dependerem de datas fixas de agosto/setembro de 2026.

  **Distribuição:** a candidata só será disponibilizada depois do Release Preflight no SHA público exato, criação da tag imutável, validação dos packages Linux e checksums, imagem GHCR fixa e smokes previstos no runbook.

  **Canal:** permanece em `v0.1.0-beta.9` durante os gates e só poderá avançar para `v1.0.0-beta.1` depois da validação da Release e dos caminhos de instalação/atualização.

## Histórico

- **v0.1.0-beta.9 — HOTFIX — 15 de agosto de 2026** — *Atualização self-hosted mais segura e resumo mensal de reservas concluído*

  Esta Beta publicada reúne política de versão consistente e execução repetível nos updaters Docker, source e release, além do resumo mensal de reservas na tela de Metas.

  A validação canônica de backup/restore foi ajustada para funcionar de forma portátil no BusyBox. A fixture de faturas foi corrigida para manter a suíte Go determinística sob `TZ=UTC`.

  O commit público `58544389c85b7a438bfb16fa1ee527cadb333f19` passou pelo workflow `Release Preflight`, incluindo testes Go com `TZ=UTC`, validações GNU/BSD/BusyBox, builds Linux `amd64`/`arm64`, empacotamento e checksums. Os mesmos gates passaram novamente após a tag.

  **Caminhos suportados de atualização:** `beta.7` → `beta.9` e `beta.8` → `beta.9`. Os contratos possuem cobertura automatizada; smokes reais sobre ambientes descartáveis não foram executados por ainda não existir harness oficial apropriado.

  **Distribuição:** a GitHub Release prerelease disponibiliza packages Linux `amd64` e `arm64`, `checksums.txt` e a imagem multi-arquitetura imutável `ghcr.io/contabase-app/contabase:v0.1.0-beta.9`.

  **Canal:** o canal Beta oficial aponta para `v0.1.0-beta.9`. A versão Stable continua inalterada.

- **v0.1.0-beta.8** — *Datas civis, histórico de faturas protegido, recorrências mais seguras e atualização self-hosted com Node 24*

  Esta Beta prepara o ContaBase para tratar datas financeiras como datas de calendário, sem depender do fuso horário do servidor. O histórico de faturas e pagamentos fica mais protegido: estados e saldos usam pagamentos efetivos de forma consistente, pagamentos podem ser revertidos com proveniência e os registros históricos não são alterados automaticamente. As recorrências também preservam melhor o histórico: excluir apenas uma ocorrência impede que ela volte a ser criada, editar este e os futuros respeita o ponto escolhido e toda a série é atualizada de forma atômica, protegendo lançamentos pagos, exceções e faturas. O healthcheck de monitoramento foi reforçado com documentação para Docker e Uptime Kuma.

  - Indicador da data, hora e timezone configurada no rodapé autenticado.

  A atualização inclui as migrations 52 a 59. A evolução de schema preserva os dados existentes e aplica validações de integridade antes de concluir o upgrade; faça backup WAL-consistente antes de atualizar. O preenchimento ou reparo de dados históricos de data civil não é automático. Para self-hosted, Node.js 24 LTS é a opção recomendada, com Node.js 22 LTS ainda compatível; no fluxo source, a validação ocorre antes de parar o serviço.

  **Backup canônico e rollback seguro.** Os scripts de atualização Docker, source e release agora exigem um backup canônico completo do banco e uploads antes de qualquer mutação. Se o backup falhar, o update é interrompido sem alterar o estado atual; `--no-backup` é rejeitado; e não há fallback para cópias cruas de DB/WAL/SHM. Se o healthcheck do novo runtime falhar, o serviço permanece parado e a versão anterior não é reativada automaticamente; o retorno exige restore explícito do pacote pré-update.

  **Faturas — movimentação manual.** Lançamentos individuais de cartão de crédito podem ser movidos manualmente para outra fatura elegível (aberta ou fechada), com persistência do vínculo em `invoice_override_id`. Pagamentos modernos e legados são preservados, faturas quitadas ou liquidadas permanecem protegidas, e o lançamento pode retornar ao vínculo automático sem alterar sua competência, data ou valor.

  **Faturas — reversão auditável.** Um pagamento de fatura pode ser revertido por usuário autenticado. A operação registra motivo, responsável, data e o lançamento de reversão, preservando o histórico para os relatórios e evitando duplicidade de identidade entre pagamentos e reversões.

  **Reservas — planejamento mensal.** O resumo de planejamento passa a usar projeções determinísticas de aporte necessário por alvo/data, rendimento composto, estimativa de prazo, saldo projetado e análise de suficiência/insuficiência.

  **Atenção no upgrade.** As migrations interrompem o upgrade com uma mensagem de diagnóstico se encontrarem dados recorrentes ou pagamentos de fatura em conflito. Resolva o conflito indicado antes de repetir a atualização; não force o upgrade nem faça downgrade para um binário que não reconheça exclusões unitárias persistentes de recorrências.

- **v0.1.0-beta.7** — *Recorrências versionadas e projeção automática das ocorrências futuras*

  As recorrências (lançamentos fixos) foram reconstruídas sobre um modelo de série versionada. Agora cada recorrência tem um histórico claro, o passado é preservado e as mudanças ficam registradas como versões da mesma série.

  **Editar uma ocorrência.** "Apenas esta" transforma a ocorrência em uma exceção manual, sem afetar as demais e sem ser sobrescrita depois. "Esta e futuras" cria uma nova versão da série a partir do ponto escolhido, encerrando a versão anterior sem apagar o histórico. "Toda a série" atualiza de uma vez as ocorrências que podem mudar, protegendo exceções manuais, lançamentos pagos e itens em faturas fechadas/pagas.

  **Excluir a partir de um ponto.** "Esta e futuras" encerra a série no ponto escolhido e remove apenas as ocorrências futuras elegíveis. As ocorrências apagadas não voltam nas próximas atualizações automáticas.

  **Projeção automática.** As ocorrências futuras são mantidas automaticamente por cerca de 15 meses à frente. Uma varredura diária, uma varredura leve ao iniciar o servidor e um preenchimento sob demanda ao abrir o Dashboard e os Lançamentos mantêm tudo atualizado, sem travar as telas. Faturas e Relatórios não disparam esse preenchimento.

  **Segurança dos dados.** Faturas fechadas ou pagas e transações já pagas são sempre preservadas. Saldos e faturas continuam consistentes, e rodar a projeção mais de uma vez não cria lançamentos duplicados.

   **Atualização.** O upgrade de bancos já existentes é seguro e aditivo: cria a estrutura de séries, faz o backfill dos lançamentos recorrentes antigos e prepara o controle de projeção, sem mexer em saldos ou faturas. Backup antes de atualizar continua obrigatório.

   **Ajustes em `/lancamentos`.** A listagem agora inclui lançamentos pagos por padrão. Workspaces Business separam corretamente a competência (`date`) do vencimento (`due_date`): a listagem agrupa pelo vencimento quando disponível, o formulário de edição carrega ambas as datas sem confusão e um indicador discreto "Comp." aparece apenas quando as datas diferem. O badge "Venc." foi removido dos cards e o layout mobile do Business foi refinado para permitir descrição em até duas linhas sem quebrar o cliente, a conta ou o valor. A release ainda não foi publicada.

- **v0.1.0-beta.6** — *Relatório contábil empresarial com exportações CSV, PDF e ZIP*

  O workspace Business ganhou um relatório contábil novo, com resumo, macros e saldos de contas. As exportações CSV, PDF e ZIP agora saem do mesmo contrato de relatório, incluindo o total de saldos nas saídas.

  **Macros e categorias.** O contrato de macros foi centralizado para classificar as linhas do relatório de forma consistente. As categorias canônicas do Business foram reparadas e a base financeira empresarial recebeu seed de receita para validar o fluxo.

  **UI e consistência.** O dashboard recebeu um card carousel redesenhado com controles em grid e sem painel de resumo. O Tailwind foi atualizado para v4.3.0, os saldos passaram a usar cores semânticas e o espaçamento das configurações mobile foi normalizado.

  **Validação.** Foi adicionado um smoke fixture de relatório contábil e cobertura de testes para PDF, CSV, ZIP, macro grouping e renderização.

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
