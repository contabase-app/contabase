# Design System Premium Fintech do ContaBase

Documento de referência para evoluir o restante do ContaBase a partir da
identidade aprovada no Dashboard. Ele descreve a fundação canônica, os
componentes compartilhados e como migrar sem misturar redesign com regra
financeira.

> **Estado atual (consolidação do shell):** a fundação global está em
> `assets/css/ui-tokens.css`, carregada depois de `app-shell.css` e antes de
> qualquer CSS de página. A referência de cores estruturais é a página
> `/contas` (paleta do shell autenticado). O acabamento dos componentes Premium
> continua sendo o Dashboard. O Dashboard não redefine mais canvas, sidebar,
> superfícies, bordas, texto, acento ou semântica; ele apenas mantém os tokens
> exclusivos da sua composição (categorias, cartões, medidores).
>
> **Shell é fonte única:** sidebar, item ativo, botão de recolher, appbar,
> bottom navigation, FAB e seletor de workspace são definidos apenas em
> `app-shell.css`, consumindo os tokens `--ui-*`. Nenhum CSS de página redefine
> a geometria, o estado ativo ou os controles do shell. Ver seção 20
> (Contratos Canônicos de Componentes).
>
> **Estado da candidata Beta 6:** Metas, Relatórios, Contatos, Configurações e
> Administração já receberam suas fases Premium; ver a matriz da seção 21.3.
> As seções 16, 17, 18 e 21.2 registram pendências do marco de consolidação do
> shell e devem ser lidas como histórico quando superadas pelas fases de página.
> As microfases de 29–30/10/2026 alinham Configurações/Administração, corrigem
> máscaras, contraste, áreas de toque e nomes acessíveis e estendem o controle
> global de privacidade a Contas, Investimentos e Notificações. São mudanças do
> checkout candidato, sem implicar publicação da Beta 6.

## 1. Princípios visuais

O ContaBase é um cofre financeiro privado: claro, silencioso e legível. A
identidade Premium Fintech aprovada no Dashboard segue estes princípios:

1. **Gray-on-gray:** camadas de cinza frio em vez de preto puro; a hierarquia
   vem de superfície, borda e peso tipográfico.
2. **Acento cirúrgico:** o violeta indica ação, seleção e foco. Ele não decora
   cards estruturais nem representa entrada/saída de dinheiro.
3. **Cor financeira com significado:** verde, vermelho, âmbar e azul só entram
   quando há estado financeiro definido (saldo, atraso, limite, pendência).
4. **Valor monetário primeiro:** números usam `tabular-nums`, alinhamento
   estável e contraste alto; rótulos e metadados ficam discretos.
5. **Densidade controlada:** listas compactas, separadores finos e poucos
   cards aninhados.
6. **Flat por padrão:** sombra é resposta funcional (hover, modal, overlay),
   não decoração permanente.
7. **Movimento funcional:** 140–180 ms, sem bounce, com
   `prefers-reduced-motion`.
8. **Privacidade visível:** valores sensíveis usam `data-sensitive` e o modo de
   privacidade existente; nenhum componente novo deve criar telemetria.

## 2. Paleta canônica

A paleta foi dividida em dois papéis, para não misturar base visual com
identidade de componente:

- **Estrutural:** fundo, sidebar, superfícies, bordas, textos e trilhos. A
  referência é a página `/contas` (valores renderizados pelo shell). Esses
  valores vivem em `app-shell.css` (`--v2-*`) e são expostos como `--ui-*`.
- **Marca e semântica:** acento violeta e estados financeiros. A referência é a
  identidade Premium aprovada no Dashboard. Os valores claros já eram iguais;
  no escuro convergem para os tons do Dashboard.

### 2.1 Paleta estrutural (referência `/contas`)

| Papel | Dark | Light |
| --- | --- | --- |
| Canvas | `#161a20` | `#f3f4f6` |
| Sidebar | `#181d24` | `#fafbfc` |
| Surface | `#1e242d` | `#ffffff` |
| Surface raised | `#232a34` | `#f7f8fa` |
| Surface hover | `#29313c` | `#eef1f5` |
| Border | `#2c3542` | `#e1e5eb` |
| Border strong | `#3d4858` | `#cbd3dd` |
| Text | `#eef1f5` | `#1b2430` |
| Muted | `#a3acb9` | `#556274` |
| Subtle | `#8b95a4` | `#657183` |
| Detail surface | `#1b2129` | `#f7f9fb` |
| Progress track | `#303a47` | `#dde4ec` |

O Dashboard mantém apenas um gradiente radial discreto de profundidade sobre o
canvas canônico. Ele não é uma segunda paleta e não altera a cor base.

### 2.2 Acento e semântica (identidade Premium)

| Papel | Dark | Light |
| --- | --- | --- |
| Accent | `#8b5cf6` | `#6d4de0` |
| Accent strong | `#a78bfa` | `#5b2fcc` |
| Accent soft | `#241f3d` | `#eee9fb` |
| Positive | `#34d399` | `#0f7a52` |
| Danger | `#fb7185` | `#c04355` |
| Warning | `#fbbf24` | `#9a6500` |
| Info | `#60a5fa` | `#1e6fa8` |

O violeta é a única cor de marca. Ele indica ação, seleção e foco; não representa
entrada/saída de dinheiro. Verde, vermelho, âmbar e azul só aparecem com estado
financeiro definido.

### 2.3 O que mudou em relação ao Dashboard isolado

| Papel | Antes (Dashboard) | Agora (canônico) |
| --- | --- | --- |
| Canvas dark | `#111322` | `#161a20` |
| Canvas light | `#eef1f7` | `#f3f4f6` |
| Surface dark | `#1a1d2e` | `#1e242d` |
| Surface raised dark | `#242840` | `#232a34` |
| Border dark | `#2e3552` | `#2c3542` |
| Text dark | `#f3f4f8` | `#eef1f5` |

O acento e os tons semânticos do Dashboard foram preservados. A página `/contas`
passou a consumir o mesmo acento, em vez de manter um violeta ligeiramente
diferente.

### 2.4 Compatibilidade com o design system legado

Dentro de `.cb-v2`, os tokens legados `--cb-*` são pontes para os canônicos:
`--cb-surface-1` → `--ui-surface`, `--cb-accent-solid` → `--ui-action-bg`, etc.
Isso permite que componentes antigos (`.cb-input`, `.cb-button`, `.cb-tabs`,
chips e utilitários Tailwind arbitrários) herdem a fundação sem reescrever
markup. Fora de `.cb-v2` (login, ativação, recibo standalone), os valores
legados permanecem; a migração dessas superfícies é uma fase própria.

**Não criar uma quarta paleta.** Se um componente precisa de um tom novo,
primeiro verifique se ele não é apenas uma mistura de `--ui-accent`,
`--ui-border`, `--ui-surface` ou de um tom semântico.

## 3. Tokens fundamentais

A matéria-prima tem três camadas, nesta ordem:

1. `--v2-*` em `app-shell.css`: valores primitivos e theme-aware do shell.
2. `--ui-*` em `ui-tokens.css`: vocabulário canônico consumido por páginas e
   componentes. Nenhuma página redefine `--ui-*`.
3. `--cb-*` dentro de `.cb-v2`: ponte de compatibilidade para o legado.

| Grupo | Tokens canônicos | Observação |
| --- | --- | --- |
| Estrutura | `--ui-canvas`, `--ui-sidebar`, `--ui-surface`, `--ui-surface-raised`, `--ui-surface-hover`, `--ui-border`, `--ui-border-strong` | Aliases de `--v2-*`; referência `/contas`. |
| Texto | `--ui-text`, `--ui-text-muted`, `--ui-text-subtle` | `subtle` é o menor contraste permitido para metadados. |
| Marca | `--ui-accent`, `--ui-accent-strong`, `--ui-accent-soft`, `--ui-focus`, `--ui-action-bg`, `--ui-action-fg` | Violeta institucional; `action-bg` é mais escuro para AA com texto branco. |
| Semântica | `--ui-success`, `--ui-warning`, `--ui-danger`, `--ui-info` | Sempre acompanhados de rótulo/sinal/ícone. |
| Superfície de campo | `--ui-field-bg`, `--ui-field-border` | Inputs, selects, textarea e busca. |
| Abas | `--ui-tab-bg`, `--ui-tab-active-bg`, `--ui-tab-active-fg`, `--ui-tab-active-border` | Container neutro; ativo com superfície violeta perceptível. |
| Mês | `--ui-month-active-bg`, `--ui-month-active-fg`, `--ui-month-active-border` | Uma única versão para Contas, Lançamentos, Relatórios e Faturas. |
| Geometria | `--ui-radius-panel/card/control/button/tab/tab-item/badge/pill`, `--ui-radius-tile`, `--ui-border-width`, `--ui-control-height`, `--ui-control-height-sm/lg` | Ver seção 7. |
| Navegação | `--ui-nav-width`, `--ui-nav-width-collapsed`, `--ui-nav-item-height`, `--ui-nav-item-height-sm`, `--ui-nav-group-height`, `--ui-nav-item-radius`, `--ui-nav-item-radius-sm`, `--ui-nav-item-pad`, `--ui-nav-gap`, `--ui-nav-active-bg/fg/icon`, `--ui-nav-active-indicator(+width)` | Shell autenticado: sidebar, drawer e bottom nav. Ver seção 20.1. |
| Controles do shell | `--ui-icon-button-size/sm/xs`, `--ui-icon-button-radius/bg/border` | Recolher, tema, notificações, appbar. |
| Espaço | `--ui-panel-pad`, `--ui-gap`, `--ui-section-gap` | `22/18/24px` desktop; `16/14/18px` mobile. |
| Sombra | `--ui-shadow-panel`, `--ui-shadow-raised`, `--ui-shadow-floating` | Painel/superfície elevada; `floating` para barra fixa. Nunca em linha. |
| Transição | `--ui-transition: 160ms ease` | `prefers-reduced-motion` zera transições. |
| Tipografia | `--ui-font-family`, `--ui-fs-xs/sm/md/lg/title/display`, `--ui-fw-display/title/item` | Geist Sans; hierarquia por peso e contraste. `title` = h1 de página; `display` = heading de identidade (saudação do Dashboard). |
| Números | `font-variant-numeric: tabular-nums` | Obrigatório em valores monetários, percentuais e datas. |
| Desabilitado | `--ui-disabled-opacity: .5` | Aplicado por componente com `cursor: not-allowed`. |

**Cores secundárias:** o ContaBase não tem uma segunda cor de marca. O sistema
usa neutros frios, um acento violeta e as quatro cores semânticas. Não
introduzir uma cor secundária decorativa.

### Tokens específicos de componentes

| Componente | Tokens | Onde |
| --- | --- | --- |
| Painel do Dashboard | `--wallet-pad`, `--wallet-radius`, `--wallet-shadow` — **aliases** de `--ui-panel-pad`, `--ui-radius-panel`, `--ui-shadow-panel` | `dashboard-wallet.css` |
| Categoria | `--wallet-cat-0..5`, `--wallet-cat-color` | `dashboard-wallet.css` |
| Cartão bancário | `--card-fg`, `--card-fg-soft`, `--card-fg-muted`, `--card-line`, `--card-track`, `--card-fill`, `--card-badge-*`, `--card-dot-*` | `dashboard-wallet.css` |
| Progresso/limite | `--meter-tone`, `--v2-tone-*`, `--v2-progress-track` | `dashboard.css` + `dashboard-wallet.css` |
| Cor de instituição | `--v2-provider`, `--ic`, `--emblem-color` | `design-system.css` + `/workspace/colors.css` |

Cores de categoria do Dashboard (paleta controlada, escolhida por hash estável
do `category_id`): `#a78bfa`, `#2dd4bf`, `#fbbf24`, `#f472b6`, `#60a5fa` no
Dark; `#6d4de0`, `#0f766e`, `#b45309`, `#be185d`, `#1d4ed8` no Light. O neutro
`#8b90a4` / `#667086` representa “Sem categoria”. Elas são identidade de
componente e não entram na fundação global.

## 4. Tokens semânticos

Os nomes abaixo existem de verdade em `ui-tokens.css`. Use-os em vez de criar
apelidos locais.

```css
.cb-v2,
.cb-v2-body {
  --ui-canvas: var(--v2-canvas);
  --ui-sidebar: var(--v2-sidebar);
  --ui-surface: var(--v2-surface);
  --ui-surface-raised: var(--v2-surface-raised);
  --ui-surface-hover: var(--v2-surface-hover);
  --ui-border: var(--v2-border);
  --ui-border-strong: var(--v2-border-strong);
  --ui-text: var(--v2-text);
  --ui-text-muted: var(--v2-muted);
  --ui-text-subtle: var(--v2-subtle);
  --ui-accent: var(--v2-accent);
  --ui-accent-strong: var(--v2-accent-strong);
  --ui-accent-soft: var(--v2-accent-soft);
  --ui-focus: var(--v2-accent-strong);
  --ui-success: var(--v2-positive);
  --ui-warning: var(--v2-warning);
  --ui-danger: var(--v2-danger);
  --ui-info: var(--v2-info);
}
```

| Token | Uso | Não usar para |
| --- | --- | --- |
| `--ui-canvas` | Fundo da página e da aplicação | Painel ou card |
| `--ui-sidebar` | Sidebar desktop | Appbar mobile |
| `--ui-surface` | Painel, card de conteúdo, linha base | Fundo da página |
| `--ui-surface-raised` | Controle, KPI compacto, linha de destaque | Card estrutural grande |
| `--ui-surface-hover` | Hover de linha, item de menu, controle | Estado selecionado persistente |
| `--ui-border` | Separador e contorno de painel | Estado de erro |
| `--ui-border-strong` | Contorno de campo e controle | Divisória interna |
| `--ui-text` | Título e valor monetário | Texto auxiliar |
| `--ui-text-muted` | Descrição, data, metadado | Valor principal |
| `--ui-text-subtle` | Metadado de menor ênfase | Texto de corpo |
| `--ui-accent` | Foco, seleção, indicador de marca | Valor financeiro |
| `--ui-action-bg` | Fundo de botão primário | Fundo decorativo |
| `--ui-success` | Entrada, saldo positivo, meta concluída | Decoração |
| `--ui-danger` | Saída, atraso, limite excedido | Ação secundária |
| `--ui-warning` | Pendência, atenção, aumento relevante | Texto corrido |
| `--ui-info` | Informação neutra, composição alterada | Substituir sucesso/erro |
| `--ui-tab-active-bg/fg` | Aba ativa e seletor de mês ativo | Fundo de página |
| `--ui-radius-*` | Geometria de painel/card/controle/aba/badge | Exceções visuais sem justificativa |

### 4.1 Regras semânticas por contexto

| Contexto | Tratamento | Cuidado |
| --- | --- | --- |
| Saldo positivo / entrada | `--ui-success` + sinal `+` e rótulo | Não usar verde em despesa que apenas subiu. |
| Saldo negativo / saída | `--ui-danger` + sinal `−` e rótulo | Não pintar a linha inteira; o valor comunica. |
| A pagar | `--ui-danger` no valor, badge neutro/âmbar | Vencido usa danger; pendente usa warning. |
| A receber | `--ui-success` no valor, badge neutro | Previsto não é recebido; o rótulo “Previsto” permanece. |
| Vencido | `--ui-danger` + texto/data | Não duplicar “Atrasado” e “Vencido”. |
| Pendente | `--ui-warning` + texto | Âmbar não é erro. |
| Pago / fechado | `--ui-success` ou neutro conforme o contexto | Fechamento de fatura não é receita. |
| Progresso de meta | `--ui-accent` por padrão | Concluída usa success; negativa usa danger. |
| Limite excedido | `--ui-danger`; barra satura em 100% | Texto mostra o percentual real. |
| Crescimento de despesa | Neutro por padrão; âmbar só em aumento relevante | Percentual positivo não vira verde automaticamente. |
| Informação/composição | `--ui-info` + texto | Não substitui sucesso/erro. |

**Nunca depender só da cor.** Sinal, rótulo, badge, ícone e descrição acessível
acompanham o estado.

## 5. Tipografia Geist

- Família única: `Geist Sans`.
- Pesos reais no Dashboard: `550`, `570`, `580`, `590`, `600`, `620`, `640`,
  `650`, `680`.
- Escala observada: `10px` (metadado mínimo), `10.5px`, `11px`, `11.5px`,
  `12px`, `12.5px`, `13px`, `13.5px`, `14px`, `15.5px`, `16px`, `17px`,
  `18px`, `20px`, `23px`, `clamp(24–33px)` no saldo.
- Títulos de painel: `16–18px`, peso `620`, `letter-spacing` negativo discreto.
- Valores: `13–16px` em linhas; saldo `clamp(25–33px)`.
- Rótulos auxiliares: `10–11.5px`, `text-transform: uppercase` apenas quando é
  rótulo estrutural, não em conteúdo financeiro.

Regra prática: não usar `text-[9px]`. O shell ainda tem `9px` no rótulo da
bottom nav em 320 px e `9.5px` em alguns eyebrows; são exceções documentadas,
não padrão para novas telas.

## 6. Espaçamento

| Contexto | Token/valor |
| --- | --- |
| Padding do painel | `--ui-panel-pad`: `22px` desktop; `16px` até 767 px |
| Gap entre painéis | `--ui-gap`: `18px` desktop; `14px` mobile |
| Gap entre seções | `--ui-section-gap`: `24px` desktop; `18px` mobile |
| Padding de linha de conta | `13px 10px` (mobile 13/8) |
| Padding de linha de categoria | `12px 8px` |
| Gap de ícone + texto | `12–14px` |
| Gap de meta linha | `10–12px` |
| Altura mínima de toque | `44px` para controles; `52px` no FAB |

Evitar `margin` entre irmãos quando o container já tem `gap`. O Dashboard usa
`gap` e `padding` para o ritmo vertical.

## 7. Bordas, raios e sombras

### 7.1 Geometria oficial

| Papel | Token | Valor | Uso |
| --- | --- | --- | --- |
| Painel | `--ui-radius-panel` | `18px` | `.ui-panel`, `.wallet-section`, modal/sheet grande |
| Card | `--ui-radius-card` | `14px` | KPI, card compacto, superfície elevada |
| Tile | `--ui-radius-tile` | `10px` | Avatar, status/ícone de linha, ação compacta, skeleton |
| Controle | `--ui-radius-control` | `12px` | Input, select, textarea, busca, botão |
| Botão | `--ui-radius-button` | `12px` | Primário, secundário e ghost |
| Aba (container) | `--ui-radius-tab` | `12px` | `ui-tabs`, `cb-v2-tabs`, `cb-tabs` |
| Aba (item) | `--ui-radius-tab-item` | `8px` | Botão interno da aba |
| Badge | `--ui-radius-badge` | `999px` | Chip/badge compacta |
| Pill | `--ui-radius-pill` | `999px` | Ação compacta, nunca container de aba |
| Borda | `--ui-border-width` | `1px` | Contornos e divisórias |
| Altura de controle | `--ui-control-height` | `42px` (`44px` mobile) | Input, busca, botão padrão |
| Altura compacta | `--ui-control-height-sm` | `34px` (`40px` mobile) | Variante compacta |

**Pills são permitidas** para chips pequenos, badges compactas e alguns botões
de ação. **Não são permitidas** para containers de abas, campos de busca,
selects principais, painéis grandes ou barras de navegação segmentada.

### 7.2 Aplicação

- Painel: `1px solid var(--ui-border)`, raio `18px`, sombra
  `--ui-shadow-panel`.
- Linha de lista: sem borda externa; separador `1px`; raio `10px` apenas em
  hover/foco.
- Input/select/busca: `1px solid var(--ui-field-border)`, raio `12px`, fundo
  `--ui-field-bg`, foco com anel de `3px` do acento.
- Botão: raio `12px`; primário usa `--ui-action-bg`; secundário usa superfície
  elevada; ghost é transparente.
- Aba: container `12px`, item `8px`, fundo neutro, ativo com
  `--ui-tab-active-bg` e texto `--ui-tab-active-fg`.
- Seletor de mês: container `16px` (raio específico do trilho, entre card e
  painel); botão `12px`; ativo com `--ui-month-active-bg` e borda
  `--ui-month-active-border`.
- Cartão bancário: raio `16px`, sombra e face próprias (identidade de
  componente).
- Modal/overlay: raio `16–20px`, sombra elevada, backdrop.
- Barra de progresso: `5–7px`, raio `999px`, track `--ui-progress-track`.
- Foco: `2px solid var(--ui-focus)` com `outline-offset` positivo; em linhas,
  offset negativo para não deslocar o layout.

## 8. Componentes reutilizáveis

| Componente | Implementação atual | Reutilizável? | Contrato mínimo |
| --- | --- | --- | --- |
| Shell autenticado | `app_shell.html` + `app-shell.css` | Sim, já global | sidebar, appbar, drawer, bottom nav, FAB, workspace, tema. Fonte única: nenhum CSS de página redefine (seção 20) |
| Page header | `.ui-page-header` (Contas); `.wallet-heading` (Dashboard); Tailwind legado nas demais | Parcial | eyebrow, título, descrição, ação; migrar páginas legadas na fase própria |
| Section header | `.ui-section-heading` + `.wallet-section-heading` | Sim | ícone, título, descrição, ação opcional |
| Panel | `.ui-panel`, `.wallet-section` | Sim | borda `--ui-border`, raio `18px`, superfície canônica |
| KPI card | `.ui-kpi`, `.wallet-kpi` | Sim, com cuidado | label, valor, delta, privacidade |
| Button | `.ui-btn`, `.cb-button` | Sim | variantes primary/secondary/ghost, `sm`, disabled, foco |
| Compact action | `.ui-pill`, `.wallet-link`, `.wallet-shortcuts a` | Sim, variante pill | ação curta; não usar em tabs/segmentos |
| Badge | `.ui-badge`, `.badge-*`, `.wallet-action-flag` | Sim | tom, texto, ícone opcional; pill permitida |
| Empty state | `.ui-empty`, `.cb-v2-empty-state`, `.cb-empty-state` | Sim | ícone, título, descrição, CTA |
| Tabs | `.ui-tabs` (Premium), `.cb-tabs` (base legada), `.cb-v2-tabs` (Dashboard/Faturas) | Sim, unificado por tokens | container `12px`, item `8px`, ativo violeta, foco visível; nenhuma página reimplementa |
| Segmented filter | `.wallet-period` | Sim, mesma geometria de tabs | `aria-pressed`, ativo violeta, compacto |
| Input/select/search | `.ui-input`, `.ui-select`, `.ui-search`, `.cb-input` | Sim, via tokens + ponte | raio `12px`, altura 42/44, foco com anel |
| Month selector | `[data-month-selector]` + `.month-btn` | Sim, uma única versão | ativo com `--ui-month-active-*`; usado em Contas/Lançamentos/Relatórios/Faturas |
| Progress | `progress` + `.cb-v2-progress*` | Sim, mas separar consumo de meta | valor, máximo, tom, label |
| Financial row | `.ui-row`, `.cb-v2-account-row`, `.cb-v2-action-row` | Parcial | ícone, duas linhas, valor, link |
| Category share | `.wallet-category-row` | Sim, domínio financeiro | nome, ícone, valor, variação, barra |
| Goal ring | `.wallet-goal-ring` | Sim, domínio | percentual, tom, label |
| Credit card tile | `.wallet-card` (Dashboard) e `.cb-v2-credit-card` (Faturas) | Não unificar agora | face, status, fatura, limite |
| Chart | `balance-evolution.js` + `.wallet-evolution*` | Sim, domínio | série, tooltip, tabela, caveats |
| Modal/dropdown | `overlay-stack.js`, `.cb-modal`, `.cb-dropdown` | Sim | foco, ESC, backdrop, retorno |
| Tooltip | `tooltip.js` | Sim | texto, posição, toque/teclado |
| Loading | `.htmx-indicator`, `.ui-skeleton`, `.wallet-evolution-loading` | Sim | estado, `aria-busy` |

## 9. Componentes exclusivos do Dashboard

Não promover automaticamente para o global:

- Composição `.wallet` e as duas colunas independentes.
- `wallet-kpi-total`/`wallet-kpi-secondary` e a hierarquia Reservado/Disponível.
- `wallet-card`/carrossel de cartões com face tonal e seleção.
- `wallet-category-row` e a barra de participação por categoria.
- `wallet-evolution` (gráfico, tooltip, tabela, caveats).
- `wallet-goal-ring` e o agrupamento de metas.
- `.wallet-shortcuts`, `.wallet-privacy` e a ordem mobile específica.

Esses componentes dependem de viewmodels e regras do Dashboard. Extrair apenas
a “casca” visual quando houver uma segunda tela com o mesmo comportamento.

## 10. Responsividade

Breakpoints atuais:

| Faixa | Uso |
| --- | --- |
| `<= 359px` | Ajustes extremos da bottom nav |
| `<= 389px` | Labels e paddings compactos |
| `<= 699px` | Mobile do Dashboard |
| `<= 1023px` | Shell mobile, uma coluna, bottom nav |
| `1024–1279px` | Desktop compacto |
| `>= 1760px` | Duas colunas de contas do Dashboard |

O CSS legado ainda usa `640`, `767`, `900`, `1180`, `1240`. A divergência é
dívida: uma página pode mudar de layout em 699 e outra em 767. Para a migração,
congelar os breakpoints por página e só unificar depois de um piloto aprovado.

Regras:

- mobile-first apenas onde a página já é mobile-first;
- sem overflow horizontal em 320 px;
- conteúdo não pode ficar atrás da bottom nav (`padding-bottom` com safe area);
- sidebar desktop não pode encolher o conteúdo a ponto de quebrar valores;
- testar 320, 390, 768, 1280, 1440 e 1600 nos dois temas.

## 11. Acessibilidade

- Contraste AA: texto normal >= 4,5:1; ícones/barra >= 3:1.
- Foco visível em link, botão, linha navegável, aba e FAB.
- Área de toque >= 44×44 px (FAB 52×52).
- `prefers-reduced-motion` remove transições e animações.
- `aria-expanded`, `aria-controls`, `aria-selected`, `aria-current` e
  `aria-busy` preservados.
- Não usar cor como único indicador: sinal `+`/`−`, texto e badge sempre juntos.
- Privacidade: `data-sensitive` cobre valores e variações; gráficos e tabelas
  continuam legíveis.
- Zoom: não bloquear `user-scalable`; o shell já removeu
  `maximum-scale=1, user-scalable=no`.

## 12. Cores financeiras

| Situação | Tratamento |
| --- | --- |
| Entrada/saldo positivo | `--ui-success`, com sinal e texto |
| Saída/saldo negativo | `--ui-danger`, com sinal e texto |
| A pagar | valor em `--ui-danger`; badge neutro/âmbar conforme vencimento |
| A receber | valor em `--ui-success`; rótulo “Previsto” permanece |
| Pendente/atenção | `--ui-warning` |
| Vencido | `--ui-danger` + texto/data; sem duplicar “Atrasado” |
| Pago/fechado | `--ui-success` ou neutro conforme o contexto |
| Limite excedido | `--ui-danger`; barra satura em 100%, texto mostra o real |
| Meta concluída | `--ui-success` |
| Progresso de meta | `--ui-accent` por padrão; negativa em `--ui-danger` |
| Categoria sem cor própria | paleta determinística por `category_id` |
| Variação de despesa | neutra por padrão; âmbar só em aumento relevante |
| Composição de contas | `--ui-info`, texto “Composição alterada” |

Nunca usar verde/vermelho apenas porque o número subiu ou desceu. A semântica
vem do produto: uma redução de despesa não é automaticamente sucesso.

## 13. Organização CSS recomendada

### Estrutura-alvo

### Estrutura atual

```text
assets/css/
  design-system.css   # legado --cb-* e componentes .cb-* (transição); tabs canônicas .cb-tabs
  app-shell.css       # shell + primitivos --v2-* (fonte única de sidebar/appbar/bottom nav/FAB)
  ui-tokens.css       # fundação canônica --ui-* + ponte --cb-* no shell
  premium.css         # biblioteca de componentes ui-* (escopo Premium)
  dashboard.css       # base do Dashboard + tile de Faturas + tabs .cb-v2-tabs compartilhadas
  dashboard-wallet.css# composição e tokens exclusivos do Dashboard (não toca no shell)
  contas.css          # layout da página Contas
  <pagina>.css        # layout/tons exclusivos da página
```

A separação `tokens/shell/components/finance/pages` continua sendo o alvo de
longo prazo. Nesta etapa, `ui-tokens.css` já cumpre o papel de `tokens.css`, e
`premium.css` cumpre o de `components/finance` para as páginas que o adotam.

### Ordem de carregamento (real)

```text
style.css (Tailwind compilado)
geist-sans.css
design-system.css (legado; :root/.dark)
workspace/colors.css
app-shell.css (primitivos --v2-*)
ui-tokens.css (canônico --ui-* + ponte --cb-*)
CSS da página:
  premium.css + contas.css          (Contas)
  dashboard.css + dashboard-wallet.css (Dashboard)
  <pagina>.css                      (demais)
```

Os tokens canônicos são carregados antes de qualquer componente ou override de
página. `ui-tokens.css` usa `{{assetPath}}`; `app-shell.css` e as páginas
principais também.

### Layers

`@layer` continua viável, mas **não foi introduzido** nesta etapa para não
alterar a cascata existente. A estratégia atual é escopo explícito
(`.cb-v2`, `[data-ui="premium"]`, classes de página) e especificidade contida.

```css
@layer tokens, base, components, finance, pages, utilities, overrides;
```

Se o manifesto for adotado no futuro, `ui-tokens.css` entra em `tokens` e os
CSS de página em `pages`. Até lá, a especificidade máxima recomendada segue
`0,2,0`; `!important` só para neutralizar utilitário de terceiro.

### Regras

1. Tokens canônicos só existem em `ui-tokens.css`/`app-shell.css`; nenhum hex
   novo em página sem justificativa de componente.
2. Página consome `--ui-*`; não redefine `--v2-*` nem `--ui-*`. Tokens de
   componente (cartão, categoria, gráfico) ficam no CSS do componente.
3. Componente compartilhado não conhece viewmodel de página.
4. Domínio financeiro pode ter componente próprio, mas não duplicar cálculo.
5. Seletores de página começam por `[data-page-section="..."]` ou classe de
   página.
6. `!important` só para neutralizar utilitário de terceiro, com comentário.
7. A ponte `--cb-*` existe apenas dentro de `.cb-v2` e deve encolher conforme as
   páginas migram para `ui-*`.
8. Um reset de shell nunca deve usar seletor de elemento com classe do shell
   (`.cb-v2 button`, `.cb-v2 a`): `(0,1,1)` vence qualquer classe de componente.
   Resets de tipografia e borda ficam no Preflight; o shell só define o que é
   do shell.

## 14. Cuidados de migração

- Migrar uma página por vez, com branch/worktree isolado e screenshots antes/depois.
- Não misturar mudança visual com alteração de query, cálculo ou migration.
- Começar por tokens/shell sem mudança visual; depois componentes; depois página.
- Manter os IDs, `name`, `hx-*`, `data-*` e classes funcionais.
- Comparar com a base `dev` usando screenshots e métricas de overflow/console.
- Não remover CSS legado até a última página migrada.
- Não promover `.wallet-*` para global antes de existir um segundo consumidor.
- Validar Dark/Light, 320/390/768/1280/1440/1600, foco, reduced motion e privacidade.
- Se um componente financeiro divergir entre páginas, primeiro provar a
  diferença de regra; não “unificar” comportamento.

## 15. Inventário de arquivos atuais

| Arquivo | Papel | Observação |
| --- | --- | --- |
| `assets/css/style.css` | Tailwind compilado + utilitários | 123 KB; contém `@layer` do Tailwind |
| `assets/css/design-system.css` | DS legado `--cb-*` e `.cb-*` | Base de componentes legados; a ponte canônica vive em `ui-tokens.css` |
| `assets/css/app-shell.css` | Shell autenticado + primitivos | Tokens `--v2-*`, sidebar, nav, appbar, bottom nav, FAB, foco compartilhado. Fonte única do shell |
| `assets/css/ui-tokens.css` | Fundação canônica | `--ui-*`, navegação, geometria, estados, ponte `--cb-*` dentro de `.cb-v2` |
| `assets/css/premium.css` | Biblioteca Premium escopada | Componentes `ui-*`; consome `--ui-*` sem duplicar paleta |
| `assets/css/dashboard.css` | Base do Dashboard e tiles de Faturas | 27 KB; compartilhado com Faturas; tabs canônicas `.cb-v2-tabs` |
| `assets/css/dashboard-wallet.css` | Composição premium do Dashboard | Escopo `.wallet`; apenas tokens exclusivos (categorias, cartões, tons). Não define shell |
| `assets/css/faturas.css` | Faturas | Usa `.cb-v2-credit-card` de dashboard.css |
| `assets/css/faturas-index.css` | Hub de Faturas | Carrega dashboard.css |
| `assets/css/lancamentos.css` | Lançamentos | Lista, swipe, filtros |
| `assets/css/metas.css` | Metas/Limites | Barras e anéis |
| `assets/css/relatorios.css` | Relatórios | Tabelas e gráficos |
| `assets/css/configuracoes.css` | Configurações | Formulários |
| `assets/css/contas.css` | Piloto Contas | Layout/tons da página; usa `premium.css` |
| `assets/js/app-shell.js` | Shell, drawer, workspace | Depende de `.cb-v2-*` |
| `assets/js/dashboard.js` | Carrossel, contas, categorias | Depende de `.wallet-*` |
| `assets/js/balance-evolution.js` | Gráfico e caveats | Depende de `.wallet-evolution*` |
| `templates/components/app_shell.html` | Shell único | Sidebar, nav, FAB, modais globais |
| `templates/components/credit_card.html` | Tile de cartão | Usado por Dashboard/Faturas |
| `templates/pages/dashboard.html` | Dashboard | Composição `.wallet` |

## 16. Pendências técnicas

1. **Ponte legada `--cb-*`:** resolvida dentro de `.cb-v2`, mas ainda existem
   componentes legados que dependem de `!important`/Tailwind. Migrar por página
   e remover a ponte no fim.
2. **Separar `dashboard.css`:** hoje ele serve Dashboard e Faturas. Separar o
   que é tile de Faturas do que é base antiga do Dashboard.
3. **Unificar cartão de crédito:** `.wallet-card` (Dashboard) e
   `.cb-v2-credit-card` (Faturas) são implementações diferentes. Não unificar
   antes de validar os dois comportamentos.
4. **Migrar tabs/empty/botão legados:** as tabs equivalentes já compartilham
   tokens; ainda há páginas com tabs próprias em Tailwind (Relatórios, Faturas)
   que devem ser migradas sem alterar o contrato de navegação.
5. **Breakpoints:** 699/767/900/1180/1240/1279/1760. Definir uma escala global
   sem quebrar o Dashboard.
6. **Cache busting:** `style.css`, `geist-sans.css`, CSS de página e vários JS
   ainda são carregados sem `assetPath`. `app-shell.css`, `ui-tokens.css`,
   `premium.css`, `contas.css` e `dashboard.css` já usam `assetPath`.
7. **Fontes < 10px:** bottom nav em 320 px (`9px`), eyebrows (`9.5px`),
   monogramas de instituição (`7–8px`) e labels de progresso (`9.5px`). São
   exceções; não criar novas.
8. **`!important` no workspace:** `.cb-v2-workspace-list` usa `!important` para
   vencer utilitários. Substituir por especificidade controlada na migração.
9. **Dependência de ordem:** `dashboard.css` e `dashboard-wallet.css` funcionam
   pela ordem de carregamento. Qualquer bundling futuro precisa preservar a
   ordem ou usar layers.
10. **Dados de demonstração:** dados usados em validação visual não fazem
    parte do design system nem de seed de produção. O design system deve ser
    validado com dados reais ou sintéticos isolados, sem migration.
11. **Page headers legados:** Faturas, Lançamentos, Metas, Relatórios e
    Configurações ainda usam cabeçalhos Tailwind próprios
    (eyebrow/título/descrição). Migrar para `ui-page-header` em fase própria,
    sem reconstruir a página. A Administração concluiu essa migração na ADM3.

## 17. Roadmap de migração das páginas

| Ordem | Página | Status | Por que | Esforço | Risco |
| --- | --- | --- | --- | --- | --- |
| 0 | Baseline | **Concluído nesta etapa** | Congelar screenshots, testes e tokens atuais | P | Baixo |
| 1 | Tokens + shell | **Concluído nesta etapa** | `ui-tokens.css` + `app-shell.css` sem mudar layout estrutural | M | Médio |
| 2 | Componentes globais | **Consolidado nesta etapa** | Botão, input, tabs, empty, foco e shell padronizados; variantes documentadas na seção 20 | M | Médio |
| 3 | **Contas (piloto)** | **Concluído nesta etapa** | Busca, mês, tabs, KPIs e lista | M | Baixo |
| 4 | Faturas | Fundamentos aplicados | Card tile, badges, modais de pagamento | L | Alto |
| 5 | Lançamentos | Fundamentos aplicados | Lista densa, filtros, swipe, bulk, formulário | XL | Alto |
| 6 | Metas | Fundamentos + tabs aplicados | Limites, metas, anéis, reservas | L | Alto |
| 7 | Relatórios | Fundamentos aplicados | Tabelas, gráficos, exportação e tabs próprias | L | Médio |
| 8 | Configurações | Fundamentos aplicados | Formulários, categorias, contas, cartões | L | Médio |
| 9 | Administração | **Concluído na ADM3** | Usuários, workspaces, backups, auditoria com envelope premium próprio | M | Baixo |
| 10 | Limpeza | Pendente | Remover ponte `--cb-*`, CSS legado e aliases temporários | M | Médio |

> “Fundamentos aplicados” significa que a página já herda paleta, semântica e
> geometria via `ui-tokens.css`/ponte, mas ainda não teve seu layout ou todos os
> componentes migrados para `ui-*`.

### Dependências por página

- **Contas:** shell + tokens + empty state + botão + input + tabs. Não depende de
  Faturas. Bom piloto.
- **Faturas:** tokens + card tile + badge + modal + botão. Depende da decisão
  sobre `.cb-v2-credit-card`.
- **Lançamentos:** tokens + linha financeira + filtros + tabs + swipe + modal.
  Depende de não quebrar `swipe.js`/`bulk-select.js`.
- **Metas:** tokens + progress + anel + modal + limite. Depende de
  `ui_tones.go` e da semântica consumo vs meta.
- **Relatórios:** tokens + tabela + gráfico + filtros. Depende do contrato de
  séries e exportação.
- **Configurações:** tokens + formulários + tabs + modal. Baixo risco visual,
  muitos fluxos.
- **Administração:** shell + cabeçalho + painéis + tabela + modal +
  confirmação. Envelope próprio (`cb-v2-admin-main`) e camada
  `admin-premium.css`; a revisão final de teclado/mobile fica na ADM4.

### Critério de aceite por página

1. Screenshots Dark/Light em 320, 390, 768, 1280, 1440, 1600.
2. Zero overflow horizontal e zero erro de console.
3. Navegação, foco, teclado, modal, dropdown e HTMX preservados.
4. Nenhuma alteração de cálculo, permissão, workspace ou migration.
5. `go test ./...`, `go vet ./...`, `go build ./...`, testes JS e
   `git diff --check` verdes.
6. Diff visual revisado contra a base; toda diferença classificada como
   intencional, compartilhada, regressão ou pré-existente.

**Não iniciar a fase 1 sem autorização explícita.**

## 18. Implementação atual — padronização global (P1–P8)

### 18.1 Escopo executado

- Fundação canônica em `assets/css/ui-tokens.css`, carregada pelo shell depois
  de `app-shell.css`.
- Paleta estrutural do Dashboard substituída pela referência de `/contas`.
- Acento e semântica preservados da identidade Premium do Dashboard.
- Dashboard deixou de declarar tokens estruturais, de acento e semânticos;
  mantém somente categorias, cartões, gráfico e tons exclusivos.
- Contas: busca em 100% da largura útil; mês ativo com destaque forte; tabs com
  raio moderado e seleção violeta; geometria alinhada.
- Tabs padronizadas em Contas, Dashboard, Metas e no filtro de período do
  gráfico; seletor de mês unificado em Contas, Lançamentos, Relatórios e
  Faturas.
- Ponte `--cb-*` dentro de `.cb-v2` para que componentes legados consumam a
  fundação sem reescrita de markup.
- Nenhum cálculo, query, rota, permissão, autenticação, schema ou migration foi
  alterado.

### 18.2 Arquivos principais

| Arquivo | Mudança |
| --- | --- |
| `assets/css/ui-tokens.css` | **Novo:** tokens canônicos `--ui-*`, geometria, estados e ponte `--cb-*` |
| `assets/css/app-shell.css` | Acento/semântica canônicos; foco compartilhado de campos legados |
| `assets/css/premium.css` | Consome tokens; tabs/botões/inputs na geometria oficial; remove override duplicado do mês |
| `assets/css/contas.css` | Busca sem largura máxima |
| `assets/css/dashboard-wallet.css` | Remove overrides estruturais/acento/semântica; mantém identidade do Dashboard; tabs e período canônicos |
| `assets/css/dashboard.css` | Abas `cb-v2-tabs` na geometria e seleção canônicas |
| `assets/css/metas.css` | Abas de Metas na geometria e seleção canônicas |
| `assets/css/design-system.css` | Seletor de mês compartilhado consome tokens canônicos; uma única versão |
| `templates/components/app_shell.html` | Carrega `ui-tokens.css` antes do CSS de página |
| `internal/handlers/premium_foundation_test.go` | Contrato atualizado para a fonte canônica `ui-tokens.css` |
| `docs/design-system-premium-fintech.md` | Esta documentação |

### 18.3 Como uma página consome a fundação hoje

1. O shell carrega `app-shell.css` e `ui-tokens.css`.
2. A página carrega seu CSS depois; não redefine `--ui-*`/`--v2-*`.
3. Páginas novas usam `data-ui="premium"` + `premium.css` + `ui-*`.
4. Páginas legadas continuam com seu markup e classes; dentro de `.cb-v2`, os
   `--cb-*` já resolvem para a fundação canônica.
5. Tokens de componente (cartão, categoria, gráfico) ficam no CSS do
   componente e não são promovidos sem um segundo consumidor.

### 18.4 Contas — detalhes

- **Busca:** `width: 100%` e `max-width: none`; alinhada ao container, às tabs e
  ao painel da lista. Medida em 320/360/390/768/1280/1440/1600: largura da busca
  = largura das tabs = largura do painel.
- **Mês ativo:** fundo `--ui-month-active-bg`, texto
  `--ui-month-active-fg`, borda `--ui-month-active-border`, raio `12px`.
  Contraste medido: 4,69:1 (dark) e 6,17:1 (light).
- **Tabs:** container `12px`, item `8px`, ativo com fundo violeta e texto
  `--ui-tab-active-fg`; contraste 4,81:1 (dark) e 5,97:1 (light). `aria-pressed`
  preservado.
- **HTMX:** busca (400 ms), tabs e mês continuam funcionando; contrato e URLs
  inalterados.

### 18.5 Dashboard e demais páginas

- Canvas, sidebar, superfícies, bordas, textos e controles agora idênticos aos
  de `/contas` (comparação por `getComputedStyle`).
- Roxo institucional, cartões bancários, gráfico violeta, categorias, metas,
  limites e cores semânticas preservados.
- Tabs do Dashboard (A pagar/A receber) e de Metas usam o mesmo padrão visual de
  Contas.
- Filtro de período do gráfico (30d/90d/6m/12m) deixou de ser pill e usa a
  geometria de tabs.
- Faturas, Lançamentos, Relatórios, Configurações, Categorias e Administração
  foram verificados em Dark/Light, 390 e 1440; sem overflow, erro de console ou
  regressão funcional.

### 18.6 Exceções justificadas

- `.wallet-link`, `.wallet-shortcuts a`, `.ui-pill` e badges continuam em pill:
  são ações/chips compactos, não containers de aba.
- Cartões bancários, gráfico e anéis de meta mantêm tokens próprios: são
  objetos/visualizações de domínio, não superfícies estruturais.
- Relatórios e Faturas ainda têm navegação de página própria (abas superiores e
  links de ordenação) com semântica diferente; receberam apenas os fundamentos
  compatíveis.
- Page headers de páginas legadas continuam em Tailwind; migrar para
  `ui-page-header` é pendência da fase de página.

### 18.7 Pendências de migração

1. Migrar page headers legados para `ui-page-header` sem reconstruir a página.
2. Migrar tabs próprias de Relatórios/Faturas para os tokens de aba, preservando
   `href`, `hx-*` e `aria-current`.
3. Substituir gradualmente a ponte `--cb-*` por `ui-*` nos componentes legados e
   então removê-la.
4. Separar `dashboard.css` (base do Dashboard vs tile de Faturas).
5. Unificar os dois cartões de crédito somente após validar os dois
   comportamentos.
6. Definir a escala global de breakpoints e completar cache busting.
7. Migrar superfícies standalone/auth que ainda usam `--cb-*` fora de `.cb-v2`.

### 18.8 Critério de aceite para a próxima página

1. Screenshots Dark/Light em 320, 390, 768, 1280, 1440 e 1600.
2. Zero overflow horizontal, zero erro de console.
3. Navegação, foco, teclado, modal, dropdown e HTMX preservados.
4. Nenhuma alteração de cálculo, permissão, workspace ou migration.
5. `go test ./...`, `go vet ./...`, `go build ./...`, testes JS e
   `git diff --check` verdes.
6. Toda diferença visual classificada como intencional, compartilhada,
   regressão ou pré-existente.

## 19. Consolidação do shell e dos componentes compartilhados (P1–P10)

### 19.1 Diagnóstico — Dashboard x Contas

Comparação feita no mesmo navegador, viewport 1440×900 (e 390×844 no mobile),
`deviceScaleFactor: 1`, zoom 1, mesmo tema, mesmo workspace, mesma configuração
de sidebar, com `getComputedStyle` e `CSS.getMatchedStylesForNode` (CDP).

| Componente | Diferença encontrada | Causa real | Arquivo responsável | Correção aplicada |
| --- | --- | --- | --- | --- |
| Item ativo da sidebar | Dashboard: gradiente violeta + anel `inset 0 0 0 1px` + texto branco. Contas: fundo `accent-soft` + barra lateral de 2px + texto violeta claro | `dashboard-wallet.css` redefinia `.cb-v2-nav-link.is-active` com especificidade igual e carregava depois | `dashboard-wallet.css` (override) → `app-shell.css` (fonte) | Override removido. Estado canônico único: `--ui-nav-active-bg`, `--ui-nav-active-fg`, `--ui-nav-active-icon` e indicador de 2px |
| Geometria do item de navegação | 42px de altura e raio 11px (Dashboard) x 40px e 8px (Contas) | Override de `min-height`/`border-radius` no CSS do Dashboard | `dashboard-wallet.css` | Removido. `--ui-nav-item-height`, `--ui-nav-item-radius` |
| Botão de recolher | Dashboard: 34×34 com borda e superfície. Contas: ícone sem caixa | `.cb-v2-sidebar .cb-v2-icon-button` só existia no CSS do Dashboard | `dashboard-wallet.css` | Tratamento com borda/superfície movido para `app-shell.css`; tamanho `--ui-icon-button-size-sm` |
| Controles do rodapé (tema/notificações) | 28×32 sem caixa (Contas) x 28×32 com caixa (Dashboard) | mesma duplicação de `.cb-v2-sidebar .cb-v2-icon-button` | `dashboard-wallet.css` | Fonte única no shell; variante compacta de 30×30 (`--ui-icon-button-size-xs`) para preservar o nome do workspace |
| Appbar mobile | Dashboard com fundo translúcido, borda e `backdrop-filter`; Contas transparente | Override dentro de `@media (max-width:1023px)` no CSS do Dashboard | `dashboard-wallet.css` | Movido para o shell: todas as páginas têm o mesmo appbar |
| Bottom navigation | Dashboard: barra flutuante (raio 20, sombra, blur, `left/right 12`, item ativo 14px). Contas: barra colada (raio 0, item 10px) | Override de shell no CSS do Dashboard | `dashboard-wallet.css` | Uma única barra flutuante no shell, com estado ativo canônico |
| FAB | Ausente no Dashboard desktop, presente em Contas desktop | Regra `body:has([data-dashboard])` | `app-shell.css` | Regra global: o FAB só existe onde a sidebar está oculta (`< 1024px`) |
| Rótulos de grupo (`ACESSO RÁPIDO`) | 10px/`.09em` (Dashboard) x 9.5px/`.075em` (Contas) | Override de tipografia do shell no CSS do Dashboard | `dashboard-wallet.css` | Removido; tipografia do shell é a única |
| Workspace | raio 11px (Dashboard) x 9px (Contas) | Override local | `dashboard-wallet.css` | `--ui-nav-item-radius`; altura `--ui-nav-item-height` |
| Cabeçalho de página | h1 25/20px peso 640 (Dashboard) x `clamp(25–32)` peso 680 (Contas) | Dashboard usava valores fixos fora da escala | `dashboard-wallet.css` | `--ui-fs-display`/`--ui-fw-display` para o heading de identidade e `--ui-fs-title`/`--ui-fw-title` para o page title. A diferença de tamanho é hierarquia (saudação x título), não inconsistência |
| Descrição do cabeçalho | `margin-top` 5px x 7px | valor local | `dashboard-wallet.css` | `--ui-fs-md` + 7px, igual ao `.ui-page-description` |
| Tabs de Metas | Ativo violeta sólido com texto branco (`.cb-tab-active` legado) | `.cb-tabs` legado em `design-system.css` divergia do padrão aprovado; Metas reimplementava a correção no próprio CSS | `design-system.css` + `metas.css` | `.cb-tabs/.cb-tab` passam a ser a implementação canônica (container 12, item 8, ativo violeta suave); bloco duplicado removido de `metas.css` |
| Tabs de Contatos | Container 11px, ativo com linguagem de navegação (`accent-soft`) | componente paralelo na página | `contatos.css` | Consome `--ui-radius-tab`, `--ui-tab-active-*` e foco canônico |
| Painéis/KPIs | `.ui-panel` usava alias `--ui-radius-lg`; `.ui-kpi` usava `--ui-radius-md` | aliases de compatibilidade | `premium.css` | Passam a usar `--ui-radius-panel`/`--ui-radius-card`/`--ui-radius-tile` |

Diferenças **legítimas** mantidas (não são inconsistências):

- Composição do Dashboard (`.wallet`, colunas, KPI total, carrossel de cartões,
  categorias, gráfico de evolução) — identidade e viewmodel próprios.
- Gradiente radial do canvas do Dashboard: profundidade sobre o mesmo
  `--v2-canvas`, sem segunda paleta.
- Título do Dashboard menor que o page title: hierarquia greeting x página,
  agora ambos na mesma escala canônica.
- Altura/conteúdo de `main` e largura de descrição: dependem do conteúdo.
- Cartões de crédito do Dashboard x Faturas e tabelas especializadas.
- Bottom nav: item ativo sem barra lateral (variante horizontal documentada).

### 19.2 Ordem de carregamento (confirmada)

```text
style.css (Tailwind compilado)
geist-sans.css
design-system.css      # legado .cb-* + tabs canônicas para páginas sem premium.css
/workspace/colors.css  # dinâmico por workspace
app-shell.css          # shell + primitivos --v2-*
ui-tokens.css          # fundação canônica --ui-* + ponte --cb-*
CSS da página          # premium.css + contas.css | dashboard.css + dashboard-wallet.css | <pagina>.css
```

- `ui-tokens.css` é carregado pelo shell em todas as páginas autenticadas.
- Nenhuma página redefine `--v2-*`/`--ui-*`; o Dashboard usa `--wallet-*` para
  categorias/cartões.
- `dashboard-wallet.css` não define mais nada do shell.
- A aparência de um componente do shell não depende mais de qual CSS de página
  foi carregado por último.

### 19.3 Evidência de igualdade (mesma viewport, tema e estado)

Medição por `getComputedStyle` em Dashboard x Contas, Dark e Light:

| Propriedade | Antes | Depois |
| --- | --- | --- |
| `.cb-v2-nav-link.is-active` `background-color` | `transparent` x `accent-soft` | idêntico |
| `background-image` | gradiente x `none` | idêntico (`none`) |
| `box-shadow` | anel 1px + highlight x barra 2px | idêntico (barra 2px) |
| `border-radius` | 11px x 8px | idêntico (10px) |
| `min-height` | 42px x 40px | idêntico (40px) |
| `color` | texto principal x `accent-on-soft` | idêntico |
| Botão de recolher | 34×34 com borda x 34×34 sem borda | 34×34 com borda nos dois |
| Appbar mobile | blur + tint x transparente | idêntico |
| Bottom nav | flutuante x colada | idêntico |

A verificação comparou capturas antes/depois por componente, com script de
auditoria (`smoke.mjs`) e artefatos de evidência (`baseline.json`, `after.json`,
`matrix/matrix.json`, `matrix/failures.json`). A matriz cobre 6 viewports × 2 temas, estados
(default, hover, foco, disabled, aba), sidebar expandida/recolhida, submenu
aberto/fechado e 9 rotas. Diferenças de antialiasing/rasterização foram
separadas das diferenças de layout: a única diferença residual de largura na
bottom nav é sub-pixel (< 0,02px) entre itens distintos da grade.

### 19.4 Bug de cascata encontrado na auditoria

O reset do shell `.cb-v2 button { border: 0; font: inherit; }` tinha
especificidade `(0,1,1)` — maior que qualquer classe de componente
`(0,1,0)`. Consequências medidas (antes da correção):

| Componente | Declarado | Renderizado |
| --- | --- | --- |
| `.ui-tab` (Contas) | 12px/620 | 16px/400 (herdado do container) |
| `.cb-tab` (Metas) | 12px/620 | 16px/400 |
| `.cb-button` | 12px/700 caixa alta | 12px/700 em Contas/Lançamentos/Metas e **16px/400 em Contatos/Faturas/Configurações** |
| `.cb-tab-active` / `.ui-tab.is-active` | borda de seleção | borda removida pelo reset |
| `.ui-btn--secondary` | borda 1px | sem borda |
| `.cb-v2-nav-group-toggle` | 13px/590 | 13px/400 |

Ou seja: o **mesmo componente compartilhado** mudava de tamanho, peso e borda
conforme o `font-size` do container da página. As tabs do Dashboard escapavam
porque `.wallet .cb-v2-tabs button` tem especificidade `(0,2,1)`.

O Tailwind Preflight já faz `button { font: inherit }` em `(0,0,1)` e zera
bordas pelo seletor universal. O reset do shell era redundante e nocivo, então
foi removido. Resultado: `.cb-tab`, `.ui-tab`, `.cb-button`, `.ui-btn` e o
toggle de grupo passam a renderizar exatamente o que declaram, em todas as
páginas. É a causa-raiz de parte do "mesmo componente com aparências
diferentes" e não uma decisão estética por página.

## 20. Contratos Canônicos de Componentes

Cada componente abaixo tem **um** arquivo responsável. Consumidores usam as
classes e os tokens; não redefinem geometria, estado ou cor.

### 20.1 Sidebar e navegação principal

- **Fonte:** `app-shell.css` (estrutura, geometria, estados) + `ui-tokens.css`
  (tokens). Markup: `templates/components/app_shell.html`
  (`app-v2-desktop-sidebar`, `app-v2-nav-links`, `app-v2-settings-nav`).
- **Tokens:** `--ui-nav-width`, `--ui-nav-width-collapsed`,
  `--ui-nav-item-height`, `--ui-nav-item-height-sm`, `--ui-nav-group-height`,
  `--ui-nav-item-radius`, `--ui-nav-item-radius-sm`, `--ui-nav-item-pad`,
  `--ui-nav-gap`, `--ui-nav-active-bg`, `--ui-nav-active-fg`,
  `--ui-nav-active-icon`, `--ui-nav-active-indicator(+width)`,
  `--ui-icon-button-size*`, `--ui-border`, `--ui-surface*`, `--ui-text*`.
- **Variantes:** expandida (248px), recolhida (72px, só símbolo), drawer mobile.
- **Estados:** default, hover (`--ui-surface-hover`), active
  (`.is-active`/`aria-current="page"`: superfície violeta + barra de 2px),
  focus-visible (outline 2px, `outline-offset: -2px`), grupo aberto/fechado
  (`aria-expanded` no toggle), submenu ativo.
- **Semântica:** `aria-current="page"` no item atual; o estado ativo nunca
  depende só de cor (fundo + barra + ícone).
- **Escopo:** qualquer rota autenticada.
- **Páginas consumidoras:** todas.
- **Customização:** nenhuma página redefine `.cb-v2-nav-link`,
  `.cb-v2-nav-sub-link`, `.cb-v2-nav-group-toggle`, `.cb-v2-workspace` ou
  `.cb-v2-sidebar-toggle`. Uma nova variante de navegação exige alteração no
  shell e regressão em todas as páginas.

### 20.2 Appbar, botão de recolher e controles do shell

- **Fonte:** `app-shell.css`. Tokens: `--ui-icon-button-size`,
  `--ui-icon-button-size-sm`, `--ui-icon-button-size-xs`,
  `--ui-icon-button-radius`, `--ui-icon-button-bg`, `--ui-icon-button-border`,
  `--ui-shadow-floating`.
- **Variantes:** appbar mobile (40px), cabeçalho da sidebar (34px), utilitários
  do rodapé (30px), barra recolhida (23×28 para caber em 48px).
- **Estados:** default, hover, focus-visible, `aria-expanded` no recolher,
  ícone/posição alternados em recolhido.
- **Páginas consumidoras:** todas. Nenhuma página altera cor, borda, raio ou
  tamanho desses botões.

### 20.3 Bottom navigation e FAB

- **Fonte:** `app-shell.css`. Tokens: `--ui-sidebar`, `--ui-border`,
  `--ui-nav-item-radius`, `--ui-nav-active-*`, `--ui-shadow-floating`,
  `--ui-accent`, `--ui-action-fg`.
- **Variantes:** barra flutuante fixa (`< 1024px`), uma por breakpoint de
  rótulo (10px) e de ícone; FAB central na barra.
- **Estados:** default, active (mesma superfície/ícone do item de sidebar),
  `:active`, focus-visible.
- **Semântica:** item ativo usa `aria-current="page"`; FAB tem `aria-label`.
- **Escopo:** mobile/tablet. Acima de 1024px o FAB não existe (a sidebar já
  oferece as ações de criação) em **todas** as páginas.
- **Customização:** a página pode ajustar apenas
  `--cb-v2-bottom-nav-inline`/`--cb-v2-bottom-nav-bottom` se precisar desviar de
  um elemento fixo; nunca o raio, a sombra ou o estado ativo.

### 20.4 Seletor de workspace

- **Fonte:** `app-shell.css` (`.cb-v2-workspace`, drawer e lista). Tokens:
  `--ui-nav-item-radius`, `--ui-nav-item-height`, `--ui-surface`,
  `--ui-border(-strong)`, `--ui-text*`.
- **Variantes:** expandido, recolhido (só ícone), drawer mobile.
- **Estados:** default, hover, focus, aberto (drawer), workspace ativo na lista.
- **Semântica:** `aria-haspopup="dialog"`, `aria-controls="workspaceDrawer"`;
  a cor do workspace é tema de instituição, não estado financeiro.
- **Dívida conhecida:** a lista do drawer usa `!important` para vencer
  utilitários Tailwind gerados por `workspace.js` (seção 16.8).

### 20.5 Page header

- **Fonte:** `premium.css` (`.ui-page-header`, `.ui-eyebrow`,
  `.ui-page-title`, `.ui-page-description`). Tokens: `--ui-fs-title`,
  `--ui-fw-title`, `--ui-fs-md`, `--ui-text*`, `--ui-section-gap`.
- **Variantes:** com/sem eyebrow, com/sem ação à direita; heading de identidade
  do Dashboard (`--ui-fs-display`, `--ui-fw-display`) — mesma escala, um degrau
  abaixo do page title.
- **Estados:** não interativo.
- **Escopo:** páginas `[data-ui="premium"]`; o Dashboard usa `.wallet-heading`
  com os mesmos tokens de tipo.
- **Customização:** a página ajusta composição (largura, alinhamento, margem)
  no próprio CSS, nunca tamanho/peso fora da escala.

### 20.6 Section header e icon tile

- **Fonte:** `premium.css` (`.ui-section-heading`, `.ui-section-title`,
  `.ui-section-icon`) e `dashboard-wallet.css` (`.wallet-section-*`, mesma
  geometria de 34px/raio 11). Tokens: `--ui-fs-lg`, `--ui-fs-sm`,
  `--ui-accent`, `--ui-text*`.
- **Variantes:** ícone em tile (34px; 30px no mobile do Dashboard), link de
  seção à direita.
- **Estados:** default, hover do link (`--ui-accent-strong`).
- **Dívida conhecida:** existem duas implementações com a mesma geometria
  porque o Dashboard não carrega `premium.css`. Extrair quando o Dashboard
  adotar a biblioteca.

### 20.7 Painéis, KPIs e linhas financeiras

- **Fonte:** `premium.css` (`.ui-panel`, `.ui-kpi*`, `.ui-list-panel`,
  `.ui-row*`, `.ui-avatar`). O painel do Dashboard (`.wallet-section`) consome
  os mesmos tokens via aliases `--wallet-*`, então o chrome (borda, raio 18px,
  sombra, superfície) é idêntico; o padding vem de `--ui-panel-pad` no próprio
  painel ou do `.ui-panel__body`. Tokens: `--ui-radius-panel`, `--ui-radius-card`,
  `--ui-radius-tile`, `--ui-shadow-panel`, `--ui-panel-pad`, `--ui-gap`,
  `--ui-border`, `--ui-surface*`, `--ui-text*`, `--ui-success/danger/warning`.
- **Variantes:** painel padrão/`--flush`, KPI neutro/perigo/sucesso/atenção,
  linha com status clicável, linha vencida (`is-overdue`), avatar com tons.
- **Estados:** default, hover, `aria-busy`/skeleton, vazio (`ui-empty`).
- **Semântica:** valores usam `tabular-nums`; cor só com estado financeiro;
  linha vencida nunca é só vermelha (badge + data).
- **Customização:** páginas usam as classes; variantes novas entram em
  `premium.css`, não no CSS da página.

### 20.8 Botões, pills e badges

- **Fonte:** `premium.css` (`.ui-btn*`, `.ui-pill`, `.ui-badge*`) para páginas
  Premium; `design-system.css` (`.cb-button*`) como ponte legada.
- **Tokens:** `--ui-radius-button`, `--ui-radius-pill`, `--ui-radius-badge`,
  `--ui-control-height(-sm)`, `--ui-action-bg(-hover/-fg)`,
  `--ui-disabled-opacity`, `--ui-focus`.
- **Variantes:** primary, secondary, ghost, `sm`; badge
  neutral/accent/success/warning/danger/info; pill de ação compacta.
- **Estados:** default, hover, active (`scale(.98)`), focus-visible, disabled.
- **Regras:** pills só para chips/ações curtas. `.cb-button` legado mantém
  caixa alta e peso 700; a unificação tipográfica com `.ui-btn` é migração de
  página (seção 21).

### 20.9 Tabs

- **Fonte:** `premium.css` (`.ui-tabs`/`.ui-tab`) para páginas Premium;
  `design-system.css` (`.cb-tabs`/`.cb-tab`/`.cb-tab-active`) para páginas que
  ainda não carregam `premium.css`; `dashboard.css` (`.cb-v2-tabs`) como base
  compartilhada do Dashboard/Faturas.
- **Tokens:** `--ui-radius-tab`, `--ui-radius-tab-item`, `--ui-tab-bg`,
  `--ui-tab-active-bg/fg/border`, `--ui-focus`, `--ui-transition`.
- **Contrato:** container raio 12px com padding 4px; item raio 8px, tipografia
  12px/620, altura 38px (44px no mobile Premium); ativo com superfície violeta
  + borda de seleção; nunca pill/oval. Medido em Contas, Metas, Contatos e
  Dashboard: mesmos raios, cores, borda, sombra e tipografia.
- **Variantes:** compacta (Dashboard, 32px, dentro de section header),
  largura total (`flex: 1`).
- **Estados:** default, hover, active (`aria-selected`/`aria-pressed`/`.is-active`),
  focus-visible, disabled.
- **Páginas consumidoras:** Contas (`.ui-tabs`), Dashboard (`.cb-v2-tabs`),
  Metas (`.cb-tabs`), Contatos (`.cb-v2-contatos-tabs`, alinhado aos mesmos
  tokens).
- **Customização:** a página define apenas largura/margem; não redefine cores
  nem geometria.

### 20.10 Seletor de mês (competência)

- **Fonte:** `design-system.css` (`[data-month-selector]`, `.month-btn`,
  `.month-control`), com uma única definição para
  Contas/Lançamentos/Relatórios/Faturas. Tokens: `--ui-radius-control`,
  `--ui-month-active-bg/fg/border`, `--ui-surface*`, `--ui-border*`.
- **Variantes:** trilho contínuo (12 meses) e botões de navegação.
- **Estados:** default, hover, `data-month-current`, `data-month-active`,
  focus-visible, disabled.
- **Semântica:** o mês ativo é seleção, não valor financeiro; o contrato HTMX
  de cada página permanece.

### 20.11 Inputs, selects e busca

- **Fonte:** `premium.css` (`.ui-input`, `.ui-select`, `.ui-textarea`,
  `.ui-search`, `.ui-label`, `.ui-toolbar`); ponte legada `.cb-input` em
  `design-system.css` + foco compartilhado em `app-shell.css`.
- **Tokens:** `--ui-control-height(-sm/lg)`, `--ui-radius-control`,
  `--ui-field-bg`, `--ui-field-border`, `--ui-accent`, `--ui-text-subtle`,
  `--ui-disabled-opacity`.
- **Estados:** default, hover, focus (anel de 3px), placeholder, disabled.
- **Regras:** busca ocupa 100% do container em Contas; `font-size: 16px` no
  mobile para evitar zoom. `.cb-input` legado mantém 40px de altura até a
  migração da página.

### 20.12 Empty, loading, modal e progresso

- **Fonte:** `premium.css` (`.ui-empty`, `.ui-skeleton`, `.ui-swipe*`) e
  `app-shell.css`/`overlay-stack.js` (`.cb-modal`, overlays, `#bottom-sheet-container`).
- **Tokens:** `--ui-radius-panel`, `--ui-surface*`, `--ui-border*`, `--ui-text*`,
  `--ui-shadow-panel`, `--ui-transition`.
- **Estados:** vazio com CTA, loading (`aria-busy`), aberto/fechado,
  `prefers-reduced-motion`.
- **Semântica:** foco preso no modal, ESC fecha, retorno de foco ao gatilho.
- **Customização:** páginas não recriam overlay global; usam os containers do
  shell.

### 20.13 Contrato de manutenção

1. **Nunca duplicar um componente compartilhado sem justificativa.** Se duas
   páginas precisam do mesmo componente, a implementação vive no arquivo-fonte
   (seção 20) e a página só consome classes/tokens.
2. **Não criar tokens paralelos.** Um valor novo entra em `ui-tokens.css` com
   nome semântico; se for exclusivo de um componente, fica no CSS do
   componente e é documentado.
3. **Não usar CSS de outra página como dependência visual implícita.**
   Import cruzado entre páginas (`contas.css` em Faturas, `dashboard-wallet.css`
   em Metas) é proibido.
4. **Variantes devem ser explícitas.** Uma diferença de contexto vira classe
   modificadora (`--compact`, `--flush`, `is-overdue`) ou token documentado —
   nunca um override solto em CSS de página.
5. **Toda mudança compartilhada exige regressão nas páginas consumidoras:**
   Dashboard, Contas, Lançamentos, Faturas, Metas, Relatórios, Configurações,
   Administração, Contatos — Dark/Light, 320/390/768/1280/1440/1600.
6. **Um novo agente consulta esta documentação antes de modernizar outra
   página** e atualiza os contratos quando criar um componente reutilizável.

## 21. Pendências atualizadas e próxima fase

### 21.1 Resolvido nesta etapa

- Sidebar única (geometria, tipografia, ícones, hover, foco, ativo, separador,
  grupos, submenus, recolhida, rodapé, workspace) em Dark/Light.
- Botão de recolher e controles do shell com um único tratamento.
- Appbar, bottom nav e FAB sob o shell, sem variante por página.
- Tokens de navegação, controles e tipografia de display na fonte canônica.
- `.cb-tabs` canônico (fim da reimplementação em Metas) e tabs de Contatos
  alinhadas — mesmos raios, cores e tipografia medidos nas quatro páginas.
- `premium.css` usando tokens semânticos de raio em vez de aliases.
- Reset `.cb-v2 button { font: inherit }` removido: componentes voltaram a
  renderizar a tipografia e a borda que declaram (seção 19.4).

### 21.2 Pendências históricas do shell (conferir a matriz 21.3)

1. Page headers legados (Faturas, Lançamentos, Metas, Relatórios,
   Configurações) em Tailwind → `.ui-page-header`.
2. `.cb-button` legado (caixa alta, 12px/700) x `.ui-btn` (12,5px/620):
   unificar a linguagem tipográfica por página. A geometria (raio 12) e o
   tamanho já são consistentes entre páginas.
3. `.cb-input` legado (40px) x `.ui-input` (42/44px): unificar altura.
4. Unificar `.ui-section-heading` e `.wallet-section-heading` quando o
   Dashboard adotar `premium.css`.
5. Separar `dashboard.css` (base do Dashboard x tile de Faturas) e remover
   `.cb-v2-tabs` quando todos os consumidores estiverem em `.ui-tabs`.
6. Substituir os `!important` da lista de workspaces por especificidade
   controlada.
7. Cache busting: `style.css`, `geist-sans.css`, `relatorios.css` e alguns JS
   ainda sem `assetPath`.
8. Escala global de breakpoints (hoje 359/389/639/767/1023/1279/1759 no
   canônico; 640/699/900/1180/1240 no legado).
8.1. **Estado de navegação, não de CSS:** em `/admin/*` o grupo "Administração"
   nasce fechado (`data-v2-nav-group-default="closed"`), então o subitem ativo
   fica oculto até o grupo ser aberto. Quando visível, ele usa exatamente o
   mesmo estado canônico (`#241f3d` + `#d9ccff` + raio 8 + 34px). Corrigir isso
   exige decidir em `app-shell.js` se um grupo com item ativo abre
   automaticamente — mudança de comportamento, fora do escopo desta auditoria
   visual.
9. Superfícies standalone/auth ainda fora de `.cb-v2`.
10. Unificar os dois cartões de crédito após validar os dois comportamentos.

### 21.3 Prontidão por página

| Página | Shell e tokens | Componentes compartilhados | Próximo passo |
| --- | --- | --- | --- |
| Dashboard | Pronto | `wallet-*` + componentes canônicos | Nenhum nesta fase |
| Contas | Pronto | `ui-*` completo | Nenhum nesta fase |
| Faturas | Pronto (herda) | card tile próprio; tabs base em `dashboard.css` | Migrar header, tabs e badges para `ui-*`; decidir cartão |
| Lançamentos | Pronto (herda) | lista densa, swipe, filtros, mês canônico | Migrar header, toolbar e linhas para `ui-*` |
| Metas | Pronto | header `ui-*`, KPIs, cards, históricos, sheets, tabs ARIA e empty states `ui-empty` | Nenhum nesta fase |
| Relatórios | Pronto (R2–R8) | header `ui-*`, KPIs, tabs suaves, gráficos tokenizados, tabelas e exportações; scroll cue e 44px touch | Nenhum nesta fase |
| Configurações | Pronto (CFG2–CFG5) | header, `ui-panel`/`ui-input`/`ui-btn`, tabs `cb-*`, listas compactas, privacidade, hub e perfil corporativo | Nenhum nesta fase |
| Administração | Pronto (ADM3–ADM4) | header `ui-*`, `ui-panel`/`ui-btn`/`ui-input`, badges, danger zones, tabela com `th scope`, foco/teclado, hint de overflow real e reset ao cancelar; envelope `cb-v2-admin-main` + `admin-premium.css` | Sincronizar o runtime e executar smoke autenticado |
| Contatos | Pronto (C2–C5) | header `ui-*`, cards `--ui-*`, `cb-tabs` ARIA, `ui-search`, `ui-empty`, sheets de criação/edição no container global e danger zone | Nenhum nesta fase |

#### Metas — padrões finais (M2–M8)

- **Cabeçalho:** `ui-page-header`/`ui-eyebrow`/`ui-page-title`/
  `ui-page-description`; ações `ui-btn`; privacidade no header com 44px no
  shell touch.
- **Indicadores:** `ui-panel` + `ui-kpi-grid`/`ui-kpi`, com `tabular-nums`,
  resumo de limites e planejamento de reservas.
- **Cards:** superfície `--ui-surface`, raio 14, borda canônica e sombra
  funcional; tons semânticos de `ui_tones.go`; barra de progresso própria
  com chip clampado (WAAPI), preservada.
- **Menus:** `cb-meta-menu` com alvo de 44px no shell touch, foco visível e
  fechamento por ESC/clique externo.
- **Históricos:** sheets `role="dialog"`, cabeçalho com tile, lista
  `ui-list-panel`/`ui-row`/`ui-badge`, `ui-empty`, fechar 44×44, foco inicial
  e trap.
- **Formulários:** `metas-sheet` com cabeçalho, corpo rolável e rodapé fixo;
  `ui-input`/`ui-btn`, `ui-label`, segmented `ui-tabs`, picker/icon-picker
  preservados, erros com `aria-live`/`aria-invalid` e foco por modality.
- **Privacidade:** `privacy.js` + `data-sensitive` + `body.privacy-enabled`;
  máscara `••••` mantém geometria; inputs editáveis não são mascarados.
- **Abas:** `role="tablist"`/`tab`/`tabpanel`, `aria-selected`,
  `aria-controls`, roving `tabindex`, setas/Home/End, ativação por
  Enter/Espaço e retorno de foco após o swap HTMX.
- **Empty states:** `ui-empty` + `role="status"` em Limites e Reservas;
  históricos M5 e mensagens aprovadas preservados.
- **App Shell:** appbar com 44×44 no mobile/tablet (tema, notificações e
  menu); sidebar e appbar desktop mantêm a geometria original.

#### Relatórios — padrões finais (R2–R8)

- **Cabeçalho:** `ui-page-header`/`ui-eyebrow`/`ui-page-title`/
  `ui-page-description` e privacidade global no header.
- **Indicadores:** `ui-kpi-grid`/`ui-kpi` com `tabular-nums`, tons semânticos
  de entrada/saída/resultado e contagem sem semântica monetária.
- **Abas:** `cb-tabs`/`cb-tab` suaves para modo e views; tab bar própria para
  a navegação principal. Rótulos sem quebra e 44px no contexto touch.
- **Gráficos:** donuts SVG inline com track/segmentos por token, legendas com
  nomes neutros e valores em `--ui-text`; strokes servidos preservados.
- **Tabelas:** `ui-panel` + separadores horizontais discretos, sem card por
  célula; `th scope="col"`, regiões roláveis, coluna Resultado/Projetado com
  realce por token. Descrições quebram em vez de truncar.
- **Rolagem:** dica “Deslize para ver mais colunas” via
  `relatorios-scroll.js`, exibida apenas com overflow real e oculta no fim;
  `aria-describedby` na região.
- **Exportações:** `ui-btn--secondary` + `ui-btn--sm`, 44px no touch;
  `aria-expanded`/`aria-controls` no botão do disclosure, menu com foco
  visível e hint de que o arquivo contém valores completos.
- **Privacidade:** `privacy.js` + `data-sensitive`; exportações mantêm os
  valores reais e a interface informa isso.
- **Acessibilidade:** foco visível (2px), `prefers-reduced-motion` e alvos
  de 44px escopados em `.cb-v2-relatorios-main` até 1023px.


> **Não iniciar novas fases** de modernização sem autorização própria.
> Metas/Reservas (M2–M8), Relatórios (R2–R8), Contatos (C1–C5), Configurações
> (CFG2–CFG5) e Administração (ADM3–ADM4) já tiveram suas fases de página.

#### Contatos — padrões finais (C2–C5)

- **Cabeçalho:** `ui-page-header`/`ui-eyebrow`/`ui-page-title`/
  `ui-page-description`, ação `ui-btn--primary` e privacidade global ao lado;
  no mobile o CTA cede lugar ao FAB e o toggle permanece com 44px.
- **Cards:** superfície `--ui-surface`, raio `--ui-radius-card`, avatar 40px
  com iniciais derivadas em runes, badge `ui-badge` por tipo, metadados em
  `dl` com hairlines e `overflow-wrap: anywhere` (sem truncar nome/e-mail).
  Grid `auto-fill minmax(300px, 1fr)`; `align` de altura natural.
- **Tabs:** `cb-tabs`/`cb-tab` com `role=tablist/tab`, `aria-selected`,
  `aria-controls`, roving `tabindex`, setas/Home/End e foco restaurado após
  HTMX. Sem contadores (total por tipo não tem semântica segura sob busca).
- **Busca:** `ui-search`/`ui-input--search`, debounce 300ms, `q` e `tipo`
  preservados; URL coerente (push/replace) e `partial=lista` apenas como
  dica de swap.
- **Criação/edição:** sheets no `#bottom-sheet-container` global (overlay
  stack + `closeBottomSheet`), `ui-label`/`ui-input`/`ui-select`, foco
  inicial, focus trap local, Escape, retorno de foco e safe area; criação e
  edição re-renderizam a lista filtrada.
- **Danger zone:** seção própria no sheet de edição, separada do rodapé,
  com confirmação global identificando o contato; exclusão bloqueada por FK
  mantém a lista e a mensagem.
- **Privacidade:** `privacy.js` + `data-sensitive` + `body.privacy-enabled`;
  máscara `••••` compacta (valor oculto em uma linha) e inputs editáveis
  nunca mascarados.
- **Acessibilidade/estados:** `ui-empty` com `role=status`, foco visível de
  2px, alvos 44px até 1023px e 32/38px no desktop, sem overflow horizontal.

#### Configurações — padrões finais (CFG2–CFG5)

- **Hub `/configuracoes`:** landing compacta com cards `ui-panel` ligados
  apenas às seções permitidas ao papel/workspace e bloco secundário de
  Administração somente para ADMIN global. A navegação global continua no
  App Shell; não existe segunda sidebar nem markup legado oculto.
- **Perfil e senha:** `ui-page-header`, painéis `ui-panel`, campos
  `ui-label`/`ui-input` com `for`/`id`, `autocomplete`, `required`, loading
  (`hx-disabled-elt`/`hx-sync`) e feedback `role=alert/status` focado após o
  swap. A área de senha usa `outerHTML` e um único `#passwordFormArea`.
- **Categorias:** `cb-tabs`/`cb-tab` com `aria-pressed` + `aria-controls`
  (botões alternáveis, sem misturar `role=tab`), `ui-search`, hierarquia em
  lista única com macro `ui-badge` e ações ghost de 44px no touch.
- **Contas/Cartões:** linhas compactas com valor `tabular-nums` e
  `data-sensitive`, badges de status/datas, conciliação inline e ações
  secundárias; sem cards por item e sem réplica do carrossel de faturas.
- **Perfil corporativo:** mesmo envelope Premium, formulário em duas
  seções (empresa e identidade visual), uploads com `ui-input` + botão de
  arquivo tokenizado. `config-masks.js` é carregado somente nesta página e
  fornece CNPJ/CPF e telefone via delegação `data-oninput`; não há
  auto-submit de foto nem carga global.
- **Privacidade:** `privacy.js` + `data-sensitive`; saldos, histórico,
  patrimônio e limites são mascarados, inputs permanecem editáveis. O
  Perfil corporativo não recebe máscara financeira.
- **Responsividade/acessibilidade:** inputs de 16px e alvos de 44px no
  mobile, colunas desktop com valores à direita, foco de 2px no teclado,
  `prefers-reduced-motion` preservado e zero overflow em 320–1440.
- **Administração:** ver a seção “Administração — padrões finais” abaixo. O
  envelope é próprio e separado de Configurações.

#### Administração — padrões finais (ADM3–ADM4)

- **Envelope:** `cb-v2-admin-main` + `premium.css` + `configuracoes.css` +
  `admin-premium.css`, com `data-ui="premium"` e `#settings-dynamic-payload`.
  Configurações, Perfil e App Shell não carregam a camada administrativa.
- **Cabeçalho:** `ui-page-header`/`ui-eyebrow`/`ui-page-title`/
  `ui-page-description`, badges de escopo (instância × workspace ativo) e
  remoção dos antigos headers mobile/slugs. A navegação continua exclusivamente
  no App Shell.
- **Usuários:** cards densos com identidade, status/2FA, workspaces e papéis
  em chips; edição com `aria-expanded`/`aria-controls`, foco no primeiro campo
  e retorno ao botão Editar após cancelar; recuperação de acesso em painel
  próprio, com foco no `confirm_email` e retorno à ação por Cancelar/ESC.
- **Workspaces:** cards com tipo/tema em badges, fatos corporativos, membros e
  logos. O cabeçalho do card permanece visível durante a edição, mantendo
  Editar/Cancelar alcançável; cancelar restaura os valores do servidor e
  reexibe os blocos Business quando aplicável. Máscaras CNPJ/telefone via
  `data-admin-mask`/`config-masks.js`.
- **Backups:** exportação e rotina automática em painéis informativos;
  restauração em painel de perigo com badge “Ação destrutiva”, consequências,
  dropzone acessível e botão danger. Contratos e formatos preservados.
- **Auditoria:** escopo do workspace ativo explícito; senha SMTP nunca
  renderizada (`value=""` + `autocomplete="new-password"`), badge de credencial,
  substituição por preenchimento e remoção explícita confirmada. Tabela com
  `th scope="col"`, região rolável `tabindex="0"`, foco por teclado e hint
  exibido apenas quando há overflow real (`admin-accessibility.js`).
- **Perigo e foco:** ações destrutivas usam ícone + rótulo + texto + cor
  semântica; foco de teclado de 2px, sem anel violeta intenso no mouse/touch.
  `prefers-reduced-motion` preservado. Após swap HTMX, o feedback
  `role=alert/status` recebe foco; filtros internos da Auditoria não roubam o
  foco. O modal global de confirmação, quando aberto pela Administração,
  recebe foco em Cancelar, mantém Tab/Shift+Tab no diálogo, devolve o foco ao
  acionador e usa botões de 44px em telas até 1023px. O fechamento por
  Escape/backdrop/overlay-stack é observado no próprio modal, pois o
  `modal-interceptor` consome Escape antes dos listeners de documento; o painel
  de recuperação não fecha por Escape enquanto houver camada superior.
- **Mobile:** inputs de 16px e alvos de 44px até 1023px; 1024/1440 mantêm a
  geometria desktop. Zero overflow do documento em 320/390/768/1024/1440;
  nomes/e-mails longos quebram com `overflow-wrap:anywhere`; a tabela rola
  apenas dentro do `.admin-table-wrap`.
- **Segurança:** nenhum segredo no HTML, nenhuma nova autorização, nenhum novo
  gerenciador de overlay/foco global. Os scripts ADM2 continuam sendo a única
  camada de interação administrativa; `admin-accessibility.js` cuida do foco
  pós-swap, da visibilidade do hint de rolagem e do foco do modal de
  confirmação administrativo.
