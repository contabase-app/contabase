# ContaBase - Regras Financeiras

Esta pagina resume as regras que o sistema usa para calcular valores, faturas e reservas. Ela existe para referencia tecnica e para evitar interpretacoes erradas na operacao diaria.

## Dois modos de uso

- `Personal` atende uso familiar e fluxo de caixa simples.
- `Business` adiciona contas a pagar/receber, faturas, contatos e relatorios de negocio.

## Dinheiro sempre em centavos

Todos os valores monetarios sao armazenados como `int64` em centavos. Nao use `float` para calculo financeiro.

```go
amount := int64(123456) // R$ 1.234,56
```

A conversao para exibicao acontece so na interface.

## Cartoes de credito

### Fatura

As despesas do cartao entram no mes em que a fatura fecha, nao no mes da compra. O ajuste e controlado por `fatura_offset`.

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

- series infinitas usam `TotalInstallments = 0` ou `1`;
- parcelas passadas nao devem ser alteradas;
- para edicoes futuras, a condicao de atualizacao precisa preservar a parcela atual e as seguintes.

## Valores nulos

Campos nulos sao tratados com `COALESCE` ou `sql.NullString`, conforme o caso.
