# Release Notes

Este documento é o índice principal das notas de lançamento do ContaBase.
Para o histórico técnico detalhado, consulte o [CHANGELOG.md](CHANGELOG.md).

## Última Beta publicada

- **v1.0.0-beta.7 — prerelease publicada em 1 de novembro de 2026** — *Cancelamentos, estornos e operabilidade de Investimentos*

  A Beta 7 conclui o ciclo de Investimentos: cancelamento integral de lotes com transferências vinculadas, estorno individual de compras adicionais, cancelamento de registro inicial record-only e aplicações sem transferência automática. Os recibos são imutáveis e idempotentes; compras estornadas saem do principal ativo sem apagar o histórico.

  A migration 82 corrige o P1 em que contas e lotes ficavam bloqueados após um estorno individual. A revalidação histórica de recibos não depende mais de saldos, versões, status ou arquivamento atuais, e a validação no INSERT continua exigindo o estado financeiro completo. O schema avança de 77 para 82 sem rebuild das tabelas financeiras.

  A entrada monetária de cadastro, reaplicação e posição passa a considerar os dois últimos dígitos digitados como centavos: `1644041` exibe `R$ 16.440,41`. Valores brasileiros completos colados mantêm seu significado. Instalações novas nascem com `CONTABASE_FEATURE_INVESTMENTS=false`; atualizações preservam a configuração existente.

  As listagens de Lançamentos, Contas a pagar/receber e Lançamentos dentro das faturas passam a usar cards individuais com agrupamento por data preservado. Transferências mostram a logo da instituição de origem e de destino, com fallback pelo marcador da conta. A dica de swipe volta como rodapé discreto apenas no mobile, orientando confirmação, movimentação de parcelas e exclusão. Faturas e parcelamentos tiveram a movimentação de ocorrências, o escopo por workspace e os gestos de arraste finalizados.

  **Atualização:** `v1.0.0-beta.6` → `v1.0.0-beta.7`. Faça backup antes de atualizar. As migrations 79 a 82 são transacionais e não reconstroem as tabelas financeiras. A política de recuperação automática continua restrita à Beta 6; a Beta 7 não tem downgrade automático nem restore automático de banco.

  **P2 conhecido:** a movimentação de recorrências legadas pode ignorar ocorrências em faturas PAID. Não corrigido nesta versão. Adoção histórica e resgates financeiros das novas aplicações permanecem indisponíveis. A recuperação automática de atualização para a linha Beta 7 exige revisão específica em versão futura.

  **Distribuição:** tag `v1.0.0-beta.7`, GitHub Release prerelease, artifacts Linux amd64 e arm64 com checksums e imagem GHCR `v1.0.0-beta.7`. A promoção do canal Beta e da imagem `:beta` acontece somente após o smoke real de instalação e atualização desta tag.

  Veja as [notas da versão](docs/releases/v1.0.0-beta.7.md).

## Beta 6 publicada

- **v1.0.0-beta.6 — prerelease publicada** — *Investimentos, interface e estabilidade*

  O menu de ações em Metas e Reservas permanece legível acima dos cartões vizinhos. A página de Faturas abre no primeiro mês com fatura em aberto quando o mês atual já está pago; uma competência escolhida manualmente continua prevalecendo.

  Investimentos fica disponível por padrão quando a flag do módulo está ausente, em instalações novas e atualizadas. Desativações explícitas são preservadas. Novas aplicações permitem selecionar a origem e registrar compra, transferência e saldos na mesma operação atômica. O histórico mostra a origem e o lançamento correspondente; repetir a requisição não duplica o débito. O executor de pagamentos agendados evita a disputa entre writers SQLite durante o startup.

  Relatórios, Contatos, Configurações e Administração receberam componentes Premium e ajustes de navegação, formulários e acessibilidade. As microfases locais alinham conteúdo e ações de Configurações, corrigem máscaras, contraste e áreas de toque e acrescentam o controle global de privacidade a Contas, Investimentos e Notificações. A autorização de auditoria, o tratamento de credenciais SMTP e as proteções de exportação também foram reforçados.

  A contagem de dias para o fechamento de faturas passou a usar datas civis nos cartões e no Dashboard, eliminando a divergência de um dia na virada do dia, do mês ou do ano.

  **Segurança e recuperação.** A identidade de seed legada não é mais reativada automaticamente: credenciais públicas ou inválidas são inutilizadas e a conta exige recuperação administrativa explícita, que preserva os fatores 2FA cadastrados. `SECURITY_MASTER_KEY` passa a ser obrigatória e explícita para gravar senha SMTP; não há fallback público. Senhas cifradas com a chave antiga deixam de ser lidas automaticamente, mas o ciphertext é preservado e pode ser substituído pela Administração. Os fluxos de atualização só retornam automaticamente a um código ou imagem verificado da própria Beta 6, nunca à Beta 5, e bloqueiam o retorno após qualquer tentativa de startup da candidata. Não há restore automático de banco, schema, configuração ou chaves.

  O fluxo usa schema 77. Adoção histórica e resgates financeiros das novas aplicações continuam indisponíveis; as três flags de operações financeiras controladas permanecem opt-in. Registros patrimoniais legados não recebem débito retroativo, e a política existente de transferências continua permitindo saldo negativo. Consulte o [guia de Investimentos](docs/investimentos.md).

  **Atualização prevista:** `v1.0.0-beta.5` → `v1.0.0-beta.6`. Faça backup antes de atualizar, preserve `SECURITY_MASTER_KEY` e revise contas legadas que dependam de recuperação administrativa. O upgrade operacional pelo instalador público ainda não foi comprovado integralmente.

  **Distribuição:** Beta 6 publicada. Os detalhes acima preservam o registro da preparação dessa versão.

## Beta 5 publicada

- **v1.0.0-beta.5 — prerelease publicada** — *Home renovada e identidade visual*

  A Home/Dashboard ganhou apresentação renovada dos resumos financeiros, Limites e Metas. Os cartões compactos usam ícones e barras de progresso para facilitar a leitura.

  Metas e Limites receberam cartões e formulários reorganizados. O novo sistema de ícones permite escolher símbolos para categorias, limites e reservas; a migration 66 acrescenta as chaves necessárias e normaliza categorias antigas sem escolha explícita. O resumo de Lançamentos apresenta as situações e o resultado líquido com layout responsivo.

  A identidade visual foi atualizada no shell, nas telas de acesso e nos assets do PWA. A edição dos metadados de uma conta mantém o saldo apenas para leitura no formulário.

  **Atualização:** `v1.0.0-beta.4` → `v1.0.0-beta.5`. Faça backup antes de atualizar.

  **Distribuição:** tag e GitHub Release prerelease publicadas; canal Beta e imagem GHCR `:beta` promovidos para `v1.0.0-beta.5`.

## Marco anterior

- **v1.0.0-beta.4 — publicada em 13 de setembro de 2026** — *Remediação pré-produção e correções financeiras*

  Esta candidata inicia a correção faseada da auditoria de segurança. Anexos de lançamentos recebem extensão canônica a partir do conteúdo reconhecido, sem confiar no nome enviado. Todos os downloads de anexos, inclusive os já gravados, passam a ser binários forçados com `attachment` e `nosniff`, bloqueando a interpretação de HTML no origin da aplicação.

  A segunda fase exige reautenticação com senha atual e, quando existente, o TOTP já cadastrado para configurar, trocar, desativar MFA ou regenerar códigos de recuperação. O segredo de setup passou para um desafio cifrado e de uso único no servidor. Sessões e pré-autenticações recusam usuários pendentes, e alterações de senha ou MFA revogam outros artefatos de acesso atomicamente. Códigos TOTP não podem concluir duas pré-autenticações no mesmo intervalo.

  A terceira fase estabelece deadlines HTTP finitos e bloqueia corpos multipart de lançamentos acima de 11 MiB antes do parsing, mantendo o limite de 10 MiB por arquivo. A origem usada em logs e limites passa a vir somente da cadeia `X-Forwarded-For` emitida por um proxy direto configurado, percorrida do hop mais próximo para o cliente. Trocas de senha recebem limites por usuário e por usuário/IP antes do bcrypt.

  A quarta fase atualiza o módulo, os builds Docker e os artifacts para Go 1.26.6. As dependências `x/text`, `x/crypto`, `x/image` e transitivas receberam versões corrigidas; o gate de Release executa um scanner de vulnerabilidades fixado e falha se o binário Linux tiver sido compilado por toolchain diferente da declarada.

  A quinta fase preserva o usuário e o workspace autenticados na auditoria de operações de segurança. A identidade é transportada por um contexto interno criado antes da autenticação, inclusive quando a requisição troca de contexto, e nunca por cabeçalhos enviados pelo cliente.

  A sexta fase uniformiza o custo principal do login inválido. Contas existentes, inexistentes e bloqueadas passam pela verificação bcrypt e por uma consulta de lockout antes da mesma resposta genérica, sem preencher a tabela persistente com identidades controladas por terceiros.

  A sétima fase homologa RBAC e isolamento de workspace com a matriz fixa de `ADMIN`, `MANAGER` e `USER`. A troca de workspace usa a própria rota registrada no servidor, deriva vínculo e papel do banco e recusa workspace alheio, cookie de papel forjado e membership removido. Os handlers de administração de workspaces agora repetem internamente a exigência de `ADMIN` para criação, edição e exclusão.

  A oitava fase fecha duas superfícies HTTP de endurecimento. Fotos de perfil exigem autenticação e somente são entregues quando pertencem ao workspace ativo. A seed B2B de desenvolvimento deixa de alterar dados por `GET`: sua execução exige `POST`, CSRF válido, permissão, limite por usuário/workspace e `APP_DEBUG=true`.

  A nona fase limita a vida total das sessões mesmo quando há atividade contínua. Sessões comuns mantêm a janela renovável de 24 horas, com teto absoluto de sete dias; “manter conectado” mantém a janela de 30 dias, com teto de noventa dias. Sessões antigas além do limite são recusadas e têm o cookie removido.

  A décima fase exige confirmação recente da identidade antes de exportar ou importar backup e de alterar usuários, workspaces ou configuração da auditoria. A janela dura dez minutos por sessão e é renovada somente com a senha atual e, quando habilitado, o TOTP atual. Sessões anteriores à atualização continuam utilizáveis, mas precisam dessa confirmação para executar as operações protegidas.

  A décima primeira fase torna efetiva a política de scripts do navegador. Somente JavaScript externo servido pelo próprio origin é aceito; scripts inline, `eval`, atributos como `onclick`, URLs `javascript:`, frames e objetos são bloqueados. A cobertura percorre os templates embarcados e os arquivos Go de produção que emitem HTML. Estilos inline permanecem temporariamente permitidos para compatibilidade com temas e valores visuais dinâmicos.

  A décima segunda fase limita a memória usada pelo rate limit. O processo mantém no máximo 10.000 identidades, guarda somente hashes de tamanho fixo e recusa novas identidades quando todos os buckets ainda estão ativos, sem expulsar a proteção de alvos existentes. Instalações com múltiplas réplicas precisam aplicar um limite compartilhado no proxy ou edge, pois os buckets da aplicação não atravessam processos.

  A décima terceira fase executa a imagem Docker local atrás de um proxy TLS confiável, com configuração de produção e dados totalmente descartáveis. As 45 verificações cobrem fronteira HTTPS/Host, cookies, CSP, RBAC, isolamento entre workspaces, fotos, CSRF, anexos/XSS, limite de upload, rate limit, reautenticação administrativa, backup e trilhas de auditoria. O teste em navegador confirmou login, Dashboard e seletor de workspace sem scripts inline, atributos executáveis, URLs `javascript:` ou erros no console. Durante o smoke, dois defeitos adicionais foram encontrados e corrigidos: headers internos `X-SecOps-*` não chegam mais ao cliente, e um redirecionamento para reautenticação não é mais registrado como backup exportado.

  A décima quarta fase inicia a retirada segura de CSS inline. A CSP agora permite elementos de estilo apenas quando vêm de arquivos do próprio origin; os três blocos `<style>` existentes foram movidos para os stylesheets oficiais, e o HTMX foi configurado para não criar CSS embutido no navegador. A exceção `unsafe-inline` ficou restrita aos atributos `style` usados por cores, temas, barras e animações calculadas. O smoke real confirmou Dashboard, formulário, seletor e tela de bloqueio HTTP sem erros ou violações.

  A décima quinta fase transfere 69 atributos de estilo estáticos de templates para classes semânticas nos CSS oficiais. Avisos, ícones, painéis de estado e legendas mantêm a aparência e o contraste nos dois temas. O controle de categorias troca a rotação inline por uma classe manipulada pelo script externo. A regressão agora rejeita todo novo atributo `style` estático, com exceção explícita dos três fallbacks `display:none` que o JavaScript usa para revelar as iniciais quando o carregamento de avatar falha.

  A décima sexta fase elimina essa exceção de fallback e as escritas de `style.display` dos caminhos migrados. Avatar, pesquisa de categorias, filtros de macro, barra em lote e modais globais agora usam as classes `hidden` e `flex`; estados finitos de vencimento, categoria, contato e ícone de fatura vêm dos CSS externos. A regressão passa a rejeitar todo atributo `style` estático, sem exceções.

  A décima sétima fase remove os 12 atributos de cor das legendas dos gráficos de relatórios. O template passa a usar `data-report-stroke`, um atributo inerte associado apenas à paleta fechada emitida pelo servidor, e o helper que marcava CSS como confiável deixa de existir. Uma cor não prevista não recebe regra no stylesheet e não pode criar CSS arbitrário.

  As fases seguintes concluem a retirada de CSS inline: temas passam para estados `data-*`, barras usam `<progress>` nativo, e as 48 cores configuráveis são resolvidas por uma folha autenticada que aceita somente hexadecimal estrito e consulta o workspace ativo. Swipe, descarte de notificações e tooltip preservam movimentos calculados com a Web Animations API, sem escrever em `element.style`. A CSP agora aplica `style-src-attr 'none'`; testes de regressão rejeitam atributos de estilo em templates e escritas inline nos scripts de produção. O script antigo do banner PWA, sem inclusão ou elementos correspondentes, foi removido.

  **Correções funcionais posteriores.** A candidata também reúne as correções concluídas depois da remediação de segurança. Editar os metadados de uma conta não altera mais o saldo, e a conciliação passa a ser uma operação própria, com histórico imutável de saldo anterior, posterior, diferença, autor e data, controle de versão otimista e sem criar lançamento, receita, despesa ou vínculo de fatura. O fechamento e a reconstrução histórica consideram esses ajustes, revertendo os posteriores ao período consultado. Recorrências deixam de ser excluídas por ações em lote: a seleção em massa bloqueia essas ocorrências em vez de removê-las, preservando a série e o histórico.

  O rateio de faturas passa a considerar reembolsos ativos — o integral remove o item, o parcial reduz o peso e o geral é distribuído sobre os pesos líquidos —, e os somatórios de relatórios, a alocação de pagamentos e os agregados de saldo falham de forma explícita quando o total excede o intervalo representável, em vez de exibir valor incorreto. A exportação responde com erro sem iniciar o download.

  O backup de banco SQLite online passa a usar o snapshot coordenado do próprio SQLite, com origem somente leitura, publicação atômica e comportamento fail-closed: um arquivo cuja consistência não pode ser garantida não é publicado como válido. A aplicação de migrations antigas também foi corrigida para preservar os dados existentes. As sugestões de descrição voltam a funcionar no shell atual e encontram descrições antigas antes ignoradas, e o toggle de pagamento foi endurecido contra contenção do banco.

  **Escopo restante:** domínio, certificado, firewall, proxy/edge compartilhado, instalação e atualização precisam ser homologados na infraestrutura real antes da publicação. O rate limit global em múltiplas réplicas continua dependente do proxy ou edge compartilhado. A homologação visual desktop/mobile das telas financeiras alteradas, o smoke autenticado das conciliações e os smokes de atualização continuam pendentes.

  **Distribuição:** a beta.4 foi publicada como prerelease e permanece como versão do canal Beta durante a preparação da beta.5.

## Marco anterior à beta.4

- **v1.0.0-beta.3 — preparação local — 2 de setembro de 2026** — *Shell V2 definitivo e identidade visual das instituições*

  A terceira candidata da linha 1.0 promove a interface autenticada para um único shell canônico nas rotas definitivas. Dashboard, lançamentos, metas, contas, relatórios, contatos, faturas, notificações, ajuda e configurações compartilham navegação, tema, workspace e ações globais. O layout anterior, seu CSS sem consumidores e a rota temporária `/dashboard-v2` foram removidos; o aviso de segurança do token de setup e o rodapé com versão/timezone foram preservados no shell atual.

  Contas e cartões recebem um catálogo global de instituições com logos embarcados e um seletor pesquisável adequado ao mobile. As imagens já incluem o fundo final e agora seguem o mesmo arredondamento em Dashboard, Configurações, Faturas, Lançamentos e seletores do formulário. Instituições fora do catálogo continuam suportadas por "Outra" e fallback textual, sem upload arbitrário.

  O Dashboard consolida o redesenho de cartões e faturas nos temas claro e escuro, sem estilos especiais por instituição. A navegação lateral passa a usar três blocos compactos, rolagem interna discreta e controles de workspace, tema e notificações no rodapé. Workspaces pessoais e Business têm ícones distintos e mantêm a mesma cobertura de páginas conforme permissões.

  **Central de Faturas.** A nova rota `/faturas` reúne os cartões do workspace em um carrossel e troca o detalhe da fatura selecionada na própria página. A competência é global, a seleção preserva a URL e as rotas individuais continuam válidas para links diretos. O seletor usa uma faixa contínua de 12 meses — inclusive na virada de ano — preenchendo o desktop e centralizando o mês ativo no mobile.

  A seleção do cartão agora é estável: o Dashboard resolve a competência da fatura escolhida ao abrir a Central, a troca de mês mantém o mesmo cartão e a ordem do carrossel segue a prioridade de pagamento do Dashboard. No mobile, o trilho vertical do Painel ficou mais compacto e o cabeçalho reforça menu e marca sem transformar o hambúrguer em um botão visualmente pesado.

  **Competência contínua.** Lançamentos, Contas, Relatórios e o detalhe individual de Fatura usam agora a mesma faixa de doze meses, com ano correto na virada e período ativo centralizado no mobile. Cada tela preserva seus próprios filtros e contexto ao trocar a competência; o detalhe de Fatura também passou a reutilizar o comportamento global em vez de manter JavaScript duplicado.

  A candidata também reúne as correções financeiras posteriores à beta.2: datas civis seguem a timezone configurada, compras feitas no próprio dia do fechamento vão para a fatura seguinte e a edição "Este e futuros" aceita alterar a ocorrência atual paga e sua cauda mutável sem apagar histórico nem relaxar as proteções de fatura.

  A revisão final também corrige o estado vazio do seletor de instituições, impedindo que uma imagem sem `src` apareça como arte quebrada, e centraliza a coluna da Central de Notificações nos mesmos limites das demais páginas.

  **Upgrade pretendido:** `v1.0.0-beta.2` → `v1.0.0-beta.3`.

  **Validação:** a preparação local inclui testes direcionados de renderização, logos, shell, datas civis, faturas e recorrências, além da suíte canônica `TZ=UTC go test ./... -count=1`, vet, build e higiene do diff. Release Preflight, tag, artifacts, GHCR, smokes de instalação/atualização e avanço do canal permanecem gates separados.

  **Distribuição:** nenhuma publicação é feita nesta preparação. A tag imutável, o prerelease, os artifacts e o canal Beta só podem avançar após autorização explícita e execução do runbook.

## Histórico

- **v1.0.0-beta.2 — 25 de agosto de 2026** — *Segunda candidata da linha 1.0, publicada*

  Consolidou o monograma `CB`, confirmação futura sem efeito antecipado no saldo, snapshots diários protegidos, resumos financeiros corrigidos e ajustes de recorrências e lançamentos no mobile. A tag, a Release, os artifacts, a imagem fixa e o canal Beta foram materializados; novas correções avançam para outra versão.

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

- **v0.1.0-alpha.1** — *03 de junho de 2026*

  Primeira release pública de validação, preservada como registro histórico.
