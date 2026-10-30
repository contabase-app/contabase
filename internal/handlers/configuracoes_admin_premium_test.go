package handlers

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// ADM3 — Fundação Premium e interfaces administrativas.
//
// Os testes abaixo travam apenas o contrato visual/estrutural introduzido na
// fase: envelope próprio, hierarquia premium, separação de ações sensíveis,
// preservação dos ganchos funcionais da ADM2 e ausência de estilos inline.

func renderAdminSection(t *testing.T, handler ConfiguracoesHandler, section string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin/"+strings.TrimPrefix(section, "admin-"), nil)
	rr := httptest.NewRecorder()
	handler.HandleConfiguracoesSection(rr, req, section)
	if rr.Code != http.StatusOK {
		t.Fatalf("%s: status = %d, body = %q", section, rr.Code, rr.Body.String())
	}
	return rr.Body.String()
}

func TestAdminPremiumFoundationCoversFourSections(t *testing.T) {
	sections := []string{"admin-users", "admin-workspaces", "admin-backups", "admin-auditoria"}
	for _, section := range sections {
		t.Run(section, func(t *testing.T) {
			handler := newConfiguracoesRenderTestHandler(t)
			body := renderAdminSection(t, handler, section)

			for _, want := range []string{
				`/assets/css/premium.css?v=test`,
				`/assets/css/configuracoes.css?v=test`,
				`/assets/css/admin-premium.css?v=test`,
				`data-ui="premium"`,
				`cb-v2-admin-main`,
				`ui-page-header`,
				`ui-page-title`,
				`ui-page-description`,
				`id="settings-dynamic-payload"`,
			} {
				assertContains(t, body, want)
			}
			for _, forbidden := range []string{`sticky top-14`, `>admin users<`, `>admin workspaces<`, `>admin backups<`, `>admin auditoria<`} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("%s must not keep legacy header %q", section, forbidden)
				}
			}
			if strings.Index(body, "premium.css") > strings.Index(body, "configuracoes.css") {
				t.Fatal("premium.css must load before configuracoes.css")
			}
			if strings.Index(body, "configuracoes.css") > strings.Index(body, "admin-premium.css") {
				t.Fatal("admin-premium.css must load after configuracoes.css")
			}
		})
	}
}

func TestAdminPremiumPreservesFunctionalHooksAndSensitiveActions(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)

	users := renderAdminSection(t, handler, "admin-users")
	for _, want := range []string{
		`data-admin-user-card`,
		`data-admin-user-view`,
		`data-admin-user-form`,
		`data-admin-user-edit aria-expanded="false"`,
		`data-admin-recovery-root`,
		`data-admin-recovery-action="password" aria-expanded="false"`,
		`data-admin-recovery-form="password"`,
		`data-admin-user-workspace-row`,
		`data-admin-user-workspace-toggle`,
		`data-admin-user-workspace-role`,
		`for="adminUserName"`,
		`for="adminUserEmail"`,
		`hx-delete="/users/`,
		`admin-danger-zone`,
		`Excluir definitivamente`,
		`data-sensitive`,
	} {
		assertContains(t, users, want)
	}

	workspaces := renderAdminSection(t, handler, "admin-workspaces")
	for _, want := range []string{
		`data-admin-ws-create`,
		`data-admin-ws-card`,
		`data-admin-ws-view`,
		`data-admin-ws-form`,
		`data-admin-ws-toggle aria-expanded="false"`,
		`data-admin-ws-type`,
		`data-admin-ws-business`,
		`data-admin-mask="cnpj"`,
		`data-admin-mask="phone"`,
		`for="adminWorkspaceCompany"`,
		`value="business" selected`,
		`value="violeta" selected`,
		`hx-delete="/workspaces/`,
		`admin-danger-zone`,
		`Excluir workspace`,
	} {
		assertContains(t, workspaces, want)
	}
	if strings.Index(workspaces, `data-admin-ws-toggle`) > strings.Index(workspaces, `data-admin-ws-view`) {
		t.Fatal("workspace edit toggle must remain outside the hidden view so Cancel stays reachable")
	}

	backups := renderAdminSection(t, handler, "admin-backups")
	for _, want := range []string{
		`href="/admin/backups/exportar"`,
		`data-backup-dropzone`,
		`hx-post="/admin/backups/importar"`,
		`hx-disabled-elt="find button[type='submit']"`,
		`name="backup_file"`,
		`accept=".db"`,
		`admin-backup-panel--danger`,
		`Ação destrutiva`,
		`admin-restore-warning`,
		`Importando...`,
	} {
		assertContains(t, backups, want)
	}

	audit := renderAdminSection(t, handler, "admin-auditoria")
	for _, want := range []string{
		`Configuração por workspace`,
		`Escopo do workspace ativo`,
		`name="smtp_pass" value=""`,
		`autocomplete="new-password"`,
		`Sem credencial`,
		`name="event_type"`,
		`name="severity"`,
		`hx-target="#auditoria-table-body"`,
		`id="auditoria-table-body"`,
		`scope="col"`,
		`admin-table-wrap" role="region"`,
		`tabindex="0"`,
		`admin-scroll-hint`,
		`Role horizontalmente`,
	} {
		assertContains(t, audit, want)
	}
	if strings.Contains(audit, `name="smtp_pass_remove"`) {
		t.Fatal("sem credencial configurada a ação de remoção não deve aparecer")
	}
}

func TestAdminAuditCredentialBadgeAndRemovalStaySeparated(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)
	execTestSQL(t, handler.DB, `UPDATE workspaces SET smtp_pass = 'sec:v1:sintetico' WHERE id = 'config-workspace'`)

	body := renderAdminSection(t, handler, "admin-auditoria")
	for _, want := range []string{
		`Credencial configurada`,
		`name="smtp_pass_remove"`,
		`admin-credential-danger`,
		`Remover credencial SMTP`,
		`hx-confirm="Remover a credencial SMTP salva?`,
	} {
		assertContains(t, body, want)
	}
	if strings.Contains(body, `value="sec:v1:`) || strings.Contains(body, "sintetico") {
		t.Fatal("a credencial SMTP não pode aparecer no HTML")
	}
}

func TestAdminPremiumTemplatesHaveNoInlineStyles(t *testing.T) {
	for _, path := range []string{
		"templates/pages/configuracoes_admin_users.html",
		"templates/pages/configuracoes_admin_workspaces.html",
		"templates/pages/configuracoes_admin_backups.html",
		"templates/pages/configuracoes_admin_auditoria.html",
	} {
		content := readTestFile(t, path)
		if strings.Contains(content, "style=") {
			t.Fatalf("%s must not use inline styles (CSP style-src-attr none)", path)
		}
	}
}

func TestAdminPremiumIDsAreUniqueAndLabelsPointToExistingFields(t *testing.T) {
	idPattern := regexp.MustCompile(`id="([^"]+)"`)
	labelPattern := regexp.MustCompile(`for="([^"]+)"`)

	for _, section := range []string{"admin-users", "admin-workspaces", "admin-backups", "admin-auditoria"} {
		t.Run(section, func(t *testing.T) {
			handler := newConfiguracoesRenderTestHandler(t)
			body := renderAdminSection(t, handler, section)
			start := strings.Index(body, `id="settings-dynamic-payload"`)
			if start < 0 {
				t.Fatal("payload not found")
			}
			end := strings.LastIndex(body[start:], "</main>")
			if end < 0 {
				t.Fatal("main end not found")
			}
			payload := body[start : start+end]

			ids := map[string]bool{}
			for _, match := range idPattern.FindAllStringSubmatch(payload, -1) {
				if ids[match[1]] {
					t.Fatalf("duplicate id %q in %s", match[1], section)
				}
				ids[match[1]] = true
			}
			for _, match := range labelPattern.FindAllStringSubmatch(payload, -1) {
				if !ids[match[1]] {
					t.Fatalf("label for=%q has no matching id in %s", match[1], section)
				}
			}
		})
	}
}

func TestAdminPremiumCssKeepsTouchTargetsScoped(t *testing.T) {
	css := readTestFile(t, "assets/css/admin-premium.css")
	for _, want := range []string{
		`.cb-v2-admin-main`,
		`@media (max-width: 1023px)`,
		`min-height: 44px`,
		`font-size: 16px`,
		`@media (max-width: 767px)`,
		`.admin-recovery__form:not(.hidden)`,
		`.admin-scroll-hint.is-visible`,
		`.admin-user-workspace-row input[type="checkbox"]:focus-visible + span`,
		`#global-confirm-modal.admin-confirm-open button`,
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("admin-premium.css must contain %q", want)
		}
	}
	if strings.Contains(css, "\nbody {") {
		t.Fatal("admin-premium.css must not define global body rules")
	}
}
