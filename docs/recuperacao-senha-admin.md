# ContaBase - Recuperacao de Senha e Acesso

Este guia resume o fluxo de recuperacao quando um usuario esquece a senha ou perde o 2FA. Para os comandos detalhados, veja [Admin CLI](cli-admin.md).

## Fluxo normal no painel

1. O usuario pede a redefinicao a um administrador.
2. O administrador abre `Admin > Usuarios`.
3. O administrador gera uma senha temporaria.
4. O sistema revoga as sessoes antigas daquele usuario.
5. O usuario entra com a senha temporaria e define uma senha nova.

## Quando o painel nao resolve

Se o unico administrador perdeu o acesso ou o 2FA, use o Admin CLI no mesmo banco da instancia.

```bash
docker compose exec contabase ./admin users reset-password --email usuario@example.com
docker compose exec contabase ./admin users disable-2fa --email usuario@example.com
docker compose exec contabase ./admin users lockouts list
```

Em binario/systemd:

```bash
set -a
. /etc/contabase/contabase.env
set +a

go run ./cmd/admin users reset-password --email usuario@example.com
go run ./cmd/admin users disable-2fa --email usuario@example.com
go run ./cmd/admin users lockouts list
```

## Regras de seguranca

- Faca backup antes de qualquer recuperacao.
- Nao edite a tabela `users` direto no SQLite.
- Se a chave de criptografia mudou no restore, o 2FA antigo pode precisar ser desativado e configurado de novo.

## Boas praticas

- Mantenha pelo menos dois administradores.
- Guarde backups e variaveis sensiveis fora do servidor principal.
- Teste o login apos a recuperacao em janela anonima ou apos logout completo.
