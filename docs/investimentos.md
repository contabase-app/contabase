# Investimentos

Este guia descreve o código da `v1.0.0-beta.7`, publicada como prerelease. A disponibilidade no canal de instalação depende da promoção da versão distribuída.

## Disponibilidade

Em instalações novas pelos instaladores oficiais Docker, source e release binário, o arquivo de ambiente é criado com `CONTABASE_FEATURE_INVESTMENTS=false`; o módulo começa desativado. Instalações existentes são preservadas: os atualizadores não acrescentam nem sobrescrevem a variável. Se ela estiver ausente, o comportamento legado continua valendo (habilitado); se estiver explícita, o valor existente é mantido. A mesma regra vale para workspaces Personal e Business; sessão, permissões e isolamento por `workspace_id` continuam obrigatórios.

| Configuração explícita | Resultado |
| --- | --- |
| Ausente | Habilitado |
| `true` ou `1` (sem distinguir maiúsculas e espaços externos) | Habilitado |
| `false` ou `0` | Desabilitado |
| Vazio, somente espaços ou valor desconhecido | Desabilitado, por compatibilidade |

Uma configuração vazia antiga não permite saber se houve desativação deliberada. Por isso ela continua desabilitando o módulo, sem sobrescrita automática. Desabilitar a flag oculta o módulo e não apaga investimentos. Mudanças no ambiente exigem reiniciar/recriar o processo para reconstruir as rotas.

Para ativar manualmente, defina `CONTABASE_FEATURE_INVESTMENTS=true` no arquivo de ambiente da instância e reinicie ou recrie o serviço. A flag é lida no startup, durante o registro das rotas, e não é aplicada em tempo real. A configuração é por instância, não por workspace.

### Configuração no Docker

No Compose fornecido pelo projeto, `.env.docker` é a fonte da flag. Em instalações novas, o exemplo já contém `CONTABASE_FEATURE_INVESTMENTS=false`. Em instalações existentes, `.env.docker` é preservado: a ausência da variável mantém o comportamento legado habilitado; `true` ou `false` explícitos permanecem como estão. Para ativar, defina `true` e recrie o serviço.

Esta versão remove o repasse implícito de `CONTABASE_FEATURE_INVESTMENTS` pelo shell. Comandos como `CONTABASE_FEATURE_INVESTMENTS=false docker compose up -d` deixam de configurar essa flag no container. Se a desativação era feita somente dessa forma, registre `CONTABASE_FEATURE_INVESTMENTS=false` em `.env.docker` antes da atualização para mantê-la. Um valor explícito em `environment` de um `docker-compose.override.yml` local continua prevalecendo sobre o arquivo, conforme o contrato do Compose. Em binário/systemd, o arquivo é `/etc/contabase/contabase.env`.

## Aplicações e posição

Na Beta 7, ao digitar números nos campos de criação, reaplicação e posição, os dois últimos são centavos: `3573922` exibe `R$ 35.739,22`. Cole valores em reais, como `35.739,22` ou `R$ 35.739,22`; inteiros colados ou restaurados pelo servidor também são reais. Sem JavaScript, informe reais com vírgula decimal, como orienta o formulário. Posição aceita zero; criação e reaplicação exigem valores positivos. Essa alteração não corrige registros anteriores.

No formulário, escolha uma conta de investimento como destino. Para aplicação com transferência automática, escolha também uma conta corrente, poupança ou carteira ativa do mesmo workspace como origem: a compra, a transferência paga e os efeitos nos dois saldos são gravados atomicamente, e o histórico identifica origem e lançamento. Para dinheiro que já está na conta de investimento, escolha a modalidade sem movimentação automática (record-only): a compra é registrada no histórico sem criar transferência e sem alterar saldos, inclusive em lote financiado. Reenviar a mesma solicitação não duplica os efeitos.

O total de compras é distinto da última posição manual e da rentabilidade estimada. A posição conhecida pode ter cobertura parcial ou estar desatualizada; não representa dinheiro disponível nem patrimônio completo. O modo privacidade oculta a apresentação dos valores e não é controle de acesso nem criptografia.

As transferências mantêm a política existente de saldo, inclusive saldo negativo permitido. Registros patrimoniais legados não recebem débito retroativo; não use a nova aplicação para reconciliar dinheiro que já foi transferido. Reaplicações sem origem em inventário já financiado são aceitas como registro record-only, sem transferência e sem efeito de saldo.

### Cancelamento e estorno

O cancelamento integral do investimento com transferências vinculadas é elegível quando o contrato da schema 79 permite: a compensação inverte as transferências originais, preserva o histórico e grava recibo imutável. O cancelamento não é um estorno parcial.

Compras adicionais (não iniciais) podem ser estornadas individualmente. Em compra com transferência automática, o estorno cria uma transferência compensatória entre as mesmas contas; em compra record-only, não há movimentação financeira. Em ambos os casos a compra permanece no histórico com status Estornada, fora do principal ativo, e o recibo é imutável e idempotente. As demais compras não são afetadas.

O lote inicial sem transferência automática pode ser cancelado pelo fluxo de registro record-only (schema 81), sem movimentação financeira e com recibo imutável. O estorno individual continua restrito a compras adicionais.

Após um estorno individual, contas e lote não ficam congelados: movimentos, renomeação e transições de ciclo de vida permitidas pelas regras existentes continuam funcionando (schema 82).

## Limites da versão

- O fluxo de aplicações usa schema 82; a migration experimental 78 não integra o startup.
- Adoção histórica e resgates financeiros das novas aplicações permanecem indisponíveis. As proteções do backend recusam as saídas não homologadas desse inventário.
- A compra inicial não é elegível ao estorno individual. O lote inicial record-only pode ser cancelado pelo fluxo próprio da schema 81; não há estorno individual da compra inicial.
- `CONTABASE_FEATURE_INVESTMENT_OPERATIONS`, `CONTABASE_FEATURE_INVESTMENT_SETTLEMENTS` e `CONTABASE_FEATURE_INVESTMENT_SETTLEMENT_EFFECTS` continuam desabilitadas por padrão e exigem `true` ou `1`, além das flags anteriores na hierarquia. São controles de operações financeiras, não pré-requisitos para cadastro e aplicações com origem. Habilitá-las não homologa adoção histórica ou saídas das novas aplicações.
- Não altere por SQL os comprovantes gerados nem use código antigo para editar esses vínculos.

Faça backup antes de atualizar e siga o [guia de atualização](atualizacao.md). A disponibilização do módulo não executa reconciliação, aplicação financeira ou transformação retroativa dos dados.
