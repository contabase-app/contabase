# ContaBase - Retencao de Dados e Backups

O operador da instancia e responsavel por guardar, rotacionar e remover dados conforme sua propria politica. O ContaBase nao faz limpeza automatica de backups antigos nem decide por voce o que deve ser preservado.

## Onde os dados ficam

- Docker: a aplicacao usa o volume montado em `/app/data`.
- Binario/systemd: os dados persistentes ficam em `/var/lib/contabase`.
- Uploads do binario ficam em `/var/lib/contabase/uploads`.
- Backups do binario ficam em `/var/lib/contabase/backups`.

## O que precisa ser protegido

- banco SQLite;
- uploads e anexos;
- arquivo de ambiente;
- chaves de seguranca;
- backups anteriores.

## O que revisar na rotina do operador

- confirme se existe mais de uma copia de backup;
- teste restore em ambiente separado de tempos em tempos;
- apague copias antigas conforme sua politica;
- ajuste rotacao de logs quando eles crescerem demais;
- monitore espaco em disco.

## Regra pratica

Se um arquivo e necessario para reconstruir a instancia, ele precisa entrar na sua estrategia de backup. Isso inclui o banco, os uploads e as variaveis sensiveis do ambiente.
