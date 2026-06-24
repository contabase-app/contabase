package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/contabase-app/contabase/internal/admincli"
	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/paths"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]
	switch command {
	case "reset-password":
		runResetPassword()
	case "users":
		runUsersCommand()
	case "workspaces":
		runWorkspacesCommand()
	case "repair-orphan-cards":
		runRepairOrphanCards()
	case "category-reseed":
		runCategoryReseed()
	default:
		fmt.Printf("Comando desconhecido: %s\n", command)
		printUsage()
		os.Exit(1)
	}
}

func runResetPassword() {
	runResetPasswordWithArgs("reset-password", os.Args[2:])
}

func runResetPasswordWithArgs(commandName string, args []string) {
	resetCmd := flag.NewFlagSet(commandName, flag.ExitOnError)
	email := resetCmd.String("email", "", "E-mail do usuário")

	if err := resetCmd.Parse(args); err != nil {
		resetCmd.Usage()
		os.Exit(1)
	}

	if *email == "" {
		fmt.Println("Erro: A flag --email é obrigatória.")
		resetCmd.Usage()
		os.Exit(1)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = paths.DefaultDatabaseURL()
	}

	db, err := database.Open(dbURL)
	if err != nil {
		log.Fatalf("Erro ao conectar ao banco de dados: %v", err)
	}
	defer db.Close()

	result, err := admincli.ResetAdminPassword(db, *email)
	if err != nil {
		log.Fatalf("Erro ao redefinir senha: %v", err)
	}

	fmt.Printf("Senha temporária gerada para %s.\n", result.Email)
	fmt.Printf("Senha temporária: %s\n", result.TemporaryPassword)
	fmt.Printf("Expira em: %s\n", time.Unix(result.ExpiresAt, 0).Format(time.RFC3339))
	fmt.Println("Copie agora. Esta senha não será exibida novamente.")
	fmt.Println("O usuário será obrigado a trocar a senha no próximo login.")
}

func printUsage() {
	fmt.Println("Uso: admin <comando> [opções]")
	fmt.Println("\nComandos disponíveis:")
	fmt.Println("  reset-password        Gera senha temporária de um usuário localmente")
	fmt.Println("  users disable-2fa     Desativa 2FA de um usuário localmente")
	fmt.Println("  users lockouts        Lista ou limpa bloqueios persistentes de autenticação")
	fmt.Println("  users reset-password  Gera senha temporária de um usuário localmente")
	fmt.Println("  workspaces list       Lista workspaces disponíveis (somente leitura)")
	fmt.Println("  repair-orphan-cards   Diagnostica e repara cartões de crédito sem configuração")
	fmt.Println("  category-reseed       Analisa e aplica reseed de categorias canônicas por workspace")
	fmt.Println("\nExemplos:")
	fmt.Println("  admin reset-password --email admin@example.com")
	fmt.Println("  admin users disable-2fa --email admin@example.com")
	fmt.Println("  admin users lockouts list")
	fmt.Println("  admin users lockouts unlock --email admin@example.com")
	fmt.Println("  admin users reset-password --email user@example.com")
	fmt.Println("  admin workspaces list")
	fmt.Println("  admin repair-orphan-cards --dry-run")
	fmt.Println("  admin repair-orphan-cards")
	fmt.Println("  admin category-reseed --workspace-id <id>")
	fmt.Println("  admin category-reseed --workspace-id <id> --apply --confirm \"RESEED CATEGORIES\"")
}

func runUsersCommand() {
	if len(os.Args) < 3 {
		printUsersUsage()
		os.Exit(1)
	}
	switch os.Args[2] {
	case "disable-2fa":
		runUsersDisable2FA()
	case "lockouts":
		runUsersLockouts()
	case "reset-password":
		runResetPasswordWithArgs("users reset-password", os.Args[3:])
	default:
		fmt.Printf("Subcomando desconhecido de users: %s\n", os.Args[2])
		printUsersUsage()
		os.Exit(1)
	}
}

func printUsersUsage() {
	fmt.Println("Uso: admin users <subcomando> [opções]")
	fmt.Println("\nSubcomandos disponíveis:")
	fmt.Println("  disable-2fa      Desativa 2FA de um usuário por e-mail")
	fmt.Println("  lockouts         Lista ou limpa bloqueios persistentes de autenticação")
	fmt.Println("  reset-password   Gera senha temporária de um usuário por e-mail")
	fmt.Println("\nExemplo:")
	fmt.Println("  admin users disable-2fa --email admin@example.com")
	fmt.Println("  admin users lockouts list")
	fmt.Println("  admin users lockouts unlock --email admin@example.com")
	fmt.Println("  admin users reset-password --email user@example.com")
}

func runWorkspacesCommand() {
	if len(os.Args) < 3 {
		printWorkspacesUsage()
		os.Exit(1)
	}
	switch os.Args[2] {
	case "list":
		runWorkspacesList()
	default:
		fmt.Printf("Subcomando desconhecido de workspaces: %s\n", os.Args[2])
		printWorkspacesUsage()
		os.Exit(1)
	}
}

func printWorkspacesUsage() {
	fmt.Println("Uso: admin workspaces <subcomando> [opções]")
	fmt.Println("\nSubcomandos disponíveis:")
	fmt.Println("  list    Lista todos os workspaces (somente leitura)")
	fmt.Println("\nExemplo:")
	fmt.Println("  admin workspaces list")
}

func runWorkspacesList() {
	cmd := flag.NewFlagSet("workspaces list", flag.ExitOnError)
	help := cmd.Bool("help", false, "Exibe esta ajuda")
	helpShort := cmd.Bool("h", false, "Exibe esta ajuda (atalho)")
	if err := cmd.Parse(os.Args[3:]); err != nil {
		cmd.Usage()
		os.Exit(1)
	}
	if *help || *helpShort {
		fmt.Println("Uso: admin workspaces list")
		fmt.Println()
		fmt.Println("Lista todos os workspaces disponíveis no banco de dados.")
		fmt.Println("O campo ID pode ser usado com --workspace-id em outros comandos como category-reseed.")
		fmt.Println()
		cmd.PrintDefaults()
		return
	}

	db, err := openAdminDB()
	if err != nil {
		log.Fatalf("Erro ao conectar ao banco de dados: %v", err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT id, name, type, COALESCE(created_at, 0) FROM workspaces ORDER BY name ASC`)
	if err != nil {
		log.Fatalf("Erro ao listar workspaces: %v", err)
	}
	defer rows.Close()

	type workspaceRow struct {
		ID        string
		Name      string
		Type      string
		CreatedAt int64
	}

	var workspaces []workspaceRow
	for rows.Next() {
		var ws workspaceRow
		if err := rows.Scan(&ws.ID, &ws.Name, &ws.Type, &ws.CreatedAt); err != nil {
			log.Fatalf("Erro ao ler workspace: %v", err)
		}
		workspaces = append(workspaces, ws)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("Erro ao percorrer workspaces: %v", err)
	}

	if len(workspaces) == 0 {
		fmt.Println("Nenhum workspace encontrado.")
		return
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTYPE\tNAME\tCREATED_AT")
	for _, ws := range workspaces {
		createdAt := "-"
		if ws.CreatedAt > 0 {
			createdAt = time.Unix(ws.CreatedAt, 0).Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", ws.ID, ws.Type, ws.Name, createdAt)
	}
	if err := tw.Flush(); err != nil {
		log.Fatalf("Erro ao escrever saída: %v", err)
	}
}

func runUsersLockouts() {
	if len(os.Args) < 4 {
		printUsersLockoutsUsage()
		os.Exit(1)
	}
	switch os.Args[3] {
	case "list":
		runUsersLockoutsList()
	case "unlock":
		runUsersLockoutsUnlock()
	case "clear-expired":
		runUsersLockoutsClearExpired()
	default:
		fmt.Printf("Subcomando desconhecido de users lockouts: %s\n", os.Args[3])
		printUsersLockoutsUsage()
		os.Exit(1)
	}
}

func printUsersLockoutsUsage() {
	fmt.Println("Uso: admin users lockouts <subcomando> [opções]")
	fmt.Println("\nSubcomandos disponíveis:")
	fmt.Println("  list            Lista bloqueios ativos de senha/2FA")
	fmt.Println("  unlock          Remove bloqueio persistente por --email ou --user-id")
	fmt.Println("  clear-expired   Remove bloqueios expirados")
	fmt.Println("\nExemplos:")
	fmt.Println("  admin users lockouts list")
	fmt.Println("  admin users lockouts list --all")
	fmt.Println("  admin users lockouts unlock --email admin@example.com")
	fmt.Println("  admin users lockouts unlock --user-id <id>")
	fmt.Println("  admin users lockouts clear-expired")
	fmt.Println("\nObservação: rate limit por IP é em memória e não é persistido em auth_lockouts.")
}

func runUsersLockoutsList() {
	cmd := flag.NewFlagSet("users lockouts list", flag.ExitOnError)
	all := cmd.Bool("all", false, "Inclui registros expirados/inativos")
	help := cmd.Bool("help", false, "Exibe esta ajuda")
	helpShort := cmd.Bool("h", false, "Exibe esta ajuda (atalho)")
	if err := cmd.Parse(os.Args[4:]); err != nil {
		cmd.Usage()
		os.Exit(1)
	}
	if *help || *helpShort {
		fmt.Println("Uso: admin users lockouts list [--all]")
		fmt.Println()
		cmd.PrintDefaults()
		return
	}

	db, err := openAdminDB()
	if err != nil {
		log.Fatalf("Erro ao conectar ao banco de dados: %v", err)
	}
	defer db.Close()

	lockouts, err := admincli.ListAuthLockouts(db, *all, time.Now())
	if err != nil {
		log.Fatalf("Erro ao listar bloqueios: %v", err)
	}
	if len(lockouts) == 0 {
		if *all {
			fmt.Println("Nenhum registro de bloqueio encontrado.")
		} else {
			fmt.Println("Nenhum bloqueio ativo encontrado.")
		}
		return
	}

	nowUnix := time.Now().Unix()
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "EMAIL\tUSER_ID\tMOTIVO\tSENHA\t2FA\tEXPIRA_EM\tSTATUS")
	for _, item := range lockouts {
		email := item.Email
		if strings.TrimSpace(email) == "" {
			email = "-"
		}
		status := "expirado"
		if item.LockedUntil > nowUnix {
			status = "ativo"
		}
		expiresAt := "-"
		if item.LockedUntil > 0 {
			expiresAt = time.Unix(item.LockedUntil, 0).Format(time.RFC3339)
		}
		reason := strings.TrimSpace(item.LockReason)
		if reason == "" {
			reason = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\t%s\n", email, item.UserID, reason, item.FailedPasswordCount, item.Failed2FACount, expiresAt, status)
	}
	if err := tw.Flush(); err != nil {
		log.Fatalf("Erro ao escrever saída: %v", err)
	}
}

func runUsersLockoutsUnlock() {
	cmd := flag.NewFlagSet("users lockouts unlock", flag.ExitOnError)
	email := cmd.String("email", "", "E-mail do usuário")
	userID := cmd.String("user-id", "", "ID do usuário")
	help := cmd.Bool("help", false, "Exibe esta ajuda")
	helpShort := cmd.Bool("h", false, "Exibe esta ajuda (atalho)")
	if err := cmd.Parse(os.Args[4:]); err != nil {
		cmd.Usage()
		os.Exit(1)
	}
	if *help || *helpShort {
		fmt.Println("Uso: admin users lockouts unlock --email admin@example.com")
		fmt.Println("   ou: admin users lockouts unlock --user-id <id>")
		fmt.Println()
		cmd.PrintDefaults()
		return
	}
	if strings.TrimSpace(*email) == "" && strings.TrimSpace(*userID) == "" {
		fmt.Println("Erro: informe --email ou --user-id.")
		cmd.Usage()
		os.Exit(1)
	}
	if strings.TrimSpace(*email) != "" && strings.TrimSpace(*userID) != "" {
		fmt.Println("Erro: use apenas uma opção: --email ou --user-id.")
		cmd.Usage()
		os.Exit(1)
	}

	db, err := openAdminDB()
	if err != nil {
		log.Fatalf("Erro ao conectar ao banco de dados: %v", err)
	}
	defer db.Close()

	var result admincli.UnlockAuthLockoutResult
	if strings.TrimSpace(*email) != "" {
		result, err = admincli.UnlockAuthLockoutByEmail(db, *email)
	} else {
		result, err = admincli.UnlockAuthLockoutByUserID(db, *userID)
	}
	if err != nil {
		log.Fatalf("Erro ao desbloquear usuário: %v", err)
	}
	if result.Removed {
		fmt.Printf("Bloqueio removido para %s (id=%s).\n", result.Email, result.UserID)
		return
	}
	fmt.Printf("Nenhum bloqueio persistente ativo ou pendente para %s (id=%s).\n", result.Email, result.UserID)
}

func runUsersLockoutsClearExpired() {
	cmd := flag.NewFlagSet("users lockouts clear-expired", flag.ExitOnError)
	help := cmd.Bool("help", false, "Exibe esta ajuda")
	helpShort := cmd.Bool("h", false, "Exibe esta ajuda (atalho)")
	if err := cmd.Parse(os.Args[4:]); err != nil {
		cmd.Usage()
		os.Exit(1)
	}
	if *help || *helpShort {
		fmt.Println("Uso: admin users lockouts clear-expired")
		fmt.Println()
		cmd.PrintDefaults()
		return
	}

	db, err := openAdminDB()
	if err != nil {
		log.Fatalf("Erro ao conectar ao banco de dados: %v", err)
	}
	defer db.Close()

	result, err := admincli.ClearExpiredAuthLockouts(db, time.Now())
	if err != nil {
		log.Fatalf("Erro ao limpar bloqueios expirados: %v", err)
	}
	fmt.Printf("Bloqueios expirados removidos: %d\n", result.Removed)
}

func runUsersDisable2FA() {
	cmd := flag.NewFlagSet("users disable-2fa", flag.ExitOnError)
	email := cmd.String("email", "", "E-mail do usuário")
	help := cmd.Bool("help", false, "Exibe esta ajuda")
	helpShort := cmd.Bool("h", false, "Exibe esta ajuda (atalho)")
	if err := cmd.Parse(os.Args[3:]); err != nil {
		cmd.Usage()
		os.Exit(1)
	}
	if *help || *helpShort {
		fmt.Println("Uso: admin users disable-2fa --email admin@example.com")
		fmt.Println()
		fmt.Println("Desativa o 2FA de um usuário localmente, sem alterar senha ou permissões.")
		fmt.Println("Revoga sessões e desafios 2FA pendentes do usuário.")
		fmt.Println()
		cmd.PrintDefaults()
		return
	}
	if *email == "" {
		fmt.Println("Erro: A flag --email é obrigatória.")
		cmd.Usage()
		os.Exit(1)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = paths.DefaultDatabaseURL()
	}
	db, err := database.Open(dbURL)
	if err != nil {
		log.Fatalf("Erro ao conectar ao banco de dados: %v", err)
	}
	defer db.Close()

	result, err := admincli.DisableUser2FA(db, *email)
	if err != nil {
		log.Fatalf("Erro ao desativar 2FA: %v", err)
	}
	if result.WasEnabled {
		fmt.Printf("2FA desativado e sessões revogadas para %s.\n", result.Email)
	} else {
		fmt.Printf("2FA já estava desativado; sessões revogadas para %s.\n", result.Email)
	}
}

func openAdminDB() (*sql.DB, error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = paths.DefaultDatabaseURL()
	}
	return database.Open(dbURL)
}

func runRepairOrphanCards() {
	repairCmd := flag.NewFlagSet("repair-orphan-cards", flag.ExitOnError)
	dryRun := repairCmd.Bool("dry-run", false, "Apenas diagnosticar, sem aplicar reparos")
	help := repairCmd.Bool("help", false, "Exibe esta ajuda")
	helpShort := repairCmd.Bool("h", false, "Exibe esta ajuda (atalho)")

	if err := repairCmd.Parse(os.Args[2:]); err != nil {
		repairCmd.Usage()
		os.Exit(1)
	}

	if *help || *helpShort {
		fmt.Println("Uso: admin repair-orphan-cards [opções]")
		fmt.Println()
		fmt.Println("Diagnostica e repara cartões de crédito sem configuração interna.")
		fmt.Println("Cartões órfãos são contas do tipo CREDIT_CARD que não possuem")
		fmt.Println("linha correspondente na tabela credit_cards.")
		fmt.Println()
		repairCmd.PrintDefaults()
		fmt.Println()
		fmt.Println("Recomendação:")
		fmt.Println("  1. Rode com --dry-run primeiro para diagnosticar")
		fmt.Println("  2. Faça backup do banco (scripts/ops/backup.sh)")
		fmt.Println("  3. Rode sem --dry-run para aplicar o reparo")
		return
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = paths.DefaultDatabaseURL()
	}

	db, err := database.Open(dbURL)
	if err != nil {
		log.Fatalf("Erro ao conectar ao banco de dados: %v", err)
	}
	defer db.Close()

	result, err := admincli.RepairOrphanCreditCards(db, *dryRun)
	if err != nil {
		log.Fatalf("Erro ao reparar cartões órfãos: %v", err)
	}

	if *dryRun {
		fmt.Println("=== DIAGNÓSTICO (dry-run) ===")
	} else {
		fmt.Println("=== REPARO DE CARTÕES ÓRFÃOS ===")
		fmt.Println("ATENÇÃO: Faça backup do banco de dados antes de prosseguir com o reparo.")
	}

	fmt.Printf("Cartões diagnosticados: %d\n", result.Diagnosed)

	if result.Diagnosed == 0 {
		fmt.Println("Nenhum cartão órfão encontrado.")
		return
	}

	if *dryRun {
		fmt.Println("\nCartões que seriam reparados:")
	} else {
		fmt.Println("\nCartões reparados:")
	}

	for _, detail := range result.Details {
		fmt.Println(detail)
	}

	if !*dryRun {
		fmt.Printf("\nTotal reparado: %d cartão(s)\n", result.Repaired)
	}
}

func runCategoryReseed() {
	cmd := flag.NewFlagSet("category-reseed", flag.ExitOnError)
	workspaceID := cmd.String("workspace-id", "", "ID do workspace (obrigatório)")
	dryRun := cmd.Bool("dry-run", false, "Apenas diagnosticar, sem aplicar alterações (padrão)")
	apply := cmd.Bool("apply", false, "Aplicar reseed de categorias")
	confirm := cmd.String("confirm", "", "Confirmação textual obrigatória para apply: \"RESEED CATEGORIES\"")
	help := cmd.Bool("help", false, "Exibe esta ajuda")
	helpShort := cmd.Bool("h", false, "Exibe esta ajuda (atalho)")

	if err := cmd.Parse(os.Args[2:]); err != nil {
		cmd.Usage()
		os.Exit(1)
	}

	if *help || *helpShort {
		printCategoryReseedHelp(cmd)
		return
	}

	if strings.TrimSpace(*workspaceID) == "" {
		fmt.Println("Erro: A flag --workspace-id é obrigatória.")
		printCategoryReseedHelp(cmd)
		os.Exit(1)
	}

	if *apply && *dryRun {
		fmt.Println("Erro: --apply e --dry-run são mutuamente exclusivos.")
		printCategoryReseedHelp(cmd)
		os.Exit(1)
	}

	if *apply {
		if strings.TrimSpace(*confirm) != "RESEED CATEGORIES" {
			fmt.Println("Erro: --apply requer --confirm \"RESEED CATEGORIES\"")
			fmt.Printf("Confirmação recebida: %q\n", *confirm)
			os.Exit(1)
		}
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = paths.DefaultDatabaseURL()
	}

	db, err := database.Open(dbURL)
	if err != nil {
		log.Fatalf("Erro ao conectar ao banco de dados: %v", err)
	}
	defer db.Close()

	var exists bool
	if err := db.QueryRow(`SELECT 1 FROM workspaces WHERE id = ?`, *workspaceID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			log.Fatalf("Erro: workspace %q não encontrado.", *workspaceID)
		}
		log.Fatalf("Erro ao verificar workspace: %v", err)
	}

	if *apply {
		fmt.Println("ATENÇÃO: Faça backup do banco de dados antes de prosseguir (scripts/ops/backup.sh).")
		report, err := database.ApplyWorkspaceCategoryReseed(db, *workspaceID)
		if err != nil {
			log.Fatalf("Erro ao aplicar reseed: %v", err)
		}
		printCategoryReseedReport(report)
	} else {
		report, err := database.DryRunWorkspaceCategoryReseed(db, *workspaceID)
		if err != nil {
			log.Fatalf("Erro ao analisar reseed: %v", err)
		}
		printCategoryReseedReport(report)
	}
}

func printCategoryReseedHelp(cmd *flag.FlagSet) {
	fmt.Println("Uso: admin category-reseed [opções]")
	fmt.Println()
	fmt.Println("Analisa e aplica reseed de categorias canônicas por workspace.")
	fmt.Println()
	fmt.Println("ATENÇÃO:")
	fmt.Println("  1. Dry-run é o comportamento padrão (não altera dados).")
	fmt.Println("  2. Faça backup antes de aplicar (scripts/ops/backup.sh).")
	fmt.Println("  3. Categorias com lançamentos, recorrências, limites ou caixinhas são preservadas.")
	fmt.Println("  4. Categorias filhas com dependências preservam seus pais automaticamente.")
	fmt.Println()
	cmd.PrintDefaults()
	fmt.Println()
	fmt.Println("Exemplos:")
	fmt.Println("  admin category-reseed --workspace-id <id>")
	fmt.Println("  admin category-reseed --workspace-id <id> --dry-run")
	fmt.Println("  admin category-reseed --workspace-id <id> --apply --confirm \"RESEED CATEGORIES\"")
}

func printCategoryReseedReport(report database.CategoryReseedReport) {
	if report.Applied {
		fmt.Println("=== RESEED DE CATEGORIAS APLICADO ===")
	} else {
		fmt.Println("=== DIAGNÓSTICO DE RESEED (dry-run) ===")
	}
	fmt.Println()

	fmt.Printf("Workspace analisado: %s\n", report.WorkspaceID)
	fmt.Printf("Tipo do workspace:   %s\n", report.WorkspaceType)
	fmt.Printf("Total de categorias antes: %d\n", report.TotalBefore)
	fmt.Println()

	if len(report.RemoveCandidates) > 0 {
		fmt.Printf("Categorias candidatas à remoção (%d):\n", len(report.RemoveCandidates))
		for _, item := range report.RemoveCandidates {
			prefix := "  "
			if item.ParentID != "" {
				prefix = "  ↳ "
			}
			fmt.Printf("  %s%s (tipo=%s, macro=%q, razão=%s)\n", prefix, item.Name, item.Type, item.MacroGroup, item.Reason)
		}
		fmt.Println()
	}

	if len(report.PreservedByUsage) > 0 {
		fmt.Printf("Categorias preservadas por uso (%d):\n", len(report.PreservedByUsage))
		for _, item := range report.PreservedByUsage {
			fmt.Printf("  - %s (tipo=%s, macro=%q, razão=%s)\n", item.Name, item.Type, item.MacroGroup, item.Reason)
		}
		fmt.Println()
	}

	if len(report.PreservedByDependency) > 0 {
		fmt.Printf("Categorias preservadas por dependência (%d):\n", len(report.PreservedByDependency))
		for _, item := range report.PreservedByDependency {
			fmt.Printf("  - %s (tipo=%s, macro=%q, razão=%s)\n", item.Name, item.Type, item.MacroGroup, item.Reason)
		}
		fmt.Println()
	}

	if len(report.Conflicts) > 0 {
		fmt.Printf("Conflitos (%d):\n", len(report.Conflicts))
		for _, item := range report.Conflicts {
			fmt.Printf("  ⚠ %s (tipo=%s, macro=%q, razão=%s)\n", item.Name, item.Type, item.MacroGroup, item.Reason)
		}
		fmt.Println()
	}

	if len(report.CanonicalCreated) > 0 {
		fmt.Printf("Categorias canônicas a criar (%d):\n", len(report.CanonicalCreated))
		for _, item := range report.CanonicalCreated {
			prefix := "  "
			if item.ParentName != "" {
				prefix = "  ↳ "
			}
			fmt.Printf("  %s%s (tipo=%s, macro=%q)\n", prefix, item.Name, item.Type, item.MacroGroup)
		}
		fmt.Println()
	}

	if len(report.CanonicalAlreadyExisted) > 0 {
		fmt.Printf("Categorias canônicas já existentes (%d):\n", len(report.CanonicalAlreadyExisted))
		for _, item := range report.CanonicalAlreadyExisted {
			fmt.Printf("  - %s (tipo=%s, macro=%q)\n", item.Name, item.Type, item.MacroGroup)
		}
		fmt.Println()
	}

	if report.Applied {
		fmt.Printf("Total de categorias depois: %d\n", report.TotalAfter)
	} else {
		totalAfter := report.TotalBefore - len(report.RemoveCandidates) + len(report.CanonicalCreated)
		fmt.Printf("Total estimado de categorias depois: %d\n", totalAfter)
	}
	fmt.Println()

	if !report.Applied {
		fmt.Println("Modo dry-run: nenhuma alteração foi aplicada ao banco de dados.")
		fmt.Println("Para aplicar, use: admin category-reseed --workspace-id", report.WorkspaceID, "--apply --confirm \"RESEED CATEGORIES\"")
	}
}
