package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/security"
)

const legacySMTPFixture = "sec:v1:AAECAwQFBgcICQoL:NLiC-eMkHgguaU-W8QZcl_wHP1xR5QIBnrG6gu0oMJW2CpwD1A"

func TestSMTPMissingMasterKeyPreservesStartupAndStoredCredential(t *testing.T) {
	for _, appEnv := range []string{"development", "production"} {
		for _, state := range []string{"empty", "encrypted", "legacy-fallback"} {
			t.Run(appEnv+"/"+state, func(t *testing.T) {
				t.Setenv("APP_ENV", appEnv)
				t.Setenv("DATA_DIR", t.TempDir())
				t.Setenv("SECURITY_MASTER_KEY", "0123456789abcdef0123456789abcdef")
				stored := ""
				if state == "encrypted" {
					var err error
					stored, err = security.Encrypt("synthetic-smtp-password")
					if err != nil {
						t.Fatal(err)
					}
				} else if state == "legacy-fallback" {
					stored = legacySMTPFixture
				}
				dbs := openFullBindingDBs(t, filepath.Join(t.TempDir(), "smtp.db"), "smtp")
				defer dbs.Close()
				if _, err := serverFixtureExec(t, dbs.DB, `INSERT INTO workspaces (id, name, smtp_pass) VALUES ('ws-smtp', 'Synthetic SMTP', ?)`, stored); err != nil {
					t.Fatal(err)
				}
				t.Setenv("SECURITY_MASTER_KEY", "")
				runtime, err := startServerRuntime(dbs, serverRuntimeOptions{})
				if err != nil {
					t.Fatalf("optional SMTP must not block startup: %v", err)
				}
				defer runtime.Close()
				audit := newSecurityAuditService(func() *sql.DB { return dbs.DB })
				cfg, err := audit.loadAlertConfig(dbs.DB, "ws-smtp")
				if state == "empty" {
					if err != nil || cfg.Pass != "" {
						t.Fatalf("SMTP without a stored secret must not require a key: %v", err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "SECURITY_MASTER_KEY") || cfg.Pass != "" {
					t.Fatal("SMTP credential loading must fail safely with a clear configuration error")
				}
				var after string
				if err := dbs.DB.QueryRow(`SELECT smtp_pass FROM workspaces WHERE id = 'ws-smtp'`).Scan(&after); err != nil || after != stored {
					t.Fatalf("startup or failed read changed ciphertext: %v", err)
				}
			})
		}
	}
}

func TestSMTPMasterKeySurvivesRuntimeRestartAndEnvironmentChange(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("SECURITY_MASTER_KEY", "0123456789abcdef0123456789abcdef")
	stored, err := security.Encrypt("synthetic-smtp-password")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "smtp.db")
	dbs := openFullBindingDBs(t, path, "smtp")
	defer dbs.Close()
	if _, err := serverFixtureExec(t, dbs.DB, `INSERT INTO workspaces (id, name, smtp_pass) VALUES ('ws-smtp', 'Synthetic SMTP', ?)`, stored); err != nil {
		t.Fatal(err)
	}
	for _, appEnv := range []string{"development", "production"} {
		t.Setenv("APP_ENV", appEnv)
		if appEnv == "production" {
			dbs, err = database.OpenRuntimeDBs("file:" + path)
			if err != nil {
				t.Fatal(err)
			}
		}
		runtime, err := startServerRuntime(dbs, serverRuntimeOptions{})
		if err != nil {
			_ = dbs.Close()
			t.Fatal(err)
		}
		audit := newSecurityAuditService(func() *sql.DB { return dbs.DB })
		cfg, readErr := audit.loadAlertConfig(dbs.DB, "ws-smtp")
		var after string
		queryErr := dbs.DB.QueryRow(`SELECT smtp_pass FROM workspaces WHERE id = 'ws-smtp'`).Scan(&after)
		closeErr := runtime.Close()
		if readErr != nil || queryErr != nil || closeErr != nil || cfg.Pass != "synthetic-smtp-password" || after != stored {
			t.Fatalf("restart with the same key must preserve and decrypt ciphertext: read=%v query=%v close=%v", readErr, queryErr, closeErr)
		}
	}
}
