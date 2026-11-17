package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/contabase-app/contabase/internal/paths"
	"github.com/contabase-app/contabase/internal/runtimelease"
	"github.com/google/uuid"
)

type backupExportReceipt struct {
	OperationID            string `json:"operation_id"`
	GenerationID           string `json:"generation_id"`
	ActorUserID            string `json:"actor_user_id"`
	WorkspaceID            string `json:"workspace_id"`
	Event                  string `json:"event"`
	State                  string `json:"state"`
	Timestamp              string `json:"timestamp_utc"`
	BytesWritten           int64  `json:"bytes_written"`
	ClientReceiptConfirmed bool   `json:"client_receipt_confirmed"`
}

// Hooks belong to one service instance; production always uses real I/O.
type backupExportAuditOps struct {
	write         func(*os.File, []byte) (int, error)
	syncFile      func(*os.File) error
	syncDirectory func(*os.File) error
}

type backupExportAudit struct {
	start      backupExportReceipt
	root       *os.Root
	ops        *backupExportAuditOps
	finishOnce sync.Once
	finishErr  error
	notify     func()
}

func beginBackupExportAudit(r *http.Request, db *sql.DB, actorUserID string) (*backupExportAudit, error) {
	carrier, _ := r.Context().Value(securityAuditContextKey).(*securityAuditRequestContext)
	if carrier == nil || !runtimelease.Held(r.Context()) || runtimelease.Generation(r.Context()) == "" {
		return nil, fmt.Errorf("export audit requires authenticated runtime middleware")
	}
	actor, authenticated := carrier.authenticatedContext()
	if !authenticated || actor.UserID != actorUserID || !isGlobalAdmin(actor) {
		return nil, fmt.Errorf("export audit identity mismatch")
	}
	start := backupExportReceipt{
		OperationID: uuid.NewString(), GenerationID: runtimelease.Generation(r.Context()),
		ActorUserID: actor.UserID, WorkspaceID: actor.ActiveWorkspaceID,
		Event: "backup.export.started", State: "authorized", Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	}
	root, err := openBackupExportAuditDirectory(start.OperationID)
	if err != nil {
		return nil, err
	}
	audit := &backupExportAudit{start: start, root: root, ops: carrier.exportAuditOps}
	if err := audit.publish("started.json", start); err != nil {
		_ = root.Close()
		return nil, err
	}
	carrier.exportAuditHandled = true
	metadata, err := json.Marshal(start)
	if err == nil {
		_, err = db.Exec(`INSERT INTO security_logs
			(id, workspace_id, user_id, event_type, severity, ip_address, user_agent, metadata, created_at)
			VALUES (?, ?, ?, 'backup.export.started', 'INFO', ?, ?, ?, unixepoch())`,
			start.OperationID, actor.ActiveWorkspaceID, actor.UserID, clientIP(r), r.UserAgent(), string(metadata))
	}
	if err != nil {
		return nil, fmt.Errorf("persist export authorization: %w", errors.Join(err, audit.finish("authorization_audit_failed", 0)))
	}
	audit.prepareNotification(carrier.exportAuditService, db, r)
	return audit, nil
}

func (a *backupExportAudit) prepareNotification(service *securityAuditService, db *sql.DB, r *http.Request) {
	if service == nil || service.notifier == nil {
		return
	}
	cfg, err := service.loadAlertConfig(db, a.start.WorkspaceID)
	if err != nil {
		slog.Error("backup_export_notification_prepare_failed", "operation_id", a.start.OperationID)
		return
	}
	if !eventEmailEnabled(cfg.Preferences, "backup.export") || strings.TrimSpace(cfg.NotificationEmail) == "" || cfg.Host == "" || cfg.Port <= 0 {
		return
	}
	notifier, ip, agent := service.notifier, clientIP(r), strings.TrimSpace(r.UserAgent())
	operationID, generationID := a.start.OperationID, a.start.GenerationID
	// All configuration and identity come from G1 while its lease is held.
	// Completion uses the process-owned notifier, never a database provider.
	a.notify = func() {
		body := fmt.Sprintf("Evento: backup.export\nStatus: server_write_completed\nOperação: %s\nGeração: %s\nIP: %s\nDispositivo: %s\nMomento: %s\nRecebimento pelo cliente: não confirmado\n", operationID, generationID, ip, agent, time.Now().UTC().Format(time.RFC3339Nano))
		notifier.sendAsync(cfg, emailSubjectForEvent("backup.export"), body)
	}
}

func openBackupExportAuditDirectory(operationID string) (*os.Root, error) {
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(paths.DataDir())
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for _, name := range []string{"audit", "audit/backup-exports"} {
		if err := root.Mkdir(name, 0o700); err != nil && !os.IsExist(err) {
			return nil, err
		}
		info, err := root.Lstat(name)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("audit path must be a real directory")
		}
		if err := root.Chmod(name, 0o700); err != nil {
			return nil, err
		}
		parent, err := root.Open(filepath.Dir(name))
		if err != nil {
			return nil, err
		}
		err = errors.Join(parent.Sync(), parent.Close())
		if err != nil {
			return nil, err
		}
	}
	name := filepath.Join("audit", "backup-exports", operationID)
	if err := root.Mkdir(name, 0o700); err != nil {
		return nil, err
	}
	parent, err := root.Open("audit/backup-exports")
	if err != nil {
		return nil, err
	}
	err = errors.Join(parent.Sync(), parent.Close())
	if err != nil {
		return nil, err
	}
	return root.OpenRoot(name)
}

func (a *backupExportAudit) publish(name string, receipt backupExportReceipt) error {
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	pending := name + ".pending"
	f, err := a.root.OpenFile(pending, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer a.root.Remove(pending)
	write := (*os.File).Write
	syncFile := (*os.File).Sync
	if a.ops != nil {
		if a.ops.write != nil {
			write = a.ops.write
		}
		if a.ops.syncFile != nil {
			syncFile = a.ops.syncFile
		}
	}
	n, writeErr := write(f, data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	var syncErr error
	if writeErr == nil {
		syncErr = syncFile(f)
	}
	if err := errors.Join(writeErr, syncErr, f.Close()); err != nil {
		return err
	}
	// A final event is visible only as a complete, synced inode. Link refuses
	// duplicates, while a crash leaving only *.pending never implies completion.
	if err := a.root.Link(pending, name); err != nil {
		return err
	}
	if err := a.root.Remove(pending); err != nil {
		return err
	}
	dir, err := a.root.Open(".")
	if err != nil {
		return err
	}
	syncDir := (*os.File).Sync
	if a.ops != nil && a.ops.syncDirectory != nil {
		syncDir = a.ops.syncDirectory
	}
	return errors.Join(syncDir(dir), dir.Close())
}

func (a *backupExportAudit) finish(state string, bytesWritten int64) error {
	a.finishOnce.Do(func() {
		end := a.start
		end.Event = "backup.export.failed"
		if state == "server_write_completed" {
			end.Event = "backup.export"
		}
		end.State = state
		end.BytesWritten = bytesWritten
		end.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
		a.finishErr = errors.Join(a.publish("finished.json", end), a.root.Close())
		if a.finishErr == nil && state == "server_write_completed" && a.notify != nil {
			a.notify()
		}
	})
	return a.finishErr
}
