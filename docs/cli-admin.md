# ContaBase - Admin CLI

O Admin CLI serve para manutenção local, diagnóstico operacional e recuperação de emergência. Use sempre com backup recente e confirme que está apontando para o banco correto antes de executar comandos que alteram dados.

## Visão rápida

| Comando                | Cenário                                                                        | Nível |
| ---------------------- | ------------------------------------------------------------------------------ | ----- |
| `workspaces list`      | Descobrir IDs de workspaces para outros comandos                               | baixo |
| `users reset-password` | Recuperar acesso de um usuário local                                           | alto  |
| `users disable-2fa`    | Destravar acesso quando o autenticador foi perdido                             | alto  |
| `users lockouts`       | Revisar ou limpar bloqueios persistentes de autenticação                       | médio |
| `repair-orphan-cards`  | Diagnosticar ou reparar cartões sem configuração                               | médio |
| `category-reseed`      | Revisar e aplicar limpeza segura + reseed canônico de categorias por workspace | alto  |

## Regras gerais

* Confirme sempre o `DATABASE_URL` antes de executar qualquer comando.
* Não use o CLI para alterar outra cópia do banco sem querer.
* Quando houver `--dry-run`, rode primeiro em modo simulação.
* Quando houver `--apply`, faça backup antes.
* O comando `category-reseed` exige `--confirm "RESEED CATEGORIES"` para alterar dados.
* No Docker e no binário, o comando é o mesmo; muda apenas o ambiente de execução.
* Não rode comandos destrutivos se houver dúvida sobre o workspace, banco ou relatório.

## Como executar

### Docker

Dentro do container da aplicação:

```bash
docker compose exec contabase ./admin workspaces list
docker compose exec contabase ./admin users reset-password --email usuario@example.com
docker compose exec contabase ./admin users disable-2fa --email usuario@example.com
docker compose exec contabase ./admin users lockouts list
docker compose exec contabase ./admin repair-orphan-cards --dry-run
docker compose exec contabase ./admin category-reseed --workspace-id <id>
```

`./admin` já vem junto da imagem Docker.

### Binário

Em instalações por binário, o fluxo recomendado é usar o mesmo ambiente do serviço e executar o binário compilado:

```bash
set -a
. /etc/contabase/contabase.env
set +a

go build -o admin ./cmd/admin

./admin workspaces list
./admin users reset-password --email usuario@example.com
./admin users disable-2fa --email usuario@example.com
./admin users lockouts list
./admin category-reseed --workspace-id <id>
```

Se quiser conferir qual banco está em uso:

```bash
printf '%s\n' "$DATABASE_URL"
```

O valor esperado em instalação por binário costuma ser parecido com:

```env
DATABASE_URL=file:/var/lib/contabase/contabase.db
```

## `workspaces list`

Lista todos os workspaces disponíveis no banco. É um comando somente leitura e não altera dados.

### O que faz

* Consulta a tabela `workspaces`.
* Exibe ID, tipo, nome e data de criação.
* Ordena por nome em ordem alfabética.

### Quando usar

* Antes de `category-reseed`, para descobrir o valor de `--workspace-id`.
* Para conferir quais workspaces existem no banco.
* Para auditoria ou diagnóstico operacional.

### Exemplo

```bash
./admin workspaces list
```

Saída esperada:

```text
ID                                    TYPE      NAME                 CREATED_AT
e44476a5-970f-4f78-94e9-b9d856517d8e  personal  Espaço Vitor         2025-06-01 10:30
1f8c23d5-af38-424c-8504-a688b477e138  business  Minha Empresa        2025-08-15 14:00
```

Copie o valor da coluna `ID` do workspace desejado para usar em outros comandos, por exemplo:

```bash
./admin category-reseed --workspace-id e44476a5-970f-4f78-94e9-b9d856517d8e
```

### Docker

```bash
docker compose exec contabase ./admin workspaces list
```

### Binário

```bash
go build -o admin ./cmd/admin
./admin workspaces list
```

## `users reset-password`

Recupera o acesso de um usuário local.

### O que faz

* Localiza o usuário pelo e-mail.
* Gera uma senha temporária.
* Grava o novo hash.
* Revoga sessões antigas.
* Marca o usuário para trocar a senha no próximo login.

### Quando usar

* Quando o usuário perdeu o acesso e ainda existe a conta local.
* Quando é preciso recuperar login sem mexer em permissões, workspaces ou dados financeiros.

### Exemplo Docker

```bash
docker compose exec contabase ./admin users reset-password --email usuario@example.com
```

### Exemplo binário

```bash
./admin users reset-password --email usuario@example.com
```

### Atenção

* A senha temporária aparece uma única vez no terminal.
* Entregue a senha por canal seguro.
* Depois, se necessário, valide o estado de bloqueio com:

```bash
./admin users lockouts list --all
```

## `users disable-2fa`

Desativa o 2FA de um usuário localmente.

### O que faz

* Remove o 2FA do usuário.
* Revoga sessões e desafios 2FA pendentes.
* Não altera senha, permissões, workspace ou dados financeiros.

### Quando usar

* Quando a senha está correta, mas o autenticador, os códigos de backup ou a chave usada no restore foram perdidos.

### Exemplo Docker

```bash
docker compose exec contabase ./admin users disable-2fa --email usuario@example.com
```

### Exemplo binário

```bash
./admin users disable-2fa --email usuario@example.com
```

## `users lockouts`

O login usa dois tipos de proteção:

* limite de tentativas em memória, que some ao reiniciar o processo;
* lockout persistente no SQLite, ligado ao usuário.

O CLI atua sobre o lockout persistente.

### `list`

Mostra os bloqueios ativos ou, com `--all`, todos os registros existentes no banco.

```bash
docker compose exec contabase ./admin users lockouts list
docker compose exec contabase ./admin users lockouts list --all
```

### `unlock`

Remove um bloqueio persistente por e-mail ou por `user-id`.

```bash
docker compose exec contabase ./admin users lockouts unlock --email usuario@example.com
docker compose exec contabase ./admin users lockouts unlock --user-id <id>
```

### `clear-expired`

Remove apenas bloqueios expirados.

```bash
docker compose exec contabase ./admin users lockouts clear-expired
```

### Quando usar

* Quando o usuário continua bloqueado mesmo após o tempo de bloqueio expirar.
* Quando é necessário limpar bloqueio persistente sem mexer em senha.

## `repair-orphan-cards`

Diagnostica e repara cartões de crédito sem configuração correspondente.

### O que faz

* Procura contas do tipo `CREDIT_CARD` sem linha correspondente em `credit_cards`.
* Em `--dry-run`, mostra o que seria reparado.
* Sem `--dry-run`, aplica o reparo.

### Quando usar

* Quando um cartão aparece no sistema, mas está sem configuração interna.
* Quando é preciso corrigir esse tipo de inconsistência sem reprocessar o restante do banco.

### Exemplo Docker

```bash
docker compose exec contabase ./admin repair-orphan-cards --dry-run
docker compose exec contabase ./admin repair-orphan-cards
```

### Exemplo binário

```bash
./admin repair-orphan-cards --dry-run
./admin repair-orphan-cards
```

### Atenção

* Rode sempre primeiro com `--dry-run`.
* Faça backup antes do reparo real.

## `category-reseed`

Executa a rotina segura de limpeza e reseed canônico de categorias por workspace.

### O que faz

* Executa `dry-run` ou `apply` da rotina explicitamente.
* Remove apenas categorias sem uso ou dependência.
* Preserva categorias com lançamentos, recorrências, limites, reservas e pais com filhos preservados.
* Recria ou reaplica o seed canônico do workspace alvo.
* Trabalha sempre em um workspace específico.

### Quando usar

* Após mudanças no seed canônico.
* Em DEV ou staging.
* Em cópia de banco real antes de produção.
* Para revisar o impacto de limpeza/reseed em um workspace específico.

### Quando não usar

* Sem backup.
* Direto em produção sem `dry-run`.
* Se o relatório mostrar conflitos não entendidos.
* Para todos os workspaces de uma vez.
* Como substituto de migration.
* Para corrigir categorias manualmente sem revisar o impacto.

### Segurança

* `dry-run` é o comportamento padrão.
* `--apply` é obrigatório para alterar dados.
* `--apply` exige confirmação textual exata:

```bash
--confirm "RESEED CATEGORIES"
```

* `--workspace-id` é obrigatório.
* Não existe execução automática no boot ou em migration.
* Não existe flag para todos os workspaces.
* Faça backup antes de `--apply`.

### Como descobrir o `workspace_id`

Antes de rodar `category-reseed`, liste os workspaces:

```bash
./admin workspaces list
```

No Docker:

```bash
docker compose exec contabase ./admin workspaces list
```

Use o valor da coluna `ID` no parâmetro `--workspace-id`.

Exemplo:

```bash
./admin category-reseed --workspace-id e44476a5-970f-4f78-94e9-b9d856517d8e
```

### Como interpretar o relatório

O comando imprime:

* workspace analisado;
* tipo do workspace;
* total de categorias antes;
* candidatas à remoção;
* preservadas por uso;
* preservadas por dependência;
* canônicas criadas;
* canônicas já existentes;
* conflitos;
* total de categorias depois, no `apply`.

### Procedimento recomendado

1. Faça backup.
2. Execute `./admin workspaces list` para obter o ID do workspace alvo.
3. Rode `dry-run`.
4. Revise o relatório.
5. Rode `apply` apenas se o resultado estiver coerente.
6. Valide as telas de Categorias, Lançamentos, Metas/Reservas e Relatórios.

### Bloqueios

Interrompa o `apply` se houver:

* conflitos inesperados;
* categorias usadas marcadas como removíveis;
* workspace errado;
* relatório vazio ou estranho;
* backup ausente.

### Go run

Dry-run padrão:

```bash
go run ./cmd/admin category-reseed --workspace-id <id>
```

Dry-run explícito:

```bash
go run ./cmd/admin category-reseed --workspace-id <id> --dry-run
```

Apply:

```bash
go run ./cmd/admin category-reseed --workspace-id <id> --apply --confirm "RESEED CATEGORIES"
```

### Binário

Listar workspaces:

```bash
go build -o admin ./cmd/admin
./admin workspaces list
```

Dry-run padrão:

```bash
./admin category-reseed --workspace-id <id>
```

Dry-run explícito:

```bash
./admin category-reseed --workspace-id <id> --dry-run
```

Apply:

```bash
./admin category-reseed --workspace-id <id> --apply --confirm "RESEED CATEGORIES"
```

### Docker

Listar workspaces:

```bash
docker compose exec contabase ./admin workspaces list
```

Dry-run padrão:

```bash
docker compose exec contabase ./admin category-reseed --workspace-id <id>
```

Dry-run explícito:

```bash
docker compose exec contabase ./admin category-reseed --workspace-id <id> --dry-run
```

Apply:

```bash
docker compose exec contabase ./admin category-reseed --workspace-id <id> --apply --confirm "RESEED CATEGORIES"
```
