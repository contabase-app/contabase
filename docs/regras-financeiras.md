# ContaBase - Regras Financeiras

Esta pagina resume as regras que o sistema usa para calcular valores, faturas e limites. Ela existe para referencia tecnica e para evitar interpretacoes erradas na operacao diaria.

## Dois modos de uso

- `Personal` atende uso familiar e fluxo de caixa simples.
- `Business` adiciona contas a pagar/receber, faturas, contatos e relatorios de negocio.

## Contas

A pagina `/contas` separa tres abas: `A Pagar`, `A Receber` e `A Transferir`.

- `A Transferir` mostra apenas transferencias internas com status `pending` no periodo selecionado.
- A data de referencia da aba e o vencimento quando existir; sem vencimento, usa a data da transferencia. Essa e a mesma semantica de periodo das demais pendencias.
- A aba exibe origem, destino, valor, data e situacao; parcelas e recorrencias sao identificadas por badge.
- Transferencias internas nao entram em `A Pagar`, `A Receber` nem nos totais de inadimplencia.
- A busca da aba filtra somente as transferencias; os tres indicadores superiores continuam representando os totais do periodo.
- Transferencia pendente nao reserva saldo: o debito na origem e o credito no destino so ocorrem na confirmacao.
- A aba permite editar e confirmar a transferencia pelo fluxo proprio; a confirmacao aplica o debito e o credito atomicamente. Os gestos de swipe sao apenas atalhos para confirmar ou remover e nao alteram a regra financeira.

## Saldo total e obrigacoes (F1)

Desde a Beta 8, Reservas nao fazem parte do dominio financeiro. O Dashboard nao calcula saldo reservado nem subtrai valores reservados para exibir um "saldo disponivel".

- `Saldo total` e a soma do saldo atual das contas nao arquivadas que nao sao cartao de credito. Contas de investimento entram na soma e podem nao ser resgataveis imediatamente; o valor nao representa dinheiro disponivel para gastar.
- `Obrigacoes` reune faturas de cartao ainda nao pagas pelo saldo residual e despesas pendentes em contas que nao sao cartao, incluindo investimento e carteira, liquidas de reembolsos ativos. Compras de cartao entram pela fatura, sem duplicacao com a compra individual.
- Os recortes de 30 e 60 dias usam o vencimento efetivo. Obrigacoes vencidas entram no recorte de 30 dias quando ainda estao pendentes. Itens sem data utilizavel nao entram no periodo e sao sinalizados separadamente.
- A composicao usa um unico snapshot de leitura. Se houver inconsistencia ou estouro representavel, o indicador aparece como indisponivel em vez de exibir um numero inferido.
- Nao ha transferencia automatica entre saldo total, obrigacoes e metas. Metas continuam sendo planejamento e nao reservam saldo.

## Relatorios financeiros

Os relatorios separam tres perguntas diferentes:

- `Fluxo de Caixa`: quanto dinheiro entrou, saiu ou ainda esta previsto.
- `Competencia / DRE`: qual foi o resultado economico do periodo.
- `Extrato`: quais movimentos ja foram confirmados.

Workspaces `Personal` exibem Fluxo de Caixa e Extrato. Workspaces `Business` tambem exibem Competencia/DRE.

### Fluxo de Caixa Realizado

O Realizado mostra apenas dinheiro que ja entrou ou saiu:

- inclui receitas recebidas e despesas pagas;
- inclui pagamento de fatura de cartao como saida;
- nao inclui pendencias;
- nao inclui compras individuais de cartao como saida de caixa;
- nao considera transferencias internas no resultado consolidado;
- usa a data efetiva do pagamento ou recebimento, com fallback para dados legados.

Exemplo: se uma compra no cartao foi feita em julho e a fatura foi paga em agosto, a saida de caixa aparece em agosto.

### Fluxo de Caixa Previsao

A Previsao combina o que ja aconteceu com o que ainda esta pendente:

- soma realizado + pendentes;
- organiza pendencias pelo vencimento;
- em `Personal`, quando nao ha vencimento, usa a data do lancamento como data de recebimento/vencimento previsto;
- em `Business`, usa o vencimento; a data do lancamento e competencia e nao substitui o vencimento;
- vencimento negativo, data do lancamento ausente ou data civil malformada nao usam fallback silencioso;
- inclui faturas abertas ou fechadas pelo saldo pendente;
- nao duplica compra individual de cartao + fatura;
- mostra uma janela movel com o mes selecionado e os 12 meses seguintes;
- exibe Saldo Projetado para meses atuais ou futuros.

Exemplo: uma receita pendente aparece na Previsao ate ser recebida. Depois disso, passa a aparecer no Realizado.

### Saldo Projetado

O Saldo Projetado estima o saldo futuro:

- parte do saldo atual das contas reais;
- soma valores futuros a receber;
- subtrai valores futuros a pagar e faturas pendentes;
- nao usa contas de cartao como saldo real;
- nao reconstrui historico de saldo passado.

Exemplo: ao selecionar um mes futuro, a previsao mostra o saldo estimado considerando recebimentos, contas e faturas pendentes ate aquele mes.

### Competencia / DRE

A Competencia/DRE esta disponivel em workspaces `Business` e serve para analise gerencial ou contabil:

- usa a data de origem economica do lancamento;
- considera receitas e despesas do periodo, pagas ou pendentes;
- inclui compra no cartao como despesa do periodo da compra;
- exclui pagamento de fatura para evitar duplicidade;
- nao deve ser usada como extrato de caixa.

Exemplo: uma compra no cartao em julho entra na DRE de julho. O pagamento da fatura em agosto nao entra de novo na DRE.

### Extrato

O Extrato mostra apenas movimentos confirmados:

- receitas recebidas;
- despesas pagas;
- pagamentos de fatura ja realizados;
- transferencias entre contas;
- visao de movimentos e resumo diario;
- exportacao em CSV e PDF.

Transferencias aparecem como saida em uma conta e entrada em outra, mas nao distorcem o resultado consolidado.

### Cartao de credito nos relatorios

- No caixa, a compra individual nao sai do caixa no dia da compra.
- No caixa, o pagamento da fatura sai do caixa.
- Na previsao, a fatura pendente entra pelo saldo ainda aberto.
- Na composicao por categoria/macro, os itens da fatura sao abertos para explicar o gasto.
- Na DRE, a compra entra pela data de lancamento e o pagamento da fatura e excluido.

Exemplo: se uma fatura foi paga parcialmente, a parte paga entra no Realizado e o saldo restante continua na Previsao ate ser quitado.

### Exportacoes

As exportacoes seguem a mesma separacao dos relatorios:

- CSV e PDF para Competencia/DRE quando aplicavel;
- CSV e PDF para Fluxo de Caixa Realizado e Previsao;
- CSV e PDF para Extrato de movimentos e resumo diario.

Os PDFs usam a identidade visual do workspace, fonte embutida e tabelas revisadas para acentuacao, paginacao e largura.

## Dinheiro sempre em centavos

Todos os valores monetarios sao armazenados como `int64` em centavos. Nao use `float` para calculo financeiro.

```go
amount := int64(123456) // R$ 1.234,56
```

A conversao para exibicao acontece so na interface.

## Cartoes de credito

### Fatura

No Fluxo de Caixa, a compra no cartao nao sai do caixa no dia da compra. A saida acontece quando a fatura e paga. Na Competencia/DRE, a compra entra no periodo da compra e o pagamento da fatura nao entra novamente.

### Limite disponivel

O limite disponivel segue a regra abaixo:

```text
max(0, creditLimit - invoiceTotal)
```

### Imutabilidade

Faturas pagas nao devem ser editadas ou apagadas. Isso protege o historico financeiro.

## Recorrencias e parcelamentos

Parcelamentos:

- series infinitas usam `TotalInstallments = 0` ou `1`;
- parcelas passadas nao devem ser alteradas;
- para edicoes futuras, a condicao de atualizacao precisa preservar a parcela atual e as seguintes.

Recorrencias versionadas:

- cada lancamento fixo pertence a uma serie (`recurring_series`). As regras da serie ficam em `recurring_rules`, ligadas pelo `series_id`;
- editar **apenas esta** marca a ocorrencia como excecao manual (`is_recurring_exception = 1`). As demais ocorrencias nao mudam e a excecao nao e sobrescrita pela materializacao;
- editar **esta e futuras** cria uma nova regra da mesma serie a partir do ponto do corte. A regra anterior recebe `effective_end_sequence`; a nova regra recebe `parent_rule_id` e um novo `effective_start_sequence`, mantendo o mesmo `series_id`;
- editar **toda a serie** atualiza a unica regra ativa e as ocorrencias materializadas mutaveis da serie. Regras historicas, identidades, faixas, excecoes e lancamentos protegidos permanecem intactos;
- mudancas incompatíveis com um lancamento protegido comum retornam conflito (`409`) antes de qualquer escrita. Excecoes manuais podem divergir do calendario da regra;
- competencia, frequencia e vencimento so podem ser recalculados em uma serie simples selecionada pela sequencia 1. Cadeias versionadas preservam seus calendarios historicos;
- vencimento empresarial usa a relacao civil com a competencia: campo ausente preserva, vazio limpa e uma nova data define o deslocamento em dias para cada ocorrencia;
- notas e anexos nao sao globais e devem ser alterados com **apenas esta**;
- marcar **toda a serie** como paga atualiza regra e mutaveis, limpa vencimentos e aplica saldo uma vez, sem fabricar `paid_at`, data civil de pagamento ou historico;
- excluir **apenas esta** registra uma ausencia persistente por workspace, serie e sequencia antes de apagar a ocorrencia. O materializador respeita esse registro e nao recria o lancamento;
- uma exclusao individual ligada a fatura `CLOSED` ou `PAID` retorna conflito (`409`) sem criar ausencia persistente, apagar o lancamento ou alterar saldo e fatura;
- excluir **esta e futuras** encerra a serie no ponto escolhido e remove apenas as ocorrencias futuras eliminaveis. As ocorrencias apagadas nao sao regeneradas.
- excluir **toda a serie** desativa a serie e suas regras ativas, preserva as regras historicas e mantem pagos, excecoes manuais e itens de faturas `CLOSED` ou `PAID` como historico protegido;

Materializacao automatica:

- o motor central mantem as ocorrencias futuras em uma janela movel de aproximadamente 15 meses;
- o estado por workspace fica em `recurring_materialization_state` (horizonte materializado, lock e throttle);
- a projecao e mantida por um job diario, uma varredura assincrona no boot e um fallback leve por acesso, restrito a Dashboard e Lancamentos. Faturas e Relatorios nao disparam a materializacao;
- o motor e idempotente: rodar a projecao mais de uma vez nao duplica ocorrencias por serie/sequencia nem por regra/sequencia.
- uma sequencia registrada em `recurring_occurrence_skips` e tratada como intencionalmente ausente; o horizonte continua avancando e as demais sequencias continuam sendo materializadas.

## Atomicidade e contencao

As mutacoes financeiras usam uma unica transacao: saldo, historico, fatura, skip de recorrencia e demais efeitos sao confirmados juntos ou revertidos juntos.

- Contencao SQLite detectada no registro do skip ou na remocao das transacoes, antes do commit, retorna `503` com `Retry-After: 1` para a exclusao recorrente. O rollback preserva a transacao, a serie e o skip; nao ha exclusao parcial.
- Falha generica de commit no toggle de pagamento permanece indeterminada (`409`/`429`). Na exclusao, contencao SQLite no commit retorna `409`; erro generico de commit continua `500`.
- Cancelamento de contexto comprovadamente anterior ao commit do driver retorna `408` sem efeito financeiro.
- O sistema nao executa retry financeiro automatico. O operador deve atualizar os dados e repetir a acao de forma consciente.
- A exclusao de apenas uma ocorrencia grava a ausencia persistente e remove o lancamento na mesma transacao; o materializador respeita esse registro e nao recria a ocorrencia.
- Repetir um pagamento ja confirmado nao duplica saldo nem historico; a operacao e idempotente.

## Valores nulos

Campos nulos sao tratados com `COALESCE` ou `sql.NullString`, conforme o caso.

## Rollback de binário e migration 54

A migration 54 (`recurring_occurrence_skips`) é aditiva e sua tabela permanece no banco após executada.

Skips são fatos persistentes de exclusão — uma vez registrados, representam a intenção de manter uma ocorrência ausente, mesmo que o lançamento original tenha sido removido.

Binários anteriores à correção ignoram essa tabela. Depois que existir qualquer skip real, um downgrade para binário antigo que desconhece `recurring_occurrence_skips` pode recriar lançamentos que foram intencionalmente excluídos, porque o materializador não enxergará os skips e preencherá a sequência novamente.

Rollback operacional seguro mantém o schema (incluindo `recurring_occurrence_skips`) e restaura ou reaplica uma versão corrigida que honre a tabela de skips.

Backup WAL-consistente do banco (`sqlite3` com `VACUUM INTO` ou cópia com `PRAGMA wal_checkpoint(TRUNCATE)` seguida de cópia atômica) continua obrigatório antes de qualquer operação de atualização ou rollback.
