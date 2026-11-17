# Atualização

Este guia mostra como atualizar o ContaBase.

Para escolher o método e verificar a disponibilidade da versão, consulte o [Guia Completo de Instalação](https://contabase.pages.dev/setup/). Os procedimentos abaixo preservam os detalhes técnicos de atualização e recuperação.

Regra principal:

> Antes de atualizar, faça backup.

Leia também:

[Backup e restauração](backup-restauracao.md)

## Source Bootstrap: sem atualização suportada

O **Source Bootstrap** está adiado e indisponível nesta fase: novas execuções são recusadas antes de qualquer alteração. Não possui atualização suportada e não deve ser confundido com o **Source legado**.

- O Source legado mantém o comportamento atual: exige checkout Git persistente,
  branch com upstream e usa `scripts/update-contabase-source.sh`.
- Instalações experimentais anteriores do Source Bootstrap usavam checkout destacado na tag e estado
  `CONTABASE_INSTALL_METHOD=source-bootstrap`, sem instalar `contabase-update` nem `cb-update`.

Não use `contabase-update`, `cb-update` ou
`scripts/update-contabase-source.sh` em uma instalação Source Bootstrap. O
wrapper falha fechado para esse estado. A atualização por tag/commit, a retomada
manual e as melhorias operacionais estão planejadas para Betas futuras; até
lá, não trate essa instalação como produção com dados reais.

Para novas instalações VPS/LXC, o caminho recomendado é o binário da Release,
que possui atualização suportada.

## Método recomendado: comando global

Após instalar, o ContaBase registra o comando `contabase-update` no sistema.
Para atualizar, execute:

```bash
sudo contabase-update vX.Y.Z
```

O comando detecta automaticamente se sua instalação é binary (LXC/VPS), Docker
ou Source legado com checkout persistente e chama o script de update correto.
Ele não se aplica a instalações Source Bootstrap.

### Contratos dos dispatchers Beta 7 e Beta 8

- **Dispatcher Beta 8:** valida canal, Release, identidade da tag/commit e manifesto `updater.sha256`; baixa e verifica o bundle antes de delegar ao updater. A versão explícita deve estar autorizada pelo canal ou pelo catálogo de versões. Sem identidade ou manifesto válidos, falha fechado.
- **Dispatcher Beta 7:** no modo Docker, executa o updater presente no checkout e repassa a versão explícita em `CONTABASE_VERSION`. Com uma versão explícita, seleciona `pinned`; não autentica completamente o bundle antes de executar o helper. Esse é o contrato legado da transição para a Beta 8.
- A validação da origem e da identidade da tag feita **dentro do helper já iniciado** não equivale à autenticação do bundle antes da execução. A chamada direta do helper também não oferece as garantias do dispatcher Beta 8.

O checkout da tag Beta 8 troca os scripts locais, mas não substitui por si só o comando instalado em `/usr/local/bin/contabase-update`. O dispatcher Beta 8 é reinstalado ao final de uma atualização concluída com sucesso.

Os updaters da Beta 8 exigem `flock` (util-linux) para serializar a atualização.
Sem essa ferramenta, o update falha fechado antes de alterar a instalação.

Atalho curto:

```bash
sudo cb-update vX.Y.Z
```

Sem versão explícita, o comando resolve o canal salvo; instalações `pinned` exigem uma versão ou seleção explícita de canal. Para a transição Beta 7 → Beta 8, informe sempre a versão.

Opcional: `sudo contabase-update --help`

Este comando é instalado em `/usr/local/bin/contabase-update` e
`/usr/local/bin/cb-update` durante a instalação inicial.

### Atualização headless

```bash
sudo env CONTABASE_ASSUME_YES=1 contabase-update vX.Y.Z
```

### Instalações antigas

Se você instalou o ContaBase antes do comando `contabase-update` existir, ele pode não estar disponível no seu sistema.

Nesse caso, use o script manual de atualização uma vez conforme seu tipo de instalação:

* **Docker:** `CONTABASE_VERSION=vX.Y.Z ./scripts/update-contabase-docker.sh`
* **Source legado (checkout persistente):** `sudo ./scripts/update-contabase-source.sh`
* **Release/binário:** `sudo env CONTABASE_VERSION=vX.Y.Z bash scripts/update-contabase-release.sh`

Em checkout Docker por tag, a chamada direta exige `CONTABASE_VERSION=vX.Y.Z`
explícita.

O script `update-contabase-source.sh` não é compatível com Source Bootstrap: ele
rejeita HEAD destacado e exige branch/upstream. Se sua instalação foi criada
pelo Source Bootstrap, não execute esse updater.

Antes de executar scripts diretamente, habilite a execução conforme o método usado:

```bash
chmod +x scripts/install.sh scripts/update-contabase-docker.sh scripts/update-contabase-source.sh scripts/update-contabase-release.sh
```

Após a atualização, verifique se o comando foi instalado:

```bash
command -v contabase-update
command -v cb-update
```

Se ainda não aparecer, execute o script manual mais uma vez — pode ser que a primeira execução
tenha trazido os scripts novos, mas o script antigo ainda estava rodando.

Depois disso, use normalmente:

```bash
sudo contabase-update vX.Y.Z
```

Esse fallback legado não equivale à autenticação anterior à execução oferecida pelo dispatcher Beta 8. Para checkout Docker por tag na Beta 7, siga primeiro a transição específica abaixo; apenas definir a variável não torna o helper antigo compatível com HEAD destacado.
O menu guiado contém apenas instalação, scripts manuais e saída. Os métodos não interativos `CONTABASE_INSTALL_METHOD=update-docker`, `update-release` e `update-source` continuam disponíveis para compatibilidade.

## Fallback legado: scripts da versão

Para instalações sem comando global, os scripts da versão mantêm o fallback legado descrito abaixo. Quando o dispatcher Beta 8 estiver instalado, prefira o comando global autenticado.

## 1. Escolha a nova versão

Exemplo:

```bash
export CONTABASE_VERSION=vX.Y.Z
```

Quando sair uma versão nova, troque somente o valor:

```bash
export CONTABASE_VERSION=vX.Y.Z
```

Não use `main`.

Não use `latest`.

Use uma versão publicada no GitHub Releases.

## 2. Verifique a versão atual

### LXC/VPS (Release)

```bash
cat /opt/contabase/VERSION
```

No Source legado, esse arquivo não é instalado. Consulte a versão registrada no estado da instalação:

```bash
sudo grep '^CONTABASE_INSTALLED_VERSION=' /etc/contabase/install-state.env
```

### Docker

Entre na pasta onde está o `docker-compose.yml` e rode:

```bash
docker compose exec contabase cat /app/VERSION
```

## 3. Atualizar LXC/VPS

Use este método se você instalou sem Docker, com binário pronto e systemd.

Baixe o updater da tag desejada:

```bash
curl -fsSLo /tmp/update-contabase-release.sh \
  "https://raw.githubusercontent.com/contabase-app/contabase/vX.Y.Z/scripts/update-contabase-release.sh"
```

Execute a atualização:

```bash
sudo env \
  CONTABASE_VERSION=vX.Y.Z \
  CONTABASE_ASSUME_YES=1 \
  bash /tmp/update-contabase-release.sh
```

Quando esse script é executado isolado, sem `scripts/lib`, como acontece no
`contabase-update` da beta.7, ele autentica o dispatcher da versão no canal
(`updaters/<versao>/updater.sha256`), confere commit, SHA-256 e versão e entrega
o controle ao dispatcher. O bundle completo é revalidado antes de qualquer
mutação. Se o canal ainda não publicou a versão, o bootstrap falha fechado sem
alterar a instalação.

O updater preserva:

- `/etc/contabase/contabase.env`;
- secrets;
- `CONTABASE_SETUP_TOKEN`, se ainda existir;
- banco SQLite;
- uploads;
- backups;
- `/var/lib/contabase`.

Ele troca o binário e os arquivos da aplicação.

O backup pré-update não significa rollback automático. O updater não restaura banco automaticamente.

Se a troca do bundle falhar antes de qualquer tentativa de startup da candidata, o updater só pode recuperar o bundle anterior quando ele for uma cópia qualificada da Beta 6, com identidade exata e prova administrativa local válida. Beta 7, Beta 8 e versões desconhecidas não são destinos de rollback automático. Confirme `systemctl status contabase` e o healthcheck quando houver recuperação permitida.

Após qualquer tentativa de startup da candidata, o retorno automático ao código anterior fica bloqueado, pois os dados podem ter sido migrados. Em caso de falha, o serviço pode permanecer indisponível até intervenção administrativa especializada: preserve banco, WAL, uploads, configuração, chaves e pacote pré-update e repare com um artefato verificado compatível com o schema atual. O fallback de bundle não reverte migrations nem restaura dados; restore exige decisão explícita e o procedimento de [backup e restauração](backup-restauracao.md).

`CONTABASE_SETUP_TOKEN` é usado somente para bootstrap/setup inicial. Se você já criou o primeiro administrador e removeu ou comentou esse token, o update deve continuar normalmente. Não recrie esse token para update comum; reset ou novo setup devem seguir um procedimento próprio.

## 4. Atualizar Docker

Entre na pasta do projeto:

```bash
cd contabase
```

Execute:

```bash
CONTABASE_VERSION=vX.Y.Z \
CONTABASE_INSTALL_METHOD=update-docker \
CONTABASE_ASSUME_YES=1 \
./scripts/install.sh
```

Ou chame o updater direto:

```bash
CONTABASE_VERSION=vX.Y.Z ./scripts/update-contabase-docker.sh
```

Substitua `vX.Y.Z` pela versão publicada desejada. Em checkout por tag, essa variável é obrigatória; em branch, deve corresponder ao `VERSION` do alvo. O helper direto não autentica canal, Release, commit autorizado e manifesto como o dispatcher Beta 8.

### Transição única de checkout por tag (beta.7 → beta.8)

Execute este procedimento somente após a disponibilização oficial da Beta 8 para esse método e da tag `v1.0.0-beta.8`. A existência destas instruções não comprova publicação nem promoção do canal.

Se o checkout foi criado com `git clone --branch v1.0.0-beta.7`, ele está com HEAD destacado. O helper Beta 7 recusa esse estado, e o dispatcher Beta 7 executa esse mesmo helper local. Por isso, é necessário trocar primeiro o checkout pela tag Beta 8.

Antes de qualquer checkout, confirme todos os pré-requisitos:

1. Checkout da origem oficial `https://github.com/contabase-app/contabase.git` (ou forma SSH equivalente), no diretório registrado pela instalação.
2. Worktree limpo, sem mudanças no staging nem arquivos não rastreados não ignorados.
3. Estado legado válido em `/etc/contabase/install-state.env`, com método `docker`, versão instalada `v1.0.0-beta.7` e caminho correto do checkout. Estado ausente, parcial ou divergente bloqueia a transição; não apague nem edite esse arquivo para contornar validações.
4. Container Beta 7 em execução, Docker health `healthy`, endpoint `/health` saudável e `/app/VERSION` efetivamente igual a `v1.0.0-beta.7`. Não basta ler o `VERSION` do checkout.
5. [Backup adequado](backup-restauracao.md) do banco, uploads, configuração e chaves, com procedimento de restauração conhecido, antes de atualizar. O pacote canônico criado pelo updater não autoriza restore automático.
6. Dispatcher Beta 7 instalado, `sudo`, Docker/Compose e `flock` (util-linux) disponíveis, além das dependências do helper.

Conferências locais antes do checkout (ajuste a URL à porta efetiva da instalação):

```bash
cd /caminho/do/contabase
git remote get-url origin
git status --porcelain
sudo grep -E '^(CONTABASE_INSTALL_METHOD|CONTABASE_INSTALLED_VERSION|CONTABASE_REPO_PATH)=' /etc/contabase/install-state.env
docker compose ps --all
docker inspect --format '{{.State.Health.Status}}' "$(docker compose ps -q contabase)"
curl -fsS http://127.0.0.1:8080/health
docker compose exec -T contabase cat /app/VERSION
```

O status Git precisa estar vazio, a saúde deve ser `healthy` e `{"status":"healthy"}`, e a versão do runtime deve ser Beta 7. Se a identidade ou a saúde anterior não puder ser comprovada, pare: a atualização permanece bloqueada. Preserve o estado e os dados para diagnóstico.

Com os pré-requisitos confirmados e o backup concluído:

```bash
git fetch origin tag v1.0.0-beta.8
git checkout v1.0.0-beta.8
sudo contabase-update v1.0.0-beta.8
```

Após o checkout manual, o `VERSION` local e os scripts passam a Beta 8, enquanto o runtime e o estado instalado ainda representam Beta 7. A comprovação do runtime anterior saudável permite ao helper reconhecer essa transição; sem ela, a divergência entre estado e checkout pode provocar recusa.

O comando instalado ainda é o **dispatcher Beta 7**: ele fornece `CONTABASE_VERSION=v1.0.0-beta.8` e executa o helper Beta 8 do checkout, sob o contrato legado, sem autenticação completa do bundle antes dessa execução. O helper busca a tag na origem oficial, compara o commit da tag local com o remoto, verifica `VERSION`, aceita HEAD destacado, cria o pacote canônico e valida a identidade e a saúde do novo runtime. Só após o sucesso finaliza checkout/estado e reinstala o **dispatcher Beta 8** para as próximas atualizações. Banco, uploads e configuração são preservados.

Para execução não interativa, use `sudo contabase-update --yes v1.0.0-beta.8`. Se o novo runtime falhar após iniciar, a imagem Beta 7 não é reativada automaticamente; restore exige decisão explícita. Não use `--force`, `reset --hard`, `clean`, exclusão de estado ou `docker compose pull && up -d` para contornar o procedimento. Veja [Instalação com Docker](instalacao-docker.md#transição-única-de-instalação-criada-por-tag-beta7--beta8) para a alternativa com branch local.

O updater Docker preserva:

- `.env.docker`;
- secrets;
- banco;
- uploads;
- backups;
- volumes.

## 5. Atualizar Docker manual com GHCR

Se você usa o compose manual com imagem GHCR:

```bash
docker compose pull
docker compose up -d
```

Se quiser trocar a versão fixa, edite o `docker-compose.yml`.

Exemplo:

```yaml
image: ghcr.io/contabase-app/contabase:vX.Y.Z
```

Depois rode:

```bash
docker compose pull
docker compose up -d
```

## 6. Conferir se funcionou

### LXC/VPS

```bash
systemctl status contabase
curl http://127.0.0.1:8080/health
```

Confira a versão pelo arquivo `VERSION` na Release ou pelo estado da instalação Source legado, conforme a seção 2.

### Docker

```bash
docker compose ps
curl http://127.0.0.1:8080/health
docker compose exec contabase cat /app/VERSION
```

## 7. Se algo der errado

### LXC/VPS

Veja os logs:

```bash
journalctl -u contabase -n 100 --no-pager
```

Siga os logs:

```bash
journalctl -u contabase -f
```

### Docker

Veja os logs:

```bash
docker compose logs --tail 100 contabase
```

## Dry-run e validação

### LXC/VPS

Se você clonou o repositório ou tem o script de update local:

```bash
sudo env CONTABASE_VERSION=vX.Y.Z ./scripts/update-contabase-release.sh --dry-run
```

### Docker

```bash
CONTABASE_VERSION=vX.Y.Z ./scripts/update-contabase-docker.sh --dry-run
```

Use uma versão publicada. Em checkout por tag, a variável é obrigatória; esse dry-run direto mantém o contrato do helper, sem autenticação prévia do bundle pelo dispatcher Beta 8.

Dry-run valida o que seria feito sem atualizar de verdade.

## O que a atualização não faz

A atualização não deve:

- apagar banco;
- apagar uploads;
- apagar backups;
- sobrescrever secrets;
- trocar chaves de segurança;
- exigir ou recriar `CONTABASE_SETUP_TOKEN` quando a instalação já existe;
- restaurar banco automaticamente;
- reverter migrations ou o schema automaticamente;
- atualizar uma instalação Source Bootstrap, que não possui updater suportado;
- reativar uma conta legada com senha pública.

Se precisar restaurar backup, faça isso manualmente e com cuidado.

## Beta 8: schema 83 e remoção de Reservas

Ao atualizar de `v1.0.0-beta.7` (schema 82) para `v1.0.0-beta.8`, o ContaBase aplica a migration 83 (`reserve_physical_removal`). Ela remove as tabelas e o histórico de Reservas e estruturas exclusivas associadas; contas, transações, faturas, pagamentos, recorrências, limites e Investimentos são preservados.

- A migration é transacional, mas não possui down migration.
- O preflight aborta antes de qualquer DROP se houver vínculo histórico protegido, referência cruzada ou inconsistência de integridade.
- O updater cria o pacote canônico pré-update, incluindo o snapshot pré-migration com manifesto de integridade.
- O retorno à Beta 7 exige restore explícito do pacote pré-update e descarta gravações feitas depois dele. Não há downgrade in-place após o banco ser aberto no schema 83.
- `CONTABASE_FEATURE_INVESTMENTS` continua com a política da Beta 7: instalações novas nascem com `false`; atualizações preservam o valor existente; ausência da variável mantém o comportamento legado habilitado.
- A recuperação automática histórica continua restrita à Beta 6. Beta 7 e Beta 8 não são destinos de rollback automático.

## Beta 8: recuperação automática avançada adiada

A Beta 8 não será qualificada como destino de rollback automático. A política histórica permanece restrita à Beta 6, mediante prova administrativa local válida, identidade exata do conteúdo e ausência de tentativa de startup da candidata. Beta 7, Beta 8 e versões desconhecidas continuam recusadas.

O método `docker-image`, instalado pelo Bootstrap e fixado por digest, não realiza rollback automático nem emite ou consome provas de recuperação. Backup e healthcheck não mudam esse limite. Falhas podem exigir intervenção administrativa especializada, com indisponibilidade até a recuperação segura; nenhum método restaura dados automaticamente.

## Beta 7 publicada: Investimentos, cancelamentos e schema 82

A Beta 7 foi publicada como prerelease. Ao atualizar para seu código, o ContaBase aplica as migrations `79`, `80`, `81` e `82` (schema `77` → `82`). A migration `78` experimental não integra o startup. Faça backup antes da atualização.

As migrations são transacionais e não reconstroem as tabelas financeiras. A `82` substitui dois triggers de UPDATE para devolver operabilidade a contas e lotes após um estorno individual; recibos, compras e transferências históricas continuam protegidos. Bancos já em `80` ou `81` são suportados, inclusive com recibos existentes e saldos movimentados.

Instalações existentes preservam `CONTABASE_FEATURE_INVESTMENTS`. Instalações novas pelos instaladores oficiais nascem com a flag `false`; a ausência da variável em instalação atualizada mantém o comportamento legado habilitado. Veja [Investimentos](investimentos.md).

A política de recuperação automática continua restrita à Beta 6. Não há downgrade automático para a Beta 7 nem restore automático de banco; preserve banco, WAL, uploads, configuração, chaves e backups e use procedimento manual autorizado. A recuperação automática da linha Beta 7 exige revisão específica em versão futura.

P2 conhecido: a movimentação de recorrências legadas pode ignorar ocorrências em faturas PAID. Não corrigido nesta versão.

## Beta 6 publicada: Investimentos, credenciais e recuperação

A Beta 6 foi publicada. Ao atualizar para seu código, a ausência de `CONTABASE_FEATURE_INVESTMENTS` disponibiliza o módulo sem alteração manual de configuração. Uma desativação explícita, inclusive valor vazio, permanece respeitada; não há reconciliação ou débito retroativo. As operações financeiras controladas continuam opt-in. Veja [Investimentos](investimentos.md) para limites e compatibilidade.

No Compose fornecido pelo projeto, `.env.docker` passa a ser a fonte dessa flag, sem repasse implícito do shell. Se você desabilitava Investimentos apenas prefixando o comando Docker com a variável, registre a desativação no arquivo antes de atualizar. Configurações explícitas no override local continuam prevalecendo.

Antes de atualizar, preserve `AUTH_ENCRYPTION_KEY` e `SECURITY_MASTER_KEY`. A partir desta versão, `SECURITY_MASTER_KEY` é exigida explicitamente para gravar senha SMTP e não há fallback público em desenvolvimento. A atualização não gera nem troca essa chave. Se houver senha SMTP cifrada, uma chave ausente ou incorreta impede o envio de alertas, mas preserva o ciphertext e não bloqueia o restante da aplicação. Senhas cifradas pela chave pública antiga não são lidas automaticamente; configure uma chave forte persistente e informe novamente a senha em `Administração`, ou remova a credencial de forma explícita.

Contas criadas pela identidade de seed histórica deixam de ser reativadas no startup. Uma credencial pública, vazia ou inválida é inutilizada, e a conta passa a exigir recuperação administrativa em `Administração > Usuários` ou pelo Admin CLI. A redefinição gera senha temporária, exige troca, revoga sessões e desafios anteriores e preserva os fatores 2FA cadastrados. Se o 2FA também foi perdido, desative-o em ação separada. Não edite a tabela `users` diretamente.

O retorno automático de bundle ou imagem só é permitido para uma cópia verificada da própria Beta 6, com prova administrativa local, conteúdo exato, emitida após health válido, e somente antes de qualquer tentativa de startup da candidata. Não há downgrade automático para a Beta 5. Depois de uma tentativa de startup, o schema pode ter avançado: mantenha o serviço parado, preserve banco, WAL, uploads, configuração, chaves e backups, e repare com um artefato verificado compatível com o schema atual. O fallback de código não restaura banco, configuração nem chaves; restore ou reversão de schema exige plano e autorização próprios.

O upgrade operacional `v1.0.0-beta.5` → `v1.0.0-beta.6` pelo instalador público ainda não foi comprovado integralmente. A validação funcional em Docker DEV não substitui o ensaio de atualização em instalação real.

## Recorrências versionadas (a partir da v0.1.0-beta.7)

A atualização para a v0.1.0-beta.7 ou superior aplica automaticamente as migrations `50` e `51`:

- cria a tabela `recurring_series` e faz o backfill de `series_id` nas regras e nos lançamentos recorrentes antigos;
- cria a tabela de estado `recurring_materialization_state`, usada pela projeção automática das recorrências.

Esse processo é **aditivo e seguro**: não altera saldos, não altera faturas existentes e não apaga dados. Após a atualização, o ContaBase mantém automaticamente as ocorrências futuras das recorrências em uma janela de aproximadamente 15 meses.

Como em qualquer atualização, faça backup antes.

## Beta.9 — updater portátil e migrations de datas, reservas, faturas e recorrências

Ao atualizar da `v0.1.0-beta.7` para a `v0.1.0-beta.9`, o ContaBase aplica as
migrations `52` a `59`:

- colunas de data civil e proteção da cadeia de versões das recorrências;
- exclusões unitárias persistentes e proteção contra resultados recorrentes contraditórios;
- identidade de aportes automáticos, ciclo de vida de contribuições e referências mensais de planejamento de reservas;
- identidade e proveniência de reversões de pagamento de fatura.

O upgrade preserva os dados existentes, mas pode interromper com diagnóstico se
encontrar uma cadeia recorrente, uma ocorrência terminal ou uma identidade de
pagamento em conflito. Não force a atualização: resolva o conflito informado e
tente novamente. Faça backup WAL-consistente antes de atualizar e, se precisar
voltar os dados ao estado anterior, use o restore explícito do pacote pré-update.

Depois de registrar uma exclusão unitária de recorrência, não faça downgrade
para um binário que não reconheça esse registro persistente: ele pode recriar a
ocorrência removida intencionalmente.

Instalações que chegaram à `v0.1.0-beta.8` já possuem essas migrations. Para
elas, a Beta.9 corrige a portabilidade do backup/restore canônico em GNU/Linux
e BusyBox e reforça a atualização para validar versão, proveniência e identidade
do runtime antes de promover o estado instalado. Não use downgrade como forma
de recuperação; em caso de falha, preserve o pacote pré-update e siga o fluxo
de restore explícito.

## Leia também

- [Instalação rápida](instalacao.md)
- [Instalação em LXC/VPS](instalacao-lxc-vps.md)
- [Instalação com Docker](instalacao-docker.md)
- [Configuração](configuracao.md)
- [Backup e restauração](backup-restauracao.md)
- [Solução de problemas](solucao-de-problemas.md)
