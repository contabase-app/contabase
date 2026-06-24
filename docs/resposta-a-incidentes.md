# ContaBase - Resposta a Incidentes

Este guia e um checklist pratico para quando houver suspeita de acesso indevido, vazamento de credenciais ou adulteracao de dados.

## 1. Conter

- pare a exposicao publica da instancia;
- suspenda o container ou o servico, se necessario;
- preserve o estado atual antes de reiniciar qualquer coisa;
- nao apague logs para "limpar a situacao".

Comandos comuns:

```bash
docker compose stop contabase
sudo systemctl stop contabase
```

## 2. Preservar evidencias

- copie o banco;
- copie os uploads;
- copie os backups existentes;
- exporte os logs do container ou do systemd;
- salve o arquivo de ambiente atual.

## 3. Rotacionar credenciais

- troque senhas que possam ter sido expostas;
- revogue sessoes comprometidas;
- desative 2FA e gere novamente quando a chave tiver sido perdida no restore;
- troque chaves e tokens sensiveis se houver indicio de acesso ao host.

## 4. Recuperar

- restaure o ultimo backup confiavel;
- suba a instancia de novo;
- valide `/health`;
- teste login e acesso administrativo;
- confirme se os arquivos de upload voltaram ao lugar certo.

## 5. Comunicar

Se houver vazamento real de dados de terceiros, avalie as obrigacoes legais e informe as pessoas afetadas com clareza.

## 6. Reportar falha no software

Se a causa parecer ser um defeito do ContaBase, siga o canal de seguranca do projeto. Nao publique o problema antes da correcao.
