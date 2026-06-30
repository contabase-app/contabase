# ContaBase - Seguranca

## Objetivo

Este guia resume os cuidados operacionais mais importantes para usar o ContaBase com menos risco.
Ele nao substitui um administrador atento ou boas praticas do servidor.

## Pontos essenciais

- Use senha forte para administradores.
- Gere um `CONTABASE_SETUP_TOKEN` forte e remova depois do primeiro acesso.
- Exponha a instancia na internet somente por HTTPS.
- Use reverse proxy, tunnel ou CDN confiavel.
- Mantenha backups regulares.
- Proteja `AUTH_ENCRYPTION_KEY` e `SECURITY_MASTER_KEY`.
- Revise permissões de arquivos e diretorios.
- Atualize a instalacao com cuidado.

## Setup token

O `CONTABASE_SETUP_TOKEN` protege o primeiro setup e restauracoes criticas.
Depois do primeiro acesso, remova ou comente esse token e recrie a stack.

## HTTPS e proxy

Use HTTPS para acessos externos.
Se houver proxy reverso, configure:

- `APP_BASE_URL` com o endereco publico real;
- `ALLOWED_HOSTS` com os dominios permitidos;
- `TRUSTED_PROXIES` com o hop que fala direto com o ContaBase.

Exemplo:

```ini
APP_BASE_URL=https://fin.seu-dominio.com
ALLOWED_HOSTS=fin.seu-dominio.com
TRUSTED_PROXIES=172.16.0.0/12
CONTABASE_ACCESS_MODE=proxy
```

## LAN privada sem proxy

HTTP direto sem proxy so e aceito em modo LAN explicito, para IP privado RFC1918:

```ini
APP_BASE_URL=http://192.168.1.50:8080
ALLOWED_HOSTS=192.168.1.50
TRUSTED_PROXIES=
CONTABASE_ACCESS_MODE=lan
```

Esse modo nao libera dominio publico, IP publico nem `0.0.0.0` como autorizacao ampla. `ALLOWED_HOSTS` continua obrigatorio, mas nao substitui `CONTABASE_ACCESS_MODE`.

## Docker local somente nesta maquina

Quando o ContaBase roda em container local, o app pode ver o host como IP privado da bridge/NAT mesmo que o navegador use `localhost`. Para esse caso, use modo explicito de Docker local:

```ini
APP_BASE_URL=http://localhost:8080
ALLOWED_HOSTS=localhost,127.0.0.1,::1
TRUSTED_PROXIES=
CONTABASE_ACCESS_MODE=local-docker
```

Esse modo nao libera dominio publico, IP publico HTTP nem acesso por outros dispositivos da rede. Use apenas para Docker local nesta maquina.

## Chaves de seguranca

- `AUTH_ENCRYPTION_KEY` protege segredos ligados a autenticacao e 2FA.
- `SECURITY_MASTER_KEY` protege segredos internos quando usados.

Preserve essas chaves em cofre seguro junto com os backups.

## Backups

- Mantenha backup recente do banco.
- Preserve tambem os uploads.
- Teste restore antes de confiar no processo.

## Permissoes

Os arquivos de dados devem ficar em diretorios que apenas o usuario da aplicacao possa alterar.
No modo Docker, isso normalmente envolve o volume persistente da stack.
No modo binario/systemd, os dados ficam em `/var/lib/contabase`.

## Atualizacao

Atualizar sem revisar log, backup e configuracao aumenta o risco de interrupcao.
Antes de atualizar, confirme:

1. backup recente;
2. segredo preservado;
3. proxy e dominio corretos;
4. healthcheck funcionando.

## O que esta fora do escopo

Este documento nao promete seguranca total.
Ele resume cuidados operacionais comuns para reduzir erro humano e exposicao desnecessaria.

## Veja tambem

- [Configuracao](configuracao.md)
- [Backup e Restore](backup-restauracao.md)
- [Atualizacao](atualizacao.md)
- [Checklist final](checklist-deploy.md)
