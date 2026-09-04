package handlers

import (
	"bytes"
	"html/template"
	"os"
	"strings"
	"testing"
)

func TestNotificacoesPageUsesGlobalAppShell(t *testing.T) {
	tpl := testNotificacoesRenderTemplates(t)
	data := NotificacoesData{
		Title:               "Notificações",
		UserName:            "Usuário Teste",
		UserInitials:        "UT",
		NotificationCount:   1,
		ActiveWorkspaceName: "Workspace A",
		Items: []NotificationItem{{
			Key:         "invoice:invoice-a",
			Title:       "Fatura aguardando pagamento",
			Description: "Cartão A · vence em 07/09",
			Money:       MoneyMinor(12345),
			Icon:        "triangle-alert",
			Color:       "amber",
		}},
	}

	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "notificacoes-page", data); err != nil {
		t.Fatalf("render notificacoes page: %v", err)
	}

	body := out.String()
	for _, want := range []string{
		`data-app-shell`,
		`id="main-content"`,
		`data-page-section="notifications"`,
		`/assets/css/notificacoes.css`,
		`data-swipe-notification`,
		`data-swipe-delete`,
		`hx-delete="/notificacoes/invoice:invoice-a"`,
		`hx-target="closest article"`,
		`hx-delete="/notificacoes/limpar"`,
		`hx-select="#main-content"`,
		`id="notification-badge"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("notificacoes page missing %q", want)
		}
	}
}

func TestNotificationBadgeHTMLUsesV2BadgeTarget(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaNotificacoesScenario(t, db)
	insertCaixinhaNotification(db, "user-a", "ws-a", "Aviso", "Mensagem", "caixinha.aporte")

	html := NotificationBadgeHTML(db, "user-a", "ws-a")
	if !strings.Contains(html, `id="notification-badge"`) || !strings.Contains(html, `hx-swap-oob="true"`) {
		t.Fatalf("badge OOB target mismatch: %s", html)
	}
}

func testNotificacoesRenderTemplates(t *testing.T) *template.Template {
	t.Helper()
	pagePath := resolveTemplatePath(t, "templates/pages/notificacoes.html")
	shellPath := resolveTemplatePath(t, "templates/components/app_shell.html")
	page, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatalf("read notifications template: %v", err)
	}
	shell, err := os.ReadFile(shellPath)
	if err != nil {
		t.Fatalf("read app shell template: %v", err)
	}
	return template.Must(template.New("notificacoes-render-test").Funcs(withAppShellTestFuncs(template.FuncMap{
		"assetPath": func(path string) string { return path + "?v=test" },
	})).Parse(string(page) + string(shell)))
}
