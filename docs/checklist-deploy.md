# ContaBase - Checklist Final de Exposicao

Use este checklist antes de expor uma instancia para outras pessoas.
Ele serve para Docker e para qualquer ambiente em que o ContaBase fique acessivel por dominio.

## Antes de abrir para uso

- [ ] O dominio final esta correto em `APP_BASE_URL`.
- [ ] `ALLOWED_HOSTS` lista apenas os dominios esperados.
- [ ] `TRUSTED_PROXIES` aponta para o hop que fala direto com o ContaBase.
- [ ] `CONTABASE_ACCESS_MODE` esta coerente: `proxy` para HTTPS por proxy/tunnel, `lan` somente para IP privado em HTTP ou `local-docker` apenas para Docker local nesta maquina.
- [ ] HTTPS esta ativo no proxy, tunnel ou CDN.
- [ ] `CONTABASE_SETUP_TOKEN` sera removido depois do primeiro setup.
- [ ] `AUTH_ENCRYPTION_KEY` e `SECURITY_MASTER_KEY` estao guardadas em cofre seguro.
- [ ] Existe backup recente do banco e dos uploads.
- [ ] O restore ja foi testado ao menos uma vez.
- [ ] As permissoes dos diretorios de dados estao corretas.
- [ ] `APP_DEBUG=false` em producao.

## Validacoes rapidas

```bash
docker compose ps
docker compose logs --tail 100 contabase
curl -i http://localhost:8080/health
```

## Se a instancia usa proxy

- [ ] O proxy encaminha `Host`.
- [ ] O proxy encaminha `X-Forwarded-Proto`.
- [ ] O proxy encaminha `X-Forwarded-For`.
- [ ] O proxy nao expõe o app sem HTTPS.

## Depois do primeiro acesso

- [ ] Remover ou comentar `CONTABASE_SETUP_TOKEN`.
- [ ] Recriar a stack.
- [ ] Revalidar o healthcheck.
- [ ] Confirmar que o painel abre sem erros.

## Se algo mudar depois

- Mudou `.env.docker`? Recrie a stack.
- Mudou o modo de acesso? Revalide `CONTABASE_ACCESS_MODE`, `APP_BASE_URL` e `ALLOWED_HOSTS`.
- Mudou o proxy? Revalide `TRUSTED_PROXIES`.
- Mudou o dominio? Refaça `APP_BASE_URL` e `ALLOWED_HOSTS`.
- Mudou algo de dados? Refaça backup.

## Veja tambem

- [Configuracao](configuracao.md)
- [Seguranca](seguranca.md)
- [Backup e Restore](backup-restauracao.md)
- [Atualizacao](atualizacao.md)
- [Troubleshooting](solucao-de-problemas.md)
