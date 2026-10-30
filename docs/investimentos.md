# Investimentos

Este guia descreve o código da candidata `v1.0.0-beta.6`, ainda não publicada. A disponibilidade no canal de instalação depende da versão distribuída.

## Disponibilidade

Investimentos fica disponível por padrão em instalações novas e atualizadas quando `CONTABASE_FEATURE_INVESTMENTS` está ausente do ambiente do processo. Não é necessário acrescentar variáveis ao arquivo de configuração. A mesma regra vale para workspaces Personal e Business; sessão, permissões e isolamento por `workspace_id` continuam obrigatórios.

| Configuração explícita | Resultado |
| --- | --- |
| Ausente | Habilitado |
| `true` ou `1` (sem distinguir maiúsculas e espaços externos) | Habilitado |
| `false` ou `0` | Desabilitado |
| Vazio, somente espaços ou valor desconhecido | Desabilitado, por compatibilidade |

Uma configuração vazia antiga não permite saber se houve desativação deliberada. Por isso ela continua desabilitando o módulo, sem sobrescrita automática. Desabilitar a flag oculta o módulo e não apaga investimentos. Mudanças no ambiente exigem reiniciar/recriar o processo para reconstruir as rotas.

### Configuração no Docker

No Compose fornecido pelo projeto, `.env.docker` é a fonte da flag. A ausência da variável nesse arquivo habilita o módulo; `false`, `0` ou vazio continuam desabilitando. Não é necessário acrescentar uma variável para habilitar o padrão.

A candidata remove o repasse implícito de `CONTABASE_FEATURE_INVESTMENTS` pelo shell. Comandos como `CONTABASE_FEATURE_INVESTMENTS=false docker compose up -d` deixam de configurar essa flag no container. Se a desativação era feita somente dessa forma, registre `CONTABASE_FEATURE_INVESTMENTS=false` em `.env.docker` antes da atualização para mantê-la. Um valor explícito em `environment` de um `docker-compose.override.yml` local continua prevalecendo sobre o arquivo, conforme o contrato do Compose. Essa mudança não altera flags de processos binários/systemd.

## Aplicações e posição

No formulário, escolha uma conta de investimento como destino e uma conta corrente, poupança ou carteira ativa do mesmo workspace como origem. Use a aplicação para dinheiro que ainda não foi transferido. Compra, transferência paga e efeitos nos dois saldos são gravados atomicamente; o histórico identifica origem e lançamento. Reenviar a mesma solicitação não duplica os efeitos.

O total de compras é distinto da última posição manual e da rentabilidade estimada. A posição conhecida pode ter cobertura parcial ou estar desatualizada; não representa dinheiro disponível nem patrimônio completo. O modo privacidade oculta a apresentação dos valores e não é controle de acesso nem criptografia.

As transferências mantêm a política existente de saldo, inclusive saldo negativo permitido. Registros patrimoniais legados não recebem débito retroativo; não use a nova aplicação para reconciliar dinheiro que já foi transferido. Chamadas legadas sem origem preservam o contrato patrimonial; reaplicações sem origem em inventário já financiado são recusadas.

## Limites da candidata

- O fluxo de aplicações usa schema 77; a migration experimental 78 não integra o startup.
- Adoção histórica e resgates financeiros das novas aplicações permanecem indisponíveis. As proteções do backend recusam as saídas não homologadas desse inventário.
- `CONTABASE_FEATURE_INVESTMENT_OPERATIONS`, `CONTABASE_FEATURE_INVESTMENT_SETTLEMENTS` e `CONTABASE_FEATURE_INVESTMENT_SETTLEMENT_EFFECTS` continuam desabilitadas por padrão e exigem `true` ou `1`, além das flags anteriores na hierarquia. São controles de operações financeiras, não pré-requisitos para cadastro e aplicações com origem. Habilitá-las não homologa adoção histórica ou saídas das novas aplicações.
- Não altere por SQL os comprovantes gerados nem use código antigo para editar esses vínculos.

Faça backup antes de atualizar e siga o [guia de atualização](atualizacao.md). A disponibilização do módulo não executa reconciliação, aplicação financeira ou transformação retroativa dos dados.
