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

## Backup rapido

### Banco SQLite

```bash
sqlite3 /var/lib/contabase/contabase.db ".backup '/var/lib/contabase/backups/backup-$(date +%Y%m%d-%H%M%S).db'"
```

### Uploads

```bash
sudo tar -czf /var/lib/contabase/backups/uploads-$(date +%Y%m%d-%H%M%S).tar.gz -C /var/lib/contabase uploads
```

### Script publico

```bash
sudo DATA_DIR=/var/lib/contabase \
  DB_FILE=/var/lib/contabase/contabase.db \
  UPLOADS_DIR=/var/lib/contabase/uploads \
  ./scripts/ops/backup.sh /var/lib/contabase/backups/manual
```

## Restore rapido

1. Pare o servico.
2. Restaure o banco.
3. Restaure os uploads.
4. Ajuste permissões.
5. Reinicie o servico.

```bash
sudo systemctl stop contabase
sudo cp /caminho/do/backup/contabase.db /var/lib/contabase/contabase.db
sudo rm -f /var/lib/contabase/contabase.db-wal /var/lib/contabase/contabase.db-shm
sudo rm -rf /var/lib/contabase/uploads
sudo tar -xzf /caminho/do/backup/uploads.tar.gz -C /var/lib/contabase
sudo chown -R contabase:contabase /var/lib/contabase
sudo systemctl start contabase
```

## Restore com script

```bash
sudo DATA_DIR=/var/lib/contabase \
  DB_FILE=/var/lib/contabase/contabase.db \
  UPLOADS_DIR=/var/lib/contabase/uploads \
  CONFIRM_APP_STOPPED=yes \
  ./scripts/ops/restore.sh /var/lib/contabase/backups/manual/contabase-backup-YYYYMMDD-HHMMSS
```

## Boas praticas

- Nao copie o `.db` enquanto o servico estiver escrevendo sem usar `.backup`.
- Teste o restore em um ambiente de teste.
- Preserve o arquivo `/etc/contabase/contabase.env` junto com o backup.
- Se o 2FA mudar de chave, mantenha `AUTH_ENCRYPTION_KEY`.

## Veja tambem

- [Instalacao](../instalacao-lxc-vps.md)
- [Atualizacao](../atualizacao.md)
- [systemd](systemd.md)
