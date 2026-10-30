package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
	sqliteDriver "modernc.org/sqlite"
)

// Per-DB instrumentation delegates all successful operations to real SQLite.
// It observes statement/bind/transaction/iterator boundaries and can inject
// driver errors at discovery boundaries without production synchronization hooks.
type n24Statement struct {
	query string
	args  []driver.NamedValue
	inTx  bool
}
type n24Probe struct {
	statements                                       []n24Statement
	activeRows, activeTx, begins, commits, rollbacks int
	before                                           func(string, []driver.NamedValue) error
	afterClose                                       func(string)
	rowFault                                         func(string, []driver.NamedValue) string
	reverseWorkspaces                                bool
}
type n24Connector struct {
	dsn   string
	probe *n24Probe
}

func (c n24Connector) Driver() driver.Driver { return &sqliteDriver.Driver{} }
func (c n24Connector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &n24Conn{Conn: conn, probe: c.probe}, nil
}

type n24Conn struct {
	driver.Conn
	probe *n24Probe
}

func (c *n24Conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	c.probe.activeTx++
	c.probe.begins++
	return &n24Tx{Tx: tx, probe: c.probe}, nil
}

type n24Tx struct {
	driver.Tx
	probe *n24Probe
}

func (tx *n24Tx) Commit() error {
	err := tx.Tx.Commit()
	tx.probe.activeTx--
	tx.probe.commits++
	return err
}
func (tx *n24Tx) Rollback() error {
	err := tx.Tx.Rollback()
	tx.probe.activeTx--
	tx.probe.rollbacks++
	return err
}
func (c *n24Conn) record(q string, args []driver.NamedValue) error {
	p := c.probe
	if p.activeRows != 0 {
		return errors.New("N24 overlapping rows iterators")
	}
	p.statements = append(p.statements, n24Statement{q, append([]driver.NamedValue(nil), args...), p.activeTx == 1})
	if p.before != nil {
		return p.before(q, args)
	}
	return nil
}
func (c *n24Conn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.record(q, args); err != nil {
		return nil, err
	}
	actual := q
	if c.probe.reverseWorkspaces && q == "SELECT id FROM workspaces ORDER BY id" {
		actual += " DESC"
	}
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, actual, args)
	if err != nil {
		return nil, err
	}
	fault := ""
	if c.probe.rowFault != nil {
		fault = c.probe.rowFault(q, args)
	}
	c.probe.activeRows++
	return &n24Rows{Rows: rows, probe: c.probe, query: q, fault: fault}, nil
}
func (c *n24Conn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.record(q, args); err != nil {
		return nil, err
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}

type n24Rows struct {
	driver.Rows
	probe        *n24Probe
	query, fault string
	read, closed bool
}

func (r *n24Rows) Next(values []driver.Value) error {
	if r.fault == "next" && r.read {
		return errN24Discovery
	}
	err := r.Rows.Next(values)
	if err == nil {
		r.read = true
		if r.fault == "scan" {
			values[0] = nil
		}
	}
	return err
}
func (r *n24Rows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	err := r.Rows.Close()
	r.probe.activeRows--
	if r.probe.afterClose != nil {
		r.probe.afterClose(r.query)
	}
	if r.fault == "close" && err == nil {
		return errN24Discovery
	}
	return err
}

var errN24Discovery = errors.New("N24 injected discovery error")

func n24DB(t *testing.T) (*sql.DB, *sql.DB, *n24Probe) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "n24.db")
	runtimeDBs, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtimeDBs.Close() })
	writer := runtimeDBs.DB
	p := &n24Probe{}
	db := sql.OpenDB(n24Connector{dsn: "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", probe: p})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db, writer, p
}
func n24Now() time.Time { return time.Date(2026, 10, 8, 15, 0, 0, 0, time.UTC) }
func n24SeedWorkspace(t *testing.T, db *sql.DB, ws string) {
	t.Helper()
	execTestSQL(t, db, `INSERT INTO users(id,name,email,password_hash) VALUES(?,?,?,'hash')`, "user-"+ws, ws, ws+"@test.invalid")
	execTestSQL(t, db, `INSERT INTO workspaces(id,name) VALUES(?,?)`, ws, ws)
	execTestSQL(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES(?,?,'ADMIN')`, ws, "user-"+ws)
	execTestSQL(t, db, `INSERT INTO accounts(id,workspace_id,name,type,initial_balance,current_balance) VALUES(?,?,?,'CHECKING',100000,100000)`, "bank-"+ws, ws, ws)
}
func n24SeedCandidate(t *testing.T, db *sql.DB, id, ws string, hour int, created int64) {
	t.Helper()
	scheduled := time.Date(2026, 10, 8, hour, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,description,status,scheduled_payment_at,scheduled_payment_date_civil,scheduled_payment_created_by,created_at,updated_at) VALUES(?,?,?,?,'EXPENSE',12345,?,?,'pending',?,'2026-10-08',?,?,111)`, id, ws, "user-"+ws, "bank-"+ws, scheduled, "private-"+ws+"-"+id, scheduled, "user-"+ws, created)
}
func n24SeedInterleaved(t *testing.T, db *sql.DB) {
	t.Helper()
	n24SeedWorkspace(t, db, "A")
	n24SeedWorkspace(t, db, "B")
	n24SeedCandidate(t, db, "A1", "A", 10, 1)
	n24SeedCandidate(t, db, "B1", "B", 9, 3)
	n24SeedCandidate(t, db, "A2", "A", 10, 0)
	n24SeedCandidate(t, db, "B2", "B", 11, 2)
}
func n24Context(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func n24ScopedSQL(t *testing.T) (string, []any) {
	t.Helper()
	db, writer, p := n24DB(t)
	n24SeedWorkspace(t, writer, "A")
	if _, err := queryDueScheduledPayments(n24Context(t), db, n24Now()); err != nil {
		t.Fatal(err)
	}
	if len(p.statements) != 2 {
		t.Fatalf("discovery count=%d", len(p.statements))
	}
	s := p.statements[1]
	args := make([]any, len(s.args))
	for i, a := range s.args {
		args[i] = a.Value
	}
	return s.query, args
}
func TestTenantScheduledPaymentsN24ScopedDiscoveryOrderAndOwnership(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%t", reverse), func(t *testing.T) {
			db, writer, p := n24DB(t)
			n24SeedInterleaved(t, writer)
			// The existing INNER JOIN must exclude a structurally foreign account.
			n24SeedCandidate(t, writer, "foreign-account", "A", 8, 0)
			execTestSQL(t, writer, `UPDATE transactions SET account_id='bank-B' WHERE id='foreign-account'`)
			p.reverseWorkspaces = reverse
			ctx := n24Context(t)
			rows, err := queryDueScheduledPayments(ctx, db, n24Now())
			want := []string{"B1", "A2", "A1", "B2"}
			if err != nil || !reflect.DeepEqual(scheduledPaymentRowIDs(rows), want) {
				t.Fatalf("order=%v err=%v", scheduledPaymentRowIDs(rows), err)
			}
			if len(p.statements) != 3 || p.begins != 1 || p.commits != 1 || p.activeRows != 0 || p.activeTx != 0 {
				t.Fatalf("snapshot boundaries/count: %+v", p)
			}
			if p.statements[0].query != "SELECT id FROM workspaces ORDER BY id" || len(p.statements[0].args) != 0 {
				t.Fatal("enumeration must project only control-plane IDs")
			}
			for _, s := range p.statements {
				if !s.inTx {
					t.Fatal("discovery escaped snapshot")
				}
			}
			for _, s := range p.statements[1:] {
				old := strings.Replace(s.query, ", t.created_at", "", 1)
				old = strings.Replace(old, "WHERE t.workspace_id = ?\n\t\t  AND", "WHERE", 1)
				if strings.Join(strings.Fields(old), " ") != strings.Join(strings.Fields(n24ParentDiscoverySQL), " ") {
					t.Fatal("eligibility SQL differs from parent beyond the authorized scope/projection")
				}
				ws := s.args[0].Value.(string)
				args := make([]any, len(s.args))
				for i, a := range s.args {
					args[i] = a.Value
				}
				got := tenantScopeRows(t, writer, s.query, args...)
				if len(got) != 2 {
					t.Fatalf("workspace=%s candidates=%v", ws, got)
				}
				for _, r := range got {
					if r[1] != ws || r[2] != "user-"+ws || r[6] != "bank-"+ws || r[7] != "CHECKING" || !strings.HasPrefix(r[8].(string), "private-"+ws+"-") || r[5] != int64(12345) {
						t.Fatalf("foreign metadata: %v", r)
					}
				}
				if !strings.Contains(s.query, "t.workspace_id = ?") || !strings.Contains(s.query, "a.workspace_id = t.workspace_id") || strings.Contains(s.query, "LIMIT") {
					t.Fatal("scoped SQL contract")
				}
			}
			// Frozen SQL from the exact parent, independent of N24's new predicate.
			parent := tenantScopeRows(t, writer, n24ParentDiscoverySQL, appNextDayStartUnix(n24Now()), "INCOME", "EXPENSE", "CREDIT_CARD")
			var ids []string
			for _, r := range parent {
				ids = append(ids, r[0].(string))
			}
			if !reflect.DeepEqual(ids, want) {
				t.Fatalf("parent eligible set/order=%v", ids)
			}
			calls := []string{}
			p.statements = nil
			count, err := runDueScheduledPayments(ctx, db, n24Now(), func(ctx context.Context, db *sql.DB, row scheduledPaymentDueRow, now time.Time) (bool, error) {
				if p.activeTx != 0 || p.activeRows != 0 {
					t.Fatal("execution began before snapshot/rows closed")
				}
				calls = append(calls, row.ID)
				return executeScheduledPayment(ctx, db, row, now)
			})
			if err != nil || count != 4 || !reflect.DeepEqual(calls, want) {
				t.Fatalf("serial execution=%v count=%d err=%v", calls, count, err)
			}
			if len(p.statements) != 3+5*4 {
				t.Fatalf("cycle statements=%d want=23", len(p.statements))
			}
			t.Log("W=2 T=4; discovery=3; E=20; cycle=23; N+1 per transaction=0; order=B1,A2,A1,B2")
		})
	}
}
func TestTenantScheduledPaymentsN24ZeroAndFanout(t *testing.T) {
	for _, w := range []int{0, 2, 10} {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			db, writer, p := n24DB(t)
			for i := 0; i < w; i++ {
				execTestSQL(t, writer, `INSERT INTO workspaces(id,name) VALUES(?,?)`, fmt.Sprint(i), fmt.Sprint(i))
			}
			count, err := RunDueScheduledPayments(n24Context(t), db, n24Now())
			if err != nil || count != 0 || len(p.statements) != 1+w || p.begins != 1 || p.commits != 1 || p.activeRows != 0 {
				t.Fatalf("W=%d T=%d statements=%d err=%v", w, count, len(p.statements), err)
			}
			for _, s := range p.statements {
				if !strings.HasPrefix(strings.TrimSpace(s.query), "SELECT") || !s.inTx {
					t.Fatal("empty cycle mutated or escaped snapshot")
				}
			}
			t.Logf("W=%d T=0 discovery=%d SELECTs; mutations=0", w, len(p.statements))
		})
	}
}
func TestTenantScheduledPaymentsN24DiscoveryErrorsExecuteNothing(t *testing.T) {
	for _, phase := range []string{"enumeration", "A-query", "B-query", "enumeration-scan", "enumeration-next", "enumeration-close", "B-scan", "B-next", "B-close"} {
		t.Run(phase, func(t *testing.T) {
			db, writer, p := n24DB(t)
			n24SeedInterleaved(t, writer)
			before := n24FinancialState(t, writer)
			target := func(q string, args []driver.NamedValue) bool {
				if strings.HasPrefix(phase, "enumeration") {
					return q == "SELECT id FROM workspaces ORDER BY id"
				}
				return strings.Contains(q, "FROM transactions t") && args[0].Value == phase[:1]
			}
			if phase == "enumeration" || strings.HasSuffix(phase, "query") {
				p.before = func(q string, args []driver.NamedValue) error {
					if target(q, args) {
						return errN24Discovery
					}
					return nil
				}
			} else {
				p.rowFault = func(q string, args []driver.NamedValue) string {
					if target(q, args) {
						return phase[strings.Index(phase, "-")+1:]
					}
					return ""
				}
			}
			called := 0
			count, err := runDueScheduledPayments(n24Context(t), db, n24Now(), func(context.Context, *sql.DB, scheduledPaymentDueRow, time.Time) (bool, error) {
				called++
				return true, nil
			})
			if err == nil || count != 0 || called != 0 || p.activeRows != 0 || p.activeTx != 0 || p.rollbacks != 1 {
				t.Fatalf("count=%d called=%d err=%v probe=%+v", count, called, err, p)
			}
			if !reflect.DeepEqual(before, n24FinancialState(t, writer)) {
				t.Fatal("discovery error changed financial state")
			}
			// Failed discovery has no retry, including after A was materialized.
			if len(p.statements) > 3 {
				t.Fatal("discovery retried")
			}
		})
	}
}
func TestTenantScheduledPaymentsN24ExecutionFailFastPriorCommit(t *testing.T) {
	db, writer, p := n24DB(t)
	n24SeedInterleaved(t, writer)
	execTestSQL(t, writer, `CREATE TRIGGER n24_fail_history BEFORE INSERT ON transaction_status_history WHEN NEW.transaction_id='A1' BEGIN SELECT RAISE(ABORT,'N24 history failure'); END`)
	calls := []string{}
	count, err := runDueScheduledPayments(n24Context(t), db, n24Now(), func(ctx context.Context, db *sql.DB, row scheduledPaymentDueRow, now time.Time) (bool, error) {
		calls = append(calls, row.ID)
		return executeScheduledPayment(ctx, db, row, now)
	})
	if count != 2 || err == nil || !strings.Contains(err.Error(), "N24 history failure") || !reflect.DeepEqual(calls, []string{"B1", "A2", "A1"}) {
		t.Fatalf("count=%d calls=%v err=%v", count, calls, err)
	}
	assertScheduledAttemptEffects(t, writer, "B1", true)
	assertScheduledAttemptEffects(t, writer, "A2", true)
	assertScheduledAttemptEffects(t, writer, "A1", false)
	assertScheduledAttemptEffects(t, writer, "B2", false)
	balances := tenantScopeRows(t, writer, `SELECT id,current_balance FROM accounts ORDER BY id`)
	if !reflect.DeepEqual(balances, [][]any{{"bank-A", int64(87655)}, {"bank-B", int64(87655)}}) {
		t.Fatalf("rollback/earlier commit balances=%v", balances)
	}
	if p.begins != 4 || p.commits != 3 || p.rollbacks != 1 {
		t.Fatalf("per-payment boundaries: %+v", p)
	}
}
func TestTenantScheduledPaymentsN24SingleSnapshotAndNextCycle(t *testing.T) {
	db, writer, p := n24DB(t)
	n24SeedInterleaved(t, writer)
	inserted := false
	p.afterClose = func(q string) {
		// Enumeration has established the WAL snapshot; writer commits while the
		// read transaction is still open and before any tenant selection begins.
		if q != "SELECT id FROM workspaces ORDER BY id" || inserted {
			return
		}
		inserted = true
		n24SeedCandidate(t, writer, "late-A", "A", 8, 4)
		n24SeedCandidate(t, writer, "late-B", "B", 8, 5)
		n24SeedWorkspace(t, writer, "C")
		n24SeedCandidate(t, writer, "late-C", "C", 8, 6)
	}
	calls := []string{}
	count, err := runDueScheduledPayments(n24Context(t), db, n24Now(), func(ctx context.Context, db *sql.DB, row scheduledPaymentDueRow, now time.Time) (bool, error) {
		if p.activeTx != 0 || p.activeRows != 0 {
			t.Fatal("read snapshot held during payment")
		}
		calls = append(calls, row.ID)
		return executeScheduledPayment(ctx, db, row, now)
	})
	if !inserted || count != 4 || err != nil || !reflect.DeepEqual(calls, []string{"B1", "A2", "A1", "B2"}) {
		t.Fatalf("snapshot calls=%v count=%d err=%v", calls, count, err)
	}
	p.afterClose = nil
	rows, err := queryDueScheduledPayments(n24Context(t), db, n24Now())
	if err != nil || !reflect.DeepEqual(scheduledPaymentRowIDs(rows), []string{"late-A", "late-B", "late-C"}) {
		t.Fatalf("next cycle=%v err=%v", scheduledPaymentRowIDs(rows), err)
	}
	count, err = RunDueScheduledPayments(n24Context(t), db, n24Now())
	if count != 3 || err != nil {
		t.Fatalf("next cycle payments=%d err=%v", count, err)
	}
}

// UUIDs are intentionally omitted; every financial/business value is retained.
func n24FinancialState(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	return map[string][][]any{
		"transactions":  tenantScopeRows(t, db, `SELECT id,workspace_id,user_id,type,amount,account_id,description,status,paid_at,paid_date_civil,scheduled_payment_at,scheduled_payment_date_civil,scheduled_payment_created_by,scheduled_payment_executed_at,created_at,updated_at FROM transactions ORDER BY id`),
		"balances":      tenantScopeRows(t, db, `SELECT id,workspace_id,initial_balance,current_balance FROM accounts ORDER BY id`),
		"history":       tenantScopeRows(t, db, `SELECT transaction_id,workspace_id,user_id,previous_status,new_status,previous_paid_at,new_paid_at,source,created_at FROM transaction_status_history ORDER BY transaction_id`),
		"notifications": tenantScopeRows(t, db, `SELECT user_id,workspace_id,title,message,type,dedupe_key,is_read,created_at FROM user_notifications ORDER BY dedupe_key`),
	}
}

// This test also runs once with an overlay of the exact parent production file.
// The emitted state is compared byte-for-byte with N24's emitted state.
func TestTenantScheduledPaymentsN24BaselineFinancial(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db, writer, _ := n24DB(t)
	n24SeedInterleaved(t, writer)
	// Cover income, legacy dates, old paid_at, actor override and fallback.
	execTestSQL(t, writer, `UPDATE transactions SET type='INCOME', amount=4567, scheduled_payment_date_civil=NULL, scheduled_payment_created_by=NULL,paid_at=123 WHERE id='B1'`)
	execTestSQL(t, writer, `UPDATE transactions SET scheduled_payment_created_by='user-B' WHERE id='A1'`)
	count, err := RunDueScheduledPayments(n24Context(t), db, n24Now())
	if err != nil || count != 4 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	state := n24FinancialState(t, writer)
	if !reflect.DeepEqual(state["balances"], [][]any{{"bank-A", "A", int64(100000), int64(75310)}, {"bank-B", "B", int64(100000), int64(92222)}}) {
		t.Fatalf("financial balances=%v", state["balances"])
	}
	if len(state["history"]) != 4 || len(state["notifications"]) != 4 {
		t.Fatal("history/notification count")
	}
	t.Logf("N24_FINANCIAL_STATE=%#v", state)
	if count, err := RunDueScheduledPayments(n24Context(t), db, n24Now()); err != nil || count != 0 {
		t.Fatalf("idempotent count=%d err=%v", count, err)
	}
	if !reflect.DeepEqual(state, n24FinancialState(t, writer)) {
		t.Fatal("rerun duplicated effects")
	}
}
func TestTenantScheduledPaymentsN24QueryPlan(t *testing.T) {
	query, args := n24ScopedSQL(t)
	db, writer, _ := n24DB(t)
	_ = db
	n24SeedInterleaved(t, writer)
	plan := tenantScopeRows(t, writer, "EXPLAIN QUERY PLAN "+query, args...)
	hasT, hasA, hasIP := false, false, false
	for _, r := range plan {
		detail := fmt.Sprint(r[3])
		t.Log(detail)
		if strings.Contains(detail, "SEARCH t USING") && strings.Contains(detail, "workspace_id=?") {
			hasT = true
		}
		if strings.Contains(detail, "SEARCH a USING") {
			hasA = true
		}
		if strings.Contains(detail, "SEARCH ip USING") {
			hasIP = true
		}
		if strings.HasPrefix(detail, "SCAN ") && !strings.Contains(detail, "USING") {
			t.Fatalf("avoidable full scan: %s", detail)
		}
	}
	if !hasT || !hasA || !hasIP || !strings.Contains(query, "a.workspace_id = t.workspace_id") {
		t.Fatalf("missing scoped indexed access: %v", plan)
	}
}

// Captured verbatim from parent 336a6e7a discovery SQL.
const n24ParentDiscoverySQL = `
		SELECT t.id, t.workspace_id, t.user_id, t.scheduled_payment_created_by, t.type, t.amount,
		       t.account_id, a.type, t.description, t.scheduled_payment_at, t.scheduled_payment_date_civil, t.paid_at
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.status = 'pending'
		  AND t.scheduled_payment_at IS NOT NULL
		  AND t.scheduled_payment_executed_at IS NULL
		  AND t.scheduled_payment_at < ?
		  AND t.type IN (?, ?)
		  AND t.destination_account_id IS NULL
		  AND t.invoice_id IS NULL
		  AND a.type != ?
		  AND NOT EXISTS (
		      SELECT 1
		      FROM invoice_payments ip
		      WHERE (ip.transaction_id = t.id OR ip.reversal_transaction_id = t.id)
		        AND ip.workspace_id = t.workspace_id
		  )
		ORDER BY t.scheduled_payment_at ASC, t.created_at ASC
	`

func TestTenantScheduledPaymentsN24CrossTenantCreatedAtPrecedence(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			db, writer, p := n24DB(t)
			n24SeedInterleaved(t, writer)
			// All candidates share scheduled_at. B precedes A solely by created_at,
			// including when enumeration visits A first; there is no workspace key.
			execTestSQL(t, writer, `UPDATE transactions SET scheduled_payment_at=?,created_at=CASE id WHEN 'B2' THEN 0 WHEN 'A1' THEN 1 WHEN 'B1' THEN 2 ELSE 3 END`, time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC).Unix())
			p.reverseWorkspaces = reverse
			rows, err := queryDueScheduledPayments(n24Context(t), db, n24Now())
			want := []string{"B2", "A1", "B1", "A2"}
			if err != nil || !reflect.DeepEqual(scheduledPaymentRowIDs(rows), want) {
				t.Fatalf("created_at precedence=%v err=%v", scheduledPaymentRowIDs(rows), err)
			}
			parent := tenantScopeRows(t, writer, n24ParentDiscoverySQL, appNextDayStartUnix(n24Now()), "INCOME", "EXPENSE", "CREDIT_CARD")
			for i, r := range parent {
				if r[0] != want[i] {
					t.Fatalf("parent order=%v", parent)
				}
			}
		})
	}
}
