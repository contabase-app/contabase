# ContaBase - systemd

Use `systemd` para iniciar, parar e supervisionar o ContaBase no modo binario.
O arquivo de servico fica em `/etc/systemd/system/contabase.service`.

## Exemplo de unit

```ini
[Unit]
Description=ContaBase - Base Financeira Privada
After=network.target

[Service]
Type=simple
User=contabase
Group=contabase
WorkingDirectory=/opt/contabase
EnvironmentFile=/etc/contabase/contabase.env
ExecStart=/opt/contabase/contabase
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=/var/lib/contabase

[Install]
WantedBy=multi-user.target
```

## Regras importantes

- Alterou `/etc/contabase/contabase.env`? Use `sudo systemctl restart contabase`.
- Alterou `/etc/systemd/system/contabase.service`? Use `sudo systemd-analyze verify`, `sudo systemctl daemon-reload` e depois `sudo systemctl restart contabase`.
- `ProtectSystem=strict` bloqueia escrita fora dos caminhos liberados.
- `ReadWritePaths=/var/lib/contabase` libera apenas os dados persistentes.

## Comandos comuns

```bash
sudo systemctl status contabase --no-pager
sudo systemctl restart contabase
sudo systemctl stop contabase
sudo systemctl start contabase
sudo systemctl daemon-reload
sudo journalctl -u contabase -n 100 --no-pager
curl -i http://127.0.0.1:8080/health
```

## Quando usar cada comando

- `restart`: depois de mudar o env ou o binario.
- `daemon-reload`: depois de mudar a unit.
- `journalctl`: quando o servico falha ou o healthcheck nao responde.
- `curl /health`: para validar se o processo subiu e o banco responde.

## Troubleshooting basico

- Se o servico nao sobe, veja `sudo systemctl status contabase --no-pager -l`.
- Se a porta 8080 nao responde, veja `sudo journalctl -u contabase -n 100 --no-pager -l`.
- Se a configuracao parece ignorada, confirme se voce reiniciou o servico depois de alterar o env.
- Se a unit nao foi aplicada, confirme `daemon-reload`.

## Veja tambem

- [Instalacao](../instalacao-lxc-vps.md)
- [Atualizacao](../atualizacao.md)
- [Backup e Restauracao](../backup-restauracao.md)
