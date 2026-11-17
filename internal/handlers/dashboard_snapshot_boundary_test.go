package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"html/template"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This preparatory test exercises existing readers, not the still-independent
// queries of a complete Dashboard render.
func TestDashboardSnapshotBoundaryWALCommitRollbackAndIsolation(t *testing.T) {
	pools := openLockingRuntimeDBsAt(t, filepath.Join(t.TempDir(), "dashboard-snapshot.db"), "")
	a := seedF1Base(t, pools.DB, "snapshot-a")
	b := seedF1Base(t, pools.DB, "snapshot-b")
	now := projectionTestNow()
	addF1Invoice(t, pools.DB, "snapshot-invoice", a.card, "2026-11", "OPEN", 0, now.Unix())
	addF1Tx(t, pools.DB, f1TxSeed{ID: "snapshot-purchase", WorkspaceID: a.ws, UserID: a.user, AccountID: a.card, InvoiceID: "snapshot-invoice", Type: "EXPENSE", Amount: 5000, Status: "paid", CreatedAt: now.Unix()})
	execTestSQL(t, pools.DB, `UPDATE accounts SET current_balance=77777 WHERE workspace_id=? AND id=?`, b.ws, b.checking)
	var journal string
	if err := pools.DB.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil || journal != "wal" {
		t.Fatalf("journal=%q err=%v", journal, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reader, err := pools.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	readBalance := func(q reportQueryer, workspace, account string) int64 {
		t.Helper()
		var balance int64
		if err := q.QueryRowContext(ctx, `SELECT current_balance FROM accounts WHERE workspace_id=? AND id=?`, workspace, account).Scan(&balance); err != nil {
			t.Fatal(err)
		}
		return balance
	}
	if got := readBalance(reader, a.ws, a.checking); got != 10000 {
		t.Fatalf("initial balance=%d", got)
	}
	// The first SELECT above anchors the snapshot before the writer is started.
	written := make(chan error, 1)
	go func() {
		writer, err := pools.Immediate.BeginTx(ctx, nil)
		if err != nil {
			written <- err
			return
		}
		defer writer.Rollback()
		if _, err = writer.ExecContext(ctx, `UPDATE accounts SET current_balance=9000 WHERE workspace_id=? AND id=?`, a.ws, a.checking); err == nil {
			_, err = writer.ExecContext(ctx, `UPDATE transactions SET amount=6000 WHERE workspace_id=? AND id='snapshot-purchase'`, a.ws)
		}
		if err == nil {
			err = writer.Commit()
		}
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("WAL writer did not commit while the reader held its snapshot")
	}
	if got := readBalance(reader, a.ws, a.checking); got != 10000 {
		t.Fatalf("mixed account snapshot=%d", got)
	}
	invoice, err := loadInvoiceFinancialSnapshotTx(reader, a.ws, "snapshot-invoice")
	if err != nil || invoice.Total != 5000 {
		t.Fatalf("snapshot invoice=%d err=%v", invoice.Total, err)
	}
	projection, err := buildDashboardFinancialProjectionTx(ctx, reader, a.ws, appMonthStartUTC(now), now)
	if err != nil || len(projection.Months) == 0 || projection.Months[0].Pending.Expense.rawCents() != 5000 {
		t.Fatalf("snapshot projection=%+v err=%v", projection.Months, err)
	}
	if _, err := loadInvoiceFinancialSnapshotTx(reader, b.ws, "snapshot-invoice"); err != sql.ErrNoRows {
		t.Fatalf("cross-workspace invoice: %v", err)
	}
	if got := readBalance(reader, b.ws, b.checking); got != 77777 {
		t.Fatalf("workspace B=%d", got)
	}
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := readBalance(pools.DB, a.ws, a.checking); got != 9000 {
		t.Fatalf("fresh account=%d", got)
	}
	fresh, err := loadInvoiceFinancialSnapshotDB(pools.DB, a.ws, "snapshot-invoice")
	if err != nil || fresh.Total != 6000 {
		t.Fatalf("fresh invoice=%d err=%v", fresh.Total, err)
	}
	rollback, err := pools.Immediate.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback.Rollback()
	for _, query := range []string{
		`UPDATE accounts SET current_balance=1 WHERE workspace_id=? AND type='CHECKING'`,
		`UPDATE transactions SET amount=1 WHERE workspace_id=? AND id='snapshot-purchase'`,
	} {
		if _, err := rollback.ExecContext(ctx, query, a.ws); err != nil {
			t.Fatal(err)
		}
	}
	if err := rollback.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := readBalance(pools.DB, a.ws, a.checking); got != 9000 {
		t.Fatalf("rollback account=%d", got)
	}
	fresh, err = loadInvoiceFinancialSnapshotDB(pools.DB, a.ws, "snapshot-invoice")
	if err != nil || fresh.Total != 6000 {
		t.Fatalf("rollback invoice=%d err=%v", fresh.Total, err)
	}
	t.Log("snapshot account/invoice/projection=10000/5000/5000; committed=9000/6000; rollback preserved; tenant B=77777")
}

func TestDashboardSnapshotPreparationPreservesInvoiceCreation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "snapshot-lifecycle")
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES (?, ?, 20, 10, 100000)`, "snapshot-card-settings", base.card)
	count := func() int {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM invoices i JOIN accounts a ON a.id=i.account_id WHERE a.workspace_id=?`, base.ws).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 0 {
		t.Fatalf("fixture invoices=%d", n)
	}
	data := buildDashboardPreviewDataAt(db, base.user, base.ws, projectionTestNow(), "6m", "")
	if n := count(); n != 1 {
		t.Fatalf("current builder created invoices=%d, want 1", n)
	}
	if len(data.Cards) != 1 {
		t.Fatalf("cards=%d, want 1", len(data.Cards))
	}
	t.Log("SSR preparation creates the existing invoice once, before the read snapshot")
}

// Instrument the real SQLite driver so the commit occurs between two actual
// reads of the production compositor, without production hooks or sleeps.
type dashboardSnapshotConnector struct {
	driver  driver.Driver
	dsn     string
	once    sync.Once
	between func() error
	begins  atomic.Int64
	writes  atomic.Int64
}

func (c *dashboardSnapshotConnector) Driver() driver.Driver { return c.driver }
func (c *dashboardSnapshotConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &dashboardSnapshotConn{Conn: conn, c: c}, nil
}

type dashboardSnapshotConn struct {
	driver.Conn
	c *dashboardSnapshotConnector
}

func (c *dashboardSnapshotConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.c.begins.Add(1)
	if !opts.ReadOnly {
		return nil, fmt.Errorf("dashboard opened a writable transaction")
	}
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c *dashboardSnapshotConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	var hookErr error
	if strings.Contains(q, "a.current_balance") && strings.Contains(q, "a.sort_order") {
		c.c.once.Do(func() {
			if c.c.between != nil {
				hookErr = c.c.between()
			}
		})
	}
	if hookErr != nil {
		return nil, hookErr
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}
func (c *dashboardSnapshotConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.c.writes.Add(1)
	return nil, fmt.Errorf("write attempted by dashboard reader")
}

func TestDashboardSnapshotAllCardsConcurrentCommit(t *testing.T) {
	for _, kind := range []string{"personal", "business"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "all-cards.db")
			pools := openLockingRuntimeDBsAt(t, path, "")
			a := seedF1Base(t, pools.DB, "all-a")
			b := seedF1Base(t, pools.DB, "all-b")
			now := projectionTestNow()
			date := now.AddDate(0, 0, -5).Unix()
			execTestSQL(t, pools.DB, `UPDATE workspaces SET type=? WHERE id=?`, kind, a.ws)
			execTestSQL(t, pools.DB, `INSERT INTO credit_cards(id,account_id,closing_day,due_day,credit_limit) VALUES('settings',?,20,11,100000)`, a.card)
			execTestSQL(t, pools.DB, `INSERT INTO cost_limits(id,workspace_id,category_id,max_amount_monthly) VALUES('limit',?,?,10000)`, a.ws, a.category)
			addF1Invoice(t, pools.DB, "all-invoice", a.card, "2026-11", "OPEN", 0, now.Unix())
			for _, seed := range []f1TxSeed{
				{ID: "purchase", WorkspaceID: a.ws, UserID: a.user, AccountID: a.card, InvoiceID: "all-invoice", CategoryID: a.category, Type: "EXPENSE", Amount: 5000, Status: "paid", CreatedAt: date},
				{ID: "expense", WorkspaceID: a.ws, UserID: a.user, AccountID: a.checking, CategoryID: a.category, Type: "EXPENSE", Amount: 1000, Status: "paid", CreatedAt: date},
				{ID: "income", WorkspaceID: a.ws, UserID: a.user, AccountID: a.checking, Type: "INCOME", Amount: 4000, Status: "paid", CreatedAt: date},
				{ID: "pending", WorkspaceID: a.ws, UserID: a.user, AccountID: a.checking, CategoryID: a.category, Type: "EXPENSE", Amount: 2000, Status: "pending", DueDate: now.AddDate(0, 0, 1).Unix(), CreatedAt: date},
				{ID: "foreign", WorkspaceID: b.ws, UserID: b.user, AccountID: b.checking, Type: "INCOME", Amount: 999999, Status: "paid", CreatedAt: date},
			} {
				addF1Tx(t, pools.DB, seed)
			}
			execTestSQL(t, pools.DB, `UPDATE transactions SET paid_at=?, paid_date_civil='2026-11-05' WHERE status='paid'`, date)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			registration, err := sql.Open("sqlite", "file:"+path)
			if err != nil {
				t.Fatal(err)
			}
			baseDriver := registration.Driver()
			registration.Close()
			connector := &dashboardSnapshotConnector{driver: baseDriver, dsn: "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(1000)"}
			connector.between = func() error {
				done := make(chan error, 1)
				go func() {
					tx, err := pools.Immediate.BeginTx(ctx, nil)
					if err != nil {
						done <- err
						return
					}
					defer tx.Rollback()
					_, err = tx.ExecContext(ctx, `UPDATE accounts SET current_balance=9000 WHERE workspace_id=? AND id=?`, a.ws, a.checking)
					if err == nil {
						_, err = tx.ExecContext(ctx, `UPDATE transactions SET amount=CASE id WHEN 'purchase' THEN 6000 WHEN 'expense' THEN 1500 WHEN 'income' THEN 7000 WHEN 'pending' THEN 3000 ELSE amount END WHERE workspace_id=?`, a.ws)
					}
					if err == nil {
						err = tx.Commit()
					}
					done <- err
				}()
				select {
				case err := <-done:
					return err
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			reader := sql.OpenDB(connector)
			defer reader.Close()
			reader.SetMaxOpenConns(1)
			data, err := buildDashboardV2Snapshot(ctx, reader, a.user, a.ws, now, "6m", "", false)
			if err != nil {
				t.Fatal(err)
			}
			check := func(label string, got, want int64) {
				t.Helper()
				if got != want {
					t.Fatalf("%s=%d want %d", label, got, want)
				}
			}
			check("balance", data.Balance.Money.rawCents(), 10000)
			if len(data.Accounts) != 1 || len(data.Cards) != 1 || len(data.Limits) != 1 {
				t.Fatalf("accounts/cards/limits=%d/%d/%d", len(data.Accounts), len(data.Cards), len(data.Limits))
			}
			check("accounts", data.Accounts[0].Money.rawCents(), 10000)
			check("cards", data.Cards[0].Money.rawCents(), 5000)
			if data.Cards[0].Status != "OPEN" {
				t.Fatalf("reader changed invoice status: %s", data.Cards[0].Status)
			}
			check("commitments", data.Payable7dTotal.rawCents(), 7000)
			check("financial pending", data.Financial.Selected.PendingExpense.rawCents(), 7000)
			check("financial realized", data.Financial.Selected.RealizedExpense.rawCents(), 1000)
			check("cash flow", data.CashFlow.TotalExpense.rawCents(), 1000)
			check("income isolation", data.CashFlow.TotalIncome.rawCents(), 4000)
			check("categories", data.Categories.Total.rawCents(), 8000)
			check("goals", data.Limits[0].Spent.rawCents(), 8000)
			if connector.begins.Load() != 1 || connector.writes.Load() != 0 {
				t.Fatalf("begin/write=%d/%d", connector.begins.Load(), connector.writes.Load())
			}
			fresh, err := buildDashboardV2Snapshot(ctx, reader, a.user, a.ws, now, "6m", "", false)
			if err != nil {
				t.Fatal(err)
			}
			check("fresh balance", fresh.Balance.Money.rawCents(), 9000)
			check("fresh invoice", fresh.Cards[0].Money.rawCents(), 6000)
			check("fresh financial", fresh.Financial.Selected.PendingExpense.rawCents(), 9000)
			check("fresh cash", fresh.CashFlow.TotalExpense.rawCents(), 1500)
			check("fresh categories", fresh.Categories.Total.rawCents(), 10500)
			check("fresh goals", fresh.Limits[0].Spent.rawCents(), 10500)
			t.Log("all eight cards observed old snapshot; next refresh observed committed values; no nested BEGIN or reader writes")
		})
	}
}

func TestDashboardSnapshotHTMXDoesNotPrepareAndFailureDoesNotRender(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	a := seedF1Base(t, db, "hx-snapshot")
	execTestSQL(t, db, `INSERT INTO credit_cards(id,account_id,closing_day,due_day,credit_limit) VALUES('hx-settings',?,20,10,100000)`, a.card)
	handler := DashboardV2Handler{DB: db, UserID: a.user, WorkspaceID: a.ws, Templates: template.Must(template.New("dashboard-preview-page").Parse(`V2 {{.SelectedMonth}}`))}
	request := httptest.NewRequest("GET", "/?mes=2026-11&fluxo=12m", nil)
	request.Header.Set("HX-Request", "true")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != 503 || strings.Contains(recorder.Body.String(), "V2") {
		t.Fatalf("missing invoice refresh=%d %s", recorder.Code, recorder.Body.String())
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM invoices`).Scan(&count)
	if count != 0 {
		t.Fatalf("HTMX created %d invoices", count)
	}
	if err := prepareDashboardInvoices(db, a.ws, projectionTestNow()); err != nil {
		t.Fatal(err)
	}
	if err := prepareDashboardInvoices(db, a.ws, projectionTestNow()); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT COUNT(*) FROM invoices`).Scan(&count)
	if count != 1 {
		t.Fatalf("preparation not idempotent: %d", count)
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "V2 2026-11") {
		t.Fatalf("refresh=%d %s", recorder.Code, recorder.Body.String())
	}
	db.QueryRow(`SELECT COUNT(*) FROM invoices`).Scan(&count)
	if count != 1 {
		t.Fatalf("refresh created %d invoices", count)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := buildDashboardV2Snapshot(ctx, db, a.user, a.ws, projectionTestNow(), "6m", "", true); err != context.Canceled {
		t.Fatalf("cancelled=%v", err)
	}
}

func TestDashboardSnapshotQueryFailureReleasesTransaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failure.db")
	pools := openLockingRuntimeDBsAt(t, path, "")
	a := seedF1Base(t, pools.DB, "snapshot-failure")
	registration, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	connector := &dashboardSnapshotConnector{driver: registration.Driver(), dsn: "file:" + path, between: func() error { return fmt.Errorf("synthetic query failure") }}
	registration.Close()
	reader := sql.OpenDB(connector)
	defer reader.Close()
	reader.SetMaxOpenConns(1)
	data, err := buildDashboardV2Snapshot(context.Background(), reader, a.user, a.ws, projectionTestNow(), "6m", "", false)
	if err == nil || !strings.Contains(err.Error(), "synthetic query failure") {
		t.Fatalf("query failure=%v", err)
	}
	if len(data.Accounts) != 0 || data.Balance.RealBalanceAvailable {
		t.Fatal("failure returned partial numeric data")
	}
	if reader.Stats().InUse != 0 {
		t.Fatal("failed snapshot retained a connection")
	}
	if connector.writes.Load() != 0 {
		t.Fatal("failed snapshot attempted a write")
	}
}
