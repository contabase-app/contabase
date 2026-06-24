# ContaBase - Arquitetura

ContaBase e uma aplicacao web em Go com HTML renderizado no servidor, SQLite para persistencia e HTMX para deixar a interface mais dinamica sem depender de um SPA pesado. O desenho favorece instalacao self-hosted, dados locais e uma superficie operacional menor.

## Visao geral

| Componente | Papel |
|------------|------|
| Backend | Recebe requisições, aplica regras de negocio e valida autenticacao |
| Banco | SQLite em arquivo unico, com escrita controlada |
| Interface | Templates HTML no servidor com melhorias progressivas |
| Estilo | Tailwind compilado localmente |
| Icones e fontes | Servidos junto com a aplicacao, sem CDN |

## Como isso funciona

- A aplicacao roda como um processo principal.
- O SQLite trabalha com um escritor por vez.
- Cada workspace fica separado nas consultas.
- Regras de seguranca, permissao e autenticao sao aplicadas no servidor, nao no navegador.

## Estrutura de diretorios

```text
cmd/server/          # servidor HTTP principal
cmd/admin/           # CLI administrativa local
internal/
  auth/              # autenticacao, sessao e CSRF
  database/          # SQLite, migracoes e seeds
  handlers/          # handlers HTTP
  models/            # modelos de dados
  repository/        # acesso a dados
  security/          # rate limit e fronteiras de seguranca
  services/          # regras de negocio
templates/           # templates HTML
assets/              # CSS, JS, fontes e imagens
scripts/             # scripts publicos de operacao
```

## Regras importantes

- As consultas filtram por `workspace_id` para evitar mistura de dados entre workspaces.
- O banco e tratado como fonte de verdade local, por isso backup e restore precisam incluir o arquivo SQLite e os anexos.
- O uso de HTTPS, proxy reverso e hosts permitidos faz parte da operacao segura da instancia.

## Camadas de seguranca

- HTTPS deve ser usado quando a instancia sair da rede local.
- `ALLOWED_HOSTS` reduz risco de host inesperado.
- CSP, HSTS e outros headers protegem o navegador.
- CSRF, cookie HTTP-only, RBAC e 2FA ajudam no controle de acesso.
- Limites de taxa reduzem abuso em rotas sensiveis.

Consulte [Seguranca](seguranca.md) para a visao operacional e [Regras Financeiras](regras-financeiras.md) para os detalhes de calculo.
