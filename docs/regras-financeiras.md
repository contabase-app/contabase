# ContaBase - Regras Financeiras

Esta pagina resume as regras que o sistema usa para calcular valores, faturas e reservas. Ela existe para referencia tecnica e para evitar interpretacoes erradas na operacao diaria.

## Dois modos de uso

- `Personal` atende uso familiar e fluxo de caixa simples.
- `Business` adiciona contas a pagar/receber, faturas, contatos e relatorios de negocio.

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

## Reservas

Cada reserva usa um ledger interno (`box_virtual_ledger`) com entradas e saídas. Os tipos principais são:

| Tipo | Efeito |
|------|--------|
| `RECHARGE` | adiciona fundos |
| `BONUS` | soma valor extra |
| `RELEASE` | libera valor reservado |
| `CONSUME` | consome o valor reservado |
| `REVERSAL` | reverte um consumo anterior |

O alerta de excesso aparece apenas quando o consumo passa do valor reservado.

Administradores podem verificar inconsistências no endpoint interno `/admin/caixinhas/ledger/reconciliar`. O endpoint só diagnostica; ele não altera dados.

## Recorrencias e parcelamentos

Parcelamentos:

- series infinitas usam `TotalInstallments = 0` ou `1`;
- parcelas passadas nao devem ser alteradas;
- para edicoes futuras, a condicao de atualizacao precisa preservar a parcela atual e as seguintes.

Recorrencias versionadas:

- cada lancamento fixo pertence a uma serie (`recurring_series`). As regras da serie ficam em `recurring_rules`, ligadas pelo `series_id`;
- editar **apenas esta** marca a ocorrencia como excecao manual (`is_recurring_exception = 1`). As demais ocorrencias nao mudam e a excecao nao e sobrescrita pela materializacao;
- editar **esta e futuras** cria uma nova regra da mesma serie a partir do ponto do corte. A regra anterior recebe `effective_end_sequence`; a nova regra recebe `parent_rule_id` e um novo `effective_start_sequence`, mantendo o mesmo `series_id`;
- editar **toda a serie** atualiza as ocorrencias mutaveis de uma vez, sem tocar em excecoes manuais, lancamentos pagos ou itens em faturas fechadas/pagas;
- excluir **esta e futuras** encerra a serie no ponto escolhido e remove apenas as ocorrencias futuras eliminaveis. As ocorrencias apagadas nao sao regeneradas.

Materializacao automatica:

- o motor central mantem as ocorrencias futuras em uma janela movel de aproximadamente 15 meses;
- o estado por workspace fica em `recurring_materialization_state` (horizonte materializado, lock e throttle);
- a projecao e mantida por um job diario, uma varredura assincrona no boot e um fallback leve por acesso, restrito a Dashboard e Lancamentos. Faturas e Relatorios nao disparam a materializacao;
- o motor e idempotente: rodar a projecao mais de uma vez nao duplica ocorrencias por serie/sequencia nem por regra/sequencia.

## Valores nulos

Campos nulos sao tratados com `COALESCE` ou `sql.NullString`, conforme o caso.
