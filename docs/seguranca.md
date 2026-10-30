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

O proxy confiável deve acrescentar o endereço que ele observou em `X-Forwarded-For`. O ContaBase usa a primeira origem não confiável ao caminhar da direita para a esquerda e não aceita `X-Real-IP` ou `CF-Connecting-IP` como fallback. Restrinja `TRUSTED_PROXIES` ao hop direto e mantenha a porta do aplicativo inacessível diretamente pela Internet.

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
- `SECURITY_MASTER_KEY` protege a senha SMTP com AES-256-GCM. Exige exatamente 32 bytes e deve ser configurada explicitamente em qualquer ambiente, inclusive desenvolvimento. Para gerar uma chave nova, use `openssl rand -hex 16`.

Preserve essas chaves em cofre seguro junto com os backups.

Sem senha SMTP persistida, a ausencia de `SECURITY_MASTER_KEY` nao impede a inicializacao. Se houver senha cifrada, a ausencia de chave ou uma chave incorreta impede o envio dos alertas SMTP; o aplicativo preserva o ciphertext e os demais recursos continuam disponiveis. Uma nova senha SMTP so pode ser gravada com uma chave valida.

Instalacoes antigas de desenvolvimento podem conter senhas SMTP cifradas com uma chave publica interna. Essa chave deixou de ser usada automaticamente, inclusive para leitura. Configurar uma chave nova nao recupera esse ciphertext. Preserve o banco e as chaves existentes antes de qualquer intervencao. Um administrador pode configurar uma chave forte persistente e informar novamente a senha SMTP na Administracao, substituindo a credencial de forma explicita. A remocao tambem exige a acao administrativa existente. Se for necessario recuperar o segredo antigo sem conhece-lo, planeje uma migracao offline especifica antes de alterar os dados.

Ao mover um banco entre ambientes ou restaurar um backup, mantenha a chave original correspondente. Nao gere uma chave a cada reinicio nem substitua uma chave existente durante uma atualizacao. Restaurar apenas o banco nao recupera a chave; os fluxos de backup nao a incluem automaticamente.

## Credenciais legadas

A identidade de seed historica nao e mais reativada no startup. Hashes vazios, invalidos ou correspondentes a senha publica conhecida sao inutilizados, e a conta passa a exigir recuperacao administrativa explicita. A contencao preserva status, vinculos, permissoes, dados de workspace e fatores 2FA. Nenhuma senha temporaria e emitida automaticamente pelo startup.

Um administrador recupera o acesso pela redefinicao de senha existente em `Administracao > Usuarios` ou pelo Admin CLI. O fluxo gera uma senha temporaria, exige troca no proximo login, revoga sessoes e desafios anteriores e mantem os fatores 2FA cadastrados. Se o autenticador tambem foi perdido, desative o 2FA em uma acao administrativa separada. Nao edite a tabela `users` diretamente e nao reintroduza um build que recrie a credencial publica.

## Recuperacao de atualizacao

Os fluxos de instalacao e atualizacao so reativam automaticamente um bundle ou imagem anterior quando existe prova administrativa local de que ele e uma copia verificada da propria Beta 6, com conteudo exato, emitida apos health valido, e quando a candidata ainda nao foi iniciada. A prova fica fora do bundle, pertence ao administrador local e nao e uma assinatura de fornecedor.

Nao existe downgrade automatico para a Beta 5. Depois de qualquer tentativa de startup da candidata, o retorno automatico e bloqueado, pois o schema pode ter avancado. Nesse caso, mantenha o servico parado e preserve banco, WAL, uploads, configuracao, chaves e backups. Repare a falha com um artefato corrigido, verificado e compativel com o schema atual. Restaurar banco ou voltar schema exige plano e autorizacao proprios; o fallback de codigo nao restaura banco, configuracao nem chaves.

A politica inicial reconhece somente `v1.0.0-beta.6`. Versoes futuras exigem revisao especifica antes de receber a mesma prova.

## Sessoes

- A sessão comum usa uma janela renovável de 24 horas e exige novo login após, no máximo, sete dias desde sua criação.
- A opção “manter conectado” usa uma janela renovável de 30 dias e exige novo login após, no máximo, noventa dias.
- Alterações de senha, MFA e ações administrativas de revogação podem encerrar sessões antes desses limites.
- Exportação e importação de backup, alterações globais de usuários e workspaces e escrita da configuração de auditoria exigem autenticação confirmada nos últimos dez minutos.
- A confirmação administrativa usa a senha atual e também o código TOTP atual quando o segundo fator está habilitado. Cinco tentativas em dez minutos são permitidas por usuário e IP.

Após uma atualização que introduza esses limites, uma sessão já mais antiga que o teto correspondente será encerrada na requisição seguinte.
Sessões criadas antes da introdução da confirmação administrativa continuam válidas para navegação normal, mas devem confirmar a identidade antes da primeira operação global protegida.

## Content Security Policy

O navegador recebe uma política CSP efetiva que permite JavaScript somente em arquivos servidos pelo próprio ContaBase. Scripts inline, execução por `eval`, atributos executáveis como `onclick`, URLs `javascript:`, frames e objetos são bloqueados.

Os templates usam atributos `data-*` inertes e delegação implementada em JavaScript externo. Novas telas e componentes devem seguir esse padrão e não podem introduzir handlers ou blocos de script inline.

Elementos `<style>` e atributos `style` são bloqueados. As regras estáticas ficam em arquivos CSS servidos pelo próprio ContaBase, e o HTMX não injeta seu stylesheet padrão no DOM.

Avisos, ícones, painéis de estado, legendas, vencimentos, categorias e contatos com valores fixos usam classes dos arquivos CSS do próprio origin. Fallbacks de avatar, filtros e modais também usam `hidden` e classes de layout. As cores dos gráficos de relatórios usam um atributo `data-report-stroke` inerte, aceito somente contra uma paleta fechada no stylesheet local.

Temas usam estados `data-*` de paleta fechada, barras usam elementos `<progress>` nativos e cores configuráveis são expostas por uma folha autenticada que consulta apenas o workspace ativo e aceita somente `#RRGGBB`. Swipe, notificações e tooltips usam a Web Animations API para valores geométricos calculados sem escrever no atributo `style`. A política aplica `style-src-attr 'none'`, e a regressão rejeita novas escritas inline nos templates e scripts de produção.

## Rate limit

O ContaBase limita tentativas em rotas de autenticação e operações sensíveis. Cada processo mantém até 10.000 buckets em memória, identificados somente por hashes SHA-256. Entradas expiradas são removidas periodicamente e imediatamente quando o teto precisa ser reaproveitado.

Quando todos os buckets ainda estão ativos, uma identidade nova recebe `429 Too Many Requests` e `Retry-After`. O limitador preserva os buckets existentes em vez de expulsá-los, evitando que a rotação de identidades reinicie a cota de um alvo já acompanhado.

Os buckets são locais ao processo e são perdidos em uma reinicialização. Em uma única réplica, o bloqueio persistente de conta complementa essa limitação. Em múltiplas réplicas, configure também um rate limit compartilhado no proxy, tunnel ou edge e impeça acesso direto à porta de origem; sem isso, cada réplica aplica sua própria cota.

## Backups

- Mantenha backup recente do banco.
- Preserve tambem os uploads.
- Teste restore antes de confiar no processo.

## Permissoes

Os arquivos de dados devem ficar em diretorios que apenas o usuario da aplicacao possa alterar.
No modo Docker, isso normalmente envolve o volume persistente da stack.
No modo binario/systemd, os dados ficam em `/var/lib/contabase`.

Fotos de perfil são servidas somente para usuários autenticados quando a pessoa pertence ao workspace ativo. Logos também exigem autenticação e vínculo com o workspace correspondente. Preserve o diretório de uploads no backup; conhecer uma URL de imagem não substitui a autorização da aplicação.

## Atualizacao

Atualizar sem revisar log, backup e configuracao aumenta o risco de interrupcao.
Antes de atualizar, confirme:

1. backup recente;
2. segredo preservado;
3. proxy e dominio corretos;
4. healthcheck funcionando;
5. `AUTH_ENCRYPTION_KEY` e `SECURITY_MASTER_KEY` preservadas e coerentes com o banco;
6. contas legadas que dependam de recuperacao administrativa identificadas;
7. plano de reparo caso a candidata falhe depois do startup, sem presumir rollback de schema.

## O que esta fora do escopo

Este documento nao promete seguranca total.
Ele resume cuidados operacionais comuns para reduzir erro humano e exposicao desnecessaria.

## Veja tambem

- [Configuracao](configuracao.md)
- [Backup e Restore](backup-restauracao.md)
- [Atualizacao](atualizacao.md)
- [Checklist final](checklist-deploy.md)
