# Requisitos do Sistema e Diagnóstico

Este documento lista os requisitos mínimos e recomendados para rodar e compilar o ContaBase via Docker, além de oferecer um guia de diagnóstico de infraestrutura e gestão de espaço.

## 1. Requisitos para Executar (Runtime)
Estes são os requisitos caso você utilize a imagem Docker pré-compilada, ou já tenha realizado o build local com sucesso.

- **Processador (CPU):** 1 vCPU (mínimo)
- **Memória (RAM):** 512 MB (mínimo), 1 GB (recomendado)
- **Armazenamento (Disco):** 2 GB livres (mínimo para rodar a stack). Recomendado ter 5 GB+ livres (sem contar com o tamanho do seu banco, backups e uploads ao longo do tempo).
- **Software:** Docker Engine e Docker Compose v2 instalados.

## 2. Requisitos para Build Local via Docker
Ao fazer o build do código fonte na sua própria máquina (ex: via `./scripts/update-contabase-docker.sh` ou `docker compose build`), o Docker precisa baixar e compilar vários componentes (imagem base Go, Alpine, compilador do TailwindCSS, e o módulo puro em Go do SQLite, `modernc.org/sqlite`). Este processo exige **significativamente mais recursos temporários**.

- **Processador (CPU):** 2 vCPUs (mínimo)
- **Memória (RAM):** 2 GB (mínimo), 4 GB (recomendado)
- **Armazenamento (Disco):** 8 GB livres no mínimo (assumindo que o Docker não tenha lixo acumulado).
- **Disco Recomendado:** 15 a 20 GB livres.

> [!WARNING]
> Construir a imagem com cerca de **4 GB ou menos de espaço livre pode falhar** com a mensagem `no space left on device`, pois a imagem do Go, as dependências baixadas e o cache de compilação da biblioteca de banco de dados (modernc.org/sqlite) ocupam muito espaço no `/root/.cache/go-build` do container *builder*.

## 3. Gestão de Banco de Dados e Backups
- O ContaBase utiliza **SQLite**, cujo arquivo principal e logs de transação ficam armazenados na pasta mapeada `data/` local.
- Os **Backups automáticos** e envios do usuário ficam armazenados em `data/backups/` e `data/uploads/`.
- É **altamente recomendado** manter pelo menos 20% do disco livre para evitar que o banco de dados seja corrompido em cenários de exaustão de espaço.

> [!CAUTION]
> **Nunca execute `docker system prune --volumes` ou `docker volume prune` de forma cega**, sem ter certeza absoluta de onde os dados do seu ContaBase estão montados, sob risco de perder seu banco de dados inteiro caso esteja utilizando *named volumes* ao invés de diretórios mapeados (bind mounts).

## 4. Comandos de Diagnóstico Úteis

Caso tenha problemas com performance, recursos ou subida do sistema:

```bash
# Ver espaço livre em disco
df -h

# Ver o uso de disco interno pelo Docker (imagens, cache, volumes)
docker system df

# Limpar todo o cache de build antigo do Docker (Seguro: não afeta containers ativos)
docker builder prune -af

# Limpar imagens antigas pendentes (Seguro: não apaga as ativas)
docker image prune -af

# Checar o status dos serviços do ContaBase
docker compose ps

# Ver os últimos erros no log da aplicação
docker compose logs --tail 100 contabase

# Validar se a API do sistema subiu corretamente (Healthcheck)
curl -i http://localhost:8080/health
```

## 5. Como Resolver o Erro "No space left on device"
Se o comando de deploy/update falhar no meio da compilação acusando falta de espaço:

1. **Causa provável:** O build exauriu a partição do seu servidor armazenando o cache da compilação do SQLite e imagens temporárias.
2. **Como verificar:** Rode `df -h` e veja se a partição raiz (`/`) está em 100%. Rode `docker system df` para ver o peso do "Build Cache".
3. **Limpeza Segura:** Rode `docker builder prune -af` e `docker image prune -af` para expurgar lixos de builds antigos.
4. **Alerta:** Evite comandos agressivos com a flag `--volumes` para não destruir `data/`.
5. **Tentativa final:** Após a liberação, tente rodar a atualização novamente (`./scripts/update-contabase-docker.sh`).
