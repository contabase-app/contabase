# Política de Segurança do ContaBase

Esta política descreve as práticas, os recursos reais e as limitações de segurança da **Beta pública controlada** do ContaBase. Leia o modelo de ameaças e as responsabilidades operacionais antes de adotar o sistema.

---

## 🛡️ 1. Escopo de Segurança da Beta controlada

O ContaBase está em fase **Beta pública controlada, self-hosted**. A aplicação possui controles de segurança, mas não é infalível e ainda não passou por auditoria externa completa. Não prometemos segurança absoluta nem conformidade legal/LGPD automática. A adoção exige cautela, backup, restore testado e planejamento de contingência.

> **Status da Beta controlada:**
> A rodada de Segurança, Privacidade e LGPD tratou os bloqueantes técnicos mínimos para uma exposição pública controlada: proteção de setup/bootstrap/restore por token local, runtime Docker non-root, headers anti-cache em rotas autenticadas, mitigação de CSV Injection nas exportações CSV disponíveis e account lockout temporário persistente para login/2FA. Isso não torna a aplicação pronta automaticamente para uso de missão crítica sem supervisão técnica: o operador ainda deve seguir o checklist de deploy, HTTPS, proxy, backups, retenção e resposta a incidentes em `https://github.com/contabase-app/contabase/blob/beta/docs/seguranca-deploy.md`.

---

## 🏗️ 2. Responsabilidade do Operador (Self-Hosted)

O ContaBase é projetado para ser **auto-hospedado (self-hosted)**. A segurança dos seus dados é de responsabilidade conjunta, sendo a parte majoritária do **Operador da Instância**. O Operador é o único responsável por:

- Proteger o servidor físico ou virtual onde o Docker (ou binário) roda.
- Configurar e proteger a porta exposta com um **Proxy Reverso seguro (Nginx, Caddy, etc.) e certificado HTTPS válido**.
- Garantir a segurança das chaves geradas e armazenadas nos arquivos `.env` ou `.env.docker`.
- Gerenciar rotinas de backup da máquina hospedeira.

O ContaBase **não** possui telemetria embarcada e não envia dados a terceiros.

---

## 🔒 3. Recursos Implementados

Os seguintes recursos estão presentes e operacionais no código atual:

**Arquitetura e Dados:**
- Aplicativo self-hosted e armazenamento SQLite isolado.
- Exclusão técnica pública restrita de arquivos `.env`, `data/`, backups e keys no ambiente Github via scripts de exportação validados.
- `SECURITY_MASTER_KEY` explícita, com exatamente 32 bytes, para cifrar a senha SMTP. Não há fallback público; o ciphertext existente é preservado quando a chave está ausente ou incorreta.

**Autenticação e Sessões:**
- Senhas armazenadas via algoritmo `bcrypt`.
- Credenciais da identidade de seed histórica não são reativadas no startup. Hashes correspondentes à senha pública ou inválidos são inutilizados, e a conta exige recuperação administrativa explícita, preservando fatores 2FA e vínculos existentes.
- Cookies seguros: `HttpOnly`, `SameSite=Lax` e `Secure`.
- Sessões de usuário hashadas com SHA-256 no banco de dados; botões ativos de revogação remota ("Sair dos outros dispositivos").
- Proteção CSRF via HMAC (Double-submit mitigation).
- Limitadores de acesso em memória temporária contra requisições agressivas em rotas autenticáveis, com chaves hashadas, limpeza de expirados e teto de 10.000 buckets por processo.
- Bloqueio persistente e temporário (Account Lockout) em banco de dados contra força bruta focada em tentativas repetidas de senha ou 2FA.
- Códigos de recuperação do 2FA operacionais, com formato canônico e uso unico por login bem-sucedido.
- Setup inicial e restore em modo bootstrap protegidos por `CONTABASE_SETUP_TOKEN` forte, configurado localmente pelo operador.

**Prevenção de Abuso:**
- Isolamento rígido financeiro por filtro de Workspaces em nível de handlers.
- Content-Security-Policy efetiva para scripts: somente arquivos do próprio origin são aceitos; JavaScript inline, `eval`, atributos executáveis, URLs `javascript:`, frames e objetos são bloqueados.
- Elementos `<style>` e atributos `style` são bloqueados. Regras estáticas vêm de arquivos do próprio origin; cores livres passam por validação hexadecimal e por uma folha autenticada limitada ao workspace ativo; movimentos e posições calculados usam a Web Animations API sem criar CSS inline. A cobertura de regressão impede que templates ou scripts reintroduzam essas superfícies.
- Headers `Cache-Control: no-store` para respostas autenticadas sensíveis e parciais HTMX.
- Mitigação de CSV Injection nas exportações CSV disponíveis para campos textuais controlados por usuários.

**Atualização e Recuperação:**
- Os fluxos de instalação e atualização só reativam automaticamente um código ou imagem anterior quando há prova administrativa local de conteúdo verificado da própria Beta 6 e nenhuma tentativa de startup da candidata. Não há downgrade automático para a Beta 5.
- O fallback de código não restaura banco, schema, configuração ou chaves. Depois de uma tentativa de startup, o retorno automático é bloqueado e a recuperação exige artefato corrigido compatível com o schema atual, com plano próprio para restore.
- A política inicial de recuperação reconhece somente `v1.0.0-beta.6`; versões futuras exigem revisão específica.

---

## 🚧 4. Recursos Parciais ou Planejados

As funções abaixo estão **parcialmente implementadas** ou planejadas, devendo ser consideradas instáveis ou residuais nesta versão:

- **Autenticação em Dois Fatores (2FA TOTP):** Já ativa no motor interno, com recovery codes operacionais e recuperável via Admin CLI, pendendo polimentos finais de UX em UI externa pública.
- **Rate limiting distribuído:** os buckets da aplicação são locais a cada processo. Instalações com mais de uma réplica devem aplicar limites compartilhados no proxy/edge para que a cota seja global.

---

## 🚨 5. Como Reportar Vulnerabilidades

Apoiamos e incentivamos fortemente a divulgação responsável (*Responsible Disclosure*). Sendo o repositório open-source, adotamos um canal provisório de contato direto confidencial.

Se você encontrou um vetor de ataque perigoso que ultrapasse nossas medidas de contingência ou gere execução arbitrária:

- **NÃO** publique o exploit na lista pública de Issues.
- **NÃO** exiba prints vazando tokens na rede do Github.

**Canal de Comunicação Seguro:**
O canal de e-mail dedicado (**security@contabase.app**) ainda **não é operacional** (a definir). Enquanto ele não estiver ativo, use o canal real já existente: abra um **GitHub Security Advisory privado** ("Report a vulnerability") no repositório público do projeto, que mantém a divulgação confidencial até a correção.
A equipe analisará o impacto, triará as evidências e coordenará com você uma janela razoável de correção antes da publicação do registro oficial da brecha (CVE ou Github Advisory equivalente).
