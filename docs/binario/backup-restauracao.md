# ContaBase - Backup e Restore via Binario

No modo binario, os dados vivem em `/var/lib/contabase`.
Isso inclui o banco SQLite, os uploads e os backups locais.

## O que precisa ser preservado

- `/var/lib/contabase/contabase.db`
- `/var/lib/contabase/uploads/`
- `/var/lib/contabase/backups/`
- `/etc/contabase/contabase.env`
- `AUTH_ENCRYPTION_KEY`
- `SECURITY_MASTER_KEY`
- `CONTABASE_SETUP_TOKEN`, ate o primeiro setup terminar

## Pacote canonico manual

```bash
sudo DATA_DIR=/var/lib/contabase \
  DB_FILE=/var/lib/contabase/contabase.db \
  UPLOADS_DIR=/var/lib/contabase/uploads \
  ./scripts/ops/backup.sh /var/lib/contabase/backups/manual
```

O script imprime como ultima linha o caminho exato do pacote `backup-*`. Esse
pacote contem `database.sqlite`, `uploads/`, `manifest.txt` e
`checksums.sha256`. Ele nao contem `/etc/contabase/contabase.env` nem segredos;
preserve a configuracao separadamente.

O helper finaliza o pacote somente depois de `integrity_check` e
`foreign_key_check` sem erros. O restore valida manifesto, inventario e
checksums antes de tocar no estado ativo.

## Backup automatico antes de update

Os atualizadores publicos `update-contabase-source.sh` e
`update-contabase-release.sh` param o servico e criam obrigatoriamente esse
mesmo pacote antes da primeira alteracao do checkout, configuracao ou bundle.

- No modo source, o helper e materializado da referencia Git alvo e conferido
  pelo blob Git antes do uso.
- No modo release, o updater da tag alvo baixa os helpers dessa mesma tag e os
  aceita somente quando seus SHA-256 coincidem com os digests canonicos
  vinculados ao proprio updater; artifact, estrutura e `VERSION` tambem sao
  validados antes de qualquer parada.
- Banco e uploads ausentes significam estado inexistente e permitem continuar.
- Uploads sem banco, banco invalido/ambiguo/ilegivel ou falha do helper bloqueiam
  o update.

O atualizador informa o caminho exato do pacote, o `backup_id` e a versao
anterior registrada no manifesto.

## Restore com script

```bash
sudo DATA_DIR=/var/lib/contabase \
  DB_FILE=/var/lib/contabase/contabase.db \
  UPLOADS_DIR=/var/lib/contabase/uploads \
  CONFIRM_APP_STOPPED=yes \
  ./scripts/ops/restore.sh /caminho/exato/do/pacote/backup-YYYYMMDDTHHMMSSZ-IDENTIFICADOR
```

Reinicie o servico somente depois do restore terminar com sucesso:

```bash
sudo systemctl start contabase
```

O restore substitui banco e uploads e descarta alteracoes posteriores ao backup.
Use exatamente o caminho apresentado pelo atualizador, nunca uma busca por
"backup mais recente".

## Falha durante update

Rollback automatico do bundle/runtime antigo so e permitido antes de o runtime
novo iniciar. Depois da tentativa de inicio, o banco pode ter sido migrado. O
atualizador entao para o servico, preserva o bundle que falhou, nao reativa o
bundle antigo e imprime o comando de restore com o pacote pre-update.

O operador deve diagnosticar e escolher explicitamente entre corrigir o runtime
novo ou restaurar o pacote. Nunca rode o runtime antigo sobre o banco
possivelmente migrado por tentativa automatica.

## Retorno da Beta 8 para a Beta 7

Nao existe downgrade in-place suportado da Beta 8 para a Beta 7 depois que o
banco foi aberto pela Beta 8.

1. pare a Beta 8;
2. preserve o estado atual separadamente para diagnostico;
3. selecione pelo caminho/ID o pacote pre-update imutavel;
4. execute o restore oficial;
5. confirme integridade e foreign keys;
6. ative exatamente o runtime Beta 7;
7. valide healthcheck e smoke funcional;
8. reabra o acesso.

O retorno descarta deliberadamente alteracoes posteriores ao backup. O pacote
deve permanecer imutavel e fora do diretorio de bundle substituido.

## Boas praticas

- Nao copie o `.db` enquanto o servico estiver escrevendo sem usar `.backup`.
- Teste o restore em um ambiente de teste.
- Preserve o arquivo `/etc/contabase/contabase.env` junto com o backup.
- Se o 2FA mudar de chave, mantenha `AUTH_ENCRYPTION_KEY`.

## Veja tambem

- [Instalacao](../instalacao-lxc-vps.md)
- [Atualizacao](../atualizacao.md)
- [systemd](systemd.md)
