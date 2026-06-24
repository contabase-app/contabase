# ContaBase - Permissoes (RBAC)

## Perfis

| Perfil | Para que serve |
|--------|----------------|
| `ADMIN` | administra a instancia e o workspace |
| `MANAGER` | opera o dia a dia com acesso amplo ao workspace |
| `USER` | trabalha com permissao mais simples para uso comum |

## O que cada perfil pode fazer

`ADMIN` pode acessar as telas e acoes administrativas:

- gerenciar usuarios;
- gerenciar workspaces;
- exportar e importar backup;
- revisar auditoria;
- reconciliar o ledger das reservas;
- gerar dados de demonstracao para depuracao.

`MANAGER` e `USER` continuam limitados ao que o workspace e a tela permitirem. O acesso cruzado entre workspaces e bloqueado nas consultas.

## Como o sistema registra acoes

As acoes administrativas ficam registradas em log estruturado. Em geral:

- acoes sensiveis usam nivel de alerta maior;
- acoes de leitura ou criacao usam nivel informativo;
- cada registro inclui quem fez a acao e de onde ela veio.

## Permissoes customizadas

As permissoes customizadas por perfil existem no schema, mas nao estao ativas nesta fase do produto.

## Vinculo entre usuario e workspace

- um usuario pode pertencer a mais de um workspace;
- cada vinculo carrega um papel;
- o workspace ativo define o contexto carregado na interface.

## Limite conhecido

A tela de gerenciamento de membros ainda nao esta disponivel na interface publica. O caminho de administracao continua sendo o painel administrativo.
