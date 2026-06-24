# Política de Privacidade de Referência (Self-Hosted)

> **Aviso Importante:** O ContaBase é um software auto-hospedado (self-hosted). O mantenedor principal e os desenvolvedores originais do código aberto **não** coletam, processam, hospedam ou acessam nenhum dos seus dados. Esta política serve exclusivamente como um modelo de referência para o **Operador da Instância** (a pessoa ou empresa que provisiona o servidor e hospeda o ContaBase) e **não substitui aconselhamento jurídico especializado**. A total conformidade com a Lei Geral de Proteção de Dados (LGPD) ou equivalentes é de total responsabilidade do Operador.

## 1. O ContaBase é Self-Hosted
Ao utilizar uma instância do ContaBase, todos os dados pessoais e financeiros são armazenados e processados isoladamente na infraestrutura provisionada e gerenciada pelo Operador da Instância. O código-fonte oficial do ContaBase não contém telemetria de negócios.

## 2. O Papel do Operador
Para fins legais (ex: LGPD), o **Operador da Instância** atua como o **Controlador** (e possivelmente **Operador**) dos dados processados através da sua infraestrutura. É sua responsabilidade:
- Proteger o servidor, as chaves e os bancos de dados contra acessos não autorizados.
- Estabelecer as bases legais adequadas (Consentimento, Execução de Contrato, Legítimo Interesse) para o tratamento dos dados da sua organização, clientes ou funcionários.
- Atender solicitações dos titulares dos dados (acesso, retificação, eliminação).
- Comunicar formalmente incidentes de segurança caso ocorram.

## 3. Dados Tratados pelo Sistema
Na sua operação normal, a aplicação requer e armazena os seguintes dados:
- **Dados Pessoais e de Identificação:** Nome, E-mail, Senha (armazenada com hash `bcrypt`), Foto de Perfil, CNPJ/CPF, Endereço e Telefone associados aos perfis e Workspaces.
- **Dados Financeiros:** Lançamentos, Saldos, Faturas, Cartões, Metas, Comprovantes anexados, Contas a Pagar/Receber e Relatórios de fluxo de caixa.
- **Dados Técnicos/Operacionais:** Endereço de IP em logs de auditoria, User-Agent, Dados de Sessão (cookies hashados para manter o login), Segredos de Autenticação de Dois Fatores (TOTP) e histórico de ações críticas.

## 4. Retenção e Exclusão
Os dados persistem no banco de dados SQLite local na máquina do Operador. Atualmente, o software não conta com rotinas de deleção cronológica automática. Os dados permanecem até que o Operador ou o Usuário os excluam via interface. Para mais informações sobre como os backups devem ser geridos, consulte `docs/retencao-de-dados.md`.

## 5. Exportação de Dados
O sistema possibilita que os relatórios e registros financeiros sejam exportados pelos usuários autenticados (ex: via CSV). É dever do Operador estabelecer políticas de uso aceitável para garantir que essas extrações trafeguem de forma segura e restrita.

## 6. Resposta a Incidentes
Em caso de violação de dados ou acessos maliciosos à instância, a responsabilidade primária pela contenção, investigação e notificação à Autoridade Nacional de Proteção de Dados (ANPD) ou aos titulares afetados é do Operador da Instância. Sugerimos a consulta do guia em `docs/resposta-a-incidentes.md`.

Se você acredita ter encontrado uma vulnerabilidade diretamente no código-fonte ou arquitetura base do ContaBase, reporte isso de forma privada (consulte `SECURITY.md`).
