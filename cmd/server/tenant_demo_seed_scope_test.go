package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/database"
	"modernc.org/sqlite"
)

// Observe the real SQLite execution/result boundary. Faults replace only the
// reported result (or Exec error), never the production seed SQL or transaction.
type demoSeedProbe struct {
	writes                     []*demoSeedWrite
	queries                    []string
	begins, commits, rollbacks int
	faultTable, faultMode      string
}

type demoSeedWrite struct {
	table, query string
	args         []any
	rows         int64
	checked      bool
}

type demoSeedDriver struct{ probe *demoSeedProbe }
type demoSeedConn struct {
	driver.Conn
	probe *demoSeedProbe
}
type demoSeedTx struct {
	driver.Tx
	probe *demoSeedProbe
}
type demoSeedResult struct {
	driver.Result
	write *demoSeedWrite
	mode  string
}

var demoSeedDriverID atomic.Uint64
var demoSeedResultError = errors.New("test RowsAffected failure")
var demoSeedExecError = errors.New("test Exec failure")

func (d demoSeedDriver) Open(name string) (driver.Conn, error) {
	c, err := (&sqlite.Driver{}).Open(name)
	if err != nil {
		return nil, err
	}
	return &demoSeedConn{Conn: c, probe: d.probe}, nil
}

func (c *demoSeedConn) Begin() (driver.Tx, error) {
	tx, err := c.Conn.Begin()
	if err != nil {
		return nil, err
	}
	c.probe.begins++
	return &demoSeedTx{Tx: tx, probe: c.probe}, nil
}

func (tx *demoSeedTx) Commit() error {
	tx.probe.commits++
	return tx.Tx.Commit()
}

func (tx *demoSeedTx) Rollback() error {
	tx.probe.rollbacks++
	return tx.Tx.Rollback()
}

func (c *demoSeedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.probe.queries = append(c.probe.queries, query)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c *demoSeedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	table := ""
	if strings.Contains(query, "ON CONFLICT(id) DO UPDATE SET") {
		for _, candidate := range []string{"accounts", "categories", "contacts", "transactions"} {
			if strings.Contains(query, "INSERT INTO "+candidate+" ") {
				table = candidate
				break
			}
		}
	}
	if table == c.probe.faultTable && c.probe.faultMode == "exec-error" {
		return nil, demoSeedExecError
	}
	result, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
	if err != nil || table == "" {
		return result, err
	}
	write := &demoSeedWrite{table: table, query: query}
	for _, arg := range args {
		write.args = append(write.args, arg.Value)
	}
	c.probe.writes = append(c.probe.writes, write)
	mode := ""
	if table == c.probe.faultTable {
		mode = c.probe.faultMode
	}
	return &demoSeedResult{Result: result, write: write, mode: mode}, nil
}

func (r *demoSeedResult) RowsAffected() (int64, error) {
	r.write.checked = true
	rows, err := r.Result.RowsAffected()
	r.write.rows = rows
	if err != nil {
		return rows, err
	}
	switch r.mode {
	case "zero":
		return 0, nil
	case "multiple":
		return 2, nil
	case "rows-error":
		return 0, demoSeedResultError
	default:
		return rows, nil
	}
}

func openTenantDemoSeedDB(t *testing.T) (*sql.DB, *demoSeedProbe) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "seed.sqlite")
	// Initialize the real schema with the existing dormant owner. Never Activate:
	// this fixture observes the seed alone, without backup/pruning/monthly jobs.
	owner, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	base := owner.DB
	_, err = serverFixtureExec(t, base, `
		INSERT INTO users (id, name, email, password_hash, status, created_at, updated_at)
		VALUES ('seed-user', 'Seed User', 'seed@example.test', 'hash', 'active', 1, 1),
		       ('foreign-user', 'Foreign Owner Sentinel', 'foreign@example.test', 'hash', 'active', 1, 1);
		INSERT INTO workspaces (id, name, type, created_at, updated_at)
		VALUES ('seed-ws', 'Seed Workspace', 'business', 1, 1),
		       ('foreign-ws', 'Foreign Workspace Sentinel', 'business', 1, 1);
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('seed-ws', 'seed-user', 'ADMIN', 1), ('foreign-ws', 'foreign-user', 'ADMIN', 1)
	`)
	closeErr := owner.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("initialize fixture: %v; close: %v", err, closeErr)
	}
	probe := &demoSeedProbe{}
	name := fmt.Sprintf("tenant-demo-seed-%d", demoSeedDriverID.Add(1))
	sql.Register(name, demoSeedDriver{probe: probe})
	db, err := sql.Open(name, path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db, probe
}

func demoSeedExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("fixture Exec: %v", err)
	}
}

// Snapshot every persisted column in every application table, including tables
// with composite primary keys. No workspace filter or timestamp normalization.
func demoSeedSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	snapshot := map[string][]map[string]any{}
	for _, table := range tables {
		snapshot[table] = demoSeedReadRows(t, db, `SELECT * FROM "`+table+`"`)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func demoSeedReadRows(t *testing.T, db *sql.DB, query string, args ...any) []map[string]any {
	t.Helper()
	// Persisted rows are sorted by all columns, rather than assuming an id column.
	meta, err := db.Query(query+" LIMIT 0", args...)
	if err != nil {
		t.Fatal(err)
	}
	cols, err := meta.Columns()
	_ = meta.Close()
	if err != nil {
		t.Fatal(err)
	}
	quoted := make([]string, len(cols))
	for i, col := range cols {
		quoted[i] = `"` + col + `"`
	}
	rows, err := db.Query(query+" ORDER BY "+strings.Join(quoted, ","), args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		row := map[string]any{}
		for i, col := range cols {
			if blob, ok := values[i].([]byte); ok {
				values[i] = bytes.Clone(blob)
			}
			row[col] = values[i]
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertDemoSeedWrites(t *testing.T, p *demoSeedProbe, counts map[string]int, foreign bool) {
	t.Helper()
	got := map[string]int{}
	for i, w := range p.writes {
		got[w.table]++
		wantRows := int64(1)
		if foreign && i == len(p.writes)-1 {
			wantRows = 0
		}
		if !w.checked || w.rows != wantRows {
			t.Fatalf("%s write %d: checked=%v rows=%d, want %d", w.table, i, w.checked, w.rows, wantRows)
		}
	}
	if !reflect.DeepEqual(got, counts) {
		t.Fatalf("write counts=%v, want %v", got, counts)
	}
	if p.begins != 1 || p.commits != 1-demoSeedBoolInt(foreign) || p.rollbacks != demoSeedBoolInt(foreign) {
		t.Fatalf("transaction boundary: begin=%d commit=%d rollback=%d", p.begins, p.commits, p.rollbacks)
	}
	if len(p.queries) != 2 || !strings.Contains(p.queries[0], "FROM workspaces WHERE id = ?") || !strings.Contains(p.queries[1], "WHERE workspace_id = ? AND type IN") {
		t.Fatalf("unexpected seed queries (ownership lookup/N+1): %v", p.queries)
	}
}

func demoSeedBoolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestTenantDemoSeedNewAndSameTenantReexecution(t *testing.T) {
	db, p := openTenantDemoSeedDB(t)
	for run := 0; run < 2; run++ {
		*p = demoSeedProbe{}
		if err := seedDemoB2B(db, "seed-ws", "seed-user"); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		counts := map[string]int{"categories": 6, "contacts": 6, "transactions": 12}
		if run == 0 {
			counts["accounts"] = 1
		}
		assertDemoSeedWrites(t, p, counts, false)
		for table, count := range map[string]int{"accounts": 1, "categories": 6, "contacts": 6, "transactions": 12} {
			rows := demoSeedReadRows(t, db, "SELECT * FROM "+table+" WHERE workspace_id = ?", "seed-ws")
			if len(rows) != count {
				t.Fatalf("%s persisted rows=%d, want %d", table, len(rows), count)
			}
		}
	}
}

func TestTenantDemoSeedOwnUpdatesPreserveSetContract(t *testing.T) {
	db, p := openTenantDemoSeedDB(t)
	if err := seedDemoB2B(db, "seed-ws", "seed-user"); err != nil {
		t.Fatal(err)
	}
	first := map[string]*demoSeedWrite{}
	golden := map[string]map[string]any{}
	for _, w := range p.writes {
		if first[w.table] == nil {
			first[w.table] = w
			golden[w.table] = demoSeedReadRows(t, db, "SELECT * FROM "+w.table+" WHERE id = ?", w.args[0])[0]
		}
	}
	// WALLET is an existing valid account type that forces the unchanged account
	// fallback to exercise N27's own-ID DO UPDATE; eligible accounts are reused.
	demoSeedExec(t, db, `UPDATE accounts SET name='old', type='WALLET', color='preserved', initial_balance=123, current_balance=456, updated_at=-1 WHERE id=?`, first["accounts"].args[0])
	demoSeedExec(t, db, `UPDATE categories SET name='old', type='EXPENSE', macro_group='old', icon='preserved', color='preserved', is_fixed=1 WHERE id=?`, first["categories"].args[0])
	demoSeedExec(t, db, `UPDATE contacts SET name='old', document='old', type='vendor', email='old', phone='old', custom_client_id='preserved' WHERE id=?`, first["contacts"].args[0])
	demoSeedExec(t, db, `INSERT INTO accounts (id, workspace_id, name, type) VALUES ('own-alt', 'seed-ws', 'Preserved relationship', 'WALLET')`)
	demoSeedExec(t, db, `UPDATE transactions SET category_id=NULL, type='EXPENSE', amount=77, date=123, date_civil='2026-01-01', description='old', payment_status='PENDING', status='pending', due_date=123, due_date_civil='2026-01-01', contact_id=NULL, updated_at=-1, account_id='own-alt', notes='preserved', attachment_path='preserved', installment_number=2, total_installments=3, paid_at=456 WHERE id=?`, first["transactions"].args[0])
	before := map[string]map[string]any{}
	for table, w := range first {
		before[table] = demoSeedReadRows(t, db, "SELECT * FROM "+table+" WHERE id = ?", w.args[0])[0]
	}
	*p = demoSeedProbe{}
	if err := seedDemoB2B(db, "seed-ws", "seed-user"); err != nil {
		t.Fatal(err)
	}
	assertDemoSeedWrites(t, p, map[string]int{"accounts": 1, "categories": 6, "contacts": 6, "transactions": 12}, false)
	for table, w := range first {
		after := demoSeedReadRows(t, db, "SELECT * FROM "+table+" WHERE id = ?", w.args[0])[0]
		set := map[string]bool{}
		for _, match := range regexp.MustCompile(`(\w+) = excluded\.\w+`).FindAllStringSubmatch(strings.Split(w.query, "WHERE "+table+".workspace_id")[0], -1) {
			set[match[1]] = true
		}
		if set["workspace_id"] || after["workspace_id"] != "seed-ws" {
			t.Fatalf("%s ownership changed", table)
		}
		for col, got := range after {
			want := before[table][col]
			if set[col] {
				want = golden[table][col]
				if col == "updated_at" {
					if got.(int64) <= 0 {
						t.Fatalf("%s updated_at not applied: %v", table, got)
					}
					continue
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s.%s=%v, want %v (in SET=%v)", table, col, got, want, set[col])
			}
		}
	}
}

var demoSeedCollisionSites = []struct {
	table, key string
	counts     map[string]int
}{
	{"accounts", "demo-b2b-account-main", map[string]int{"accounts": 1}},
	{"categories", "cat-investments-other", map[string]int{"accounts": 1, "categories": 6}},
	{"contacts", "vendor-metro", map[string]int{"accounts": 1, "categories": 6, "contacts": 6}},
	{"transactions", "tx-2026-06-expense-invest", map[string]int{"accounts": 1, "categories": 6, "contacts": 6, "transactions": 12}},
}

func insertDemoSeedForeignTarget(t *testing.T, db *sql.DB, table, key string) string {
	t.Helper()
	id := seedStableID("seed-ws", key)
	switch table {
	case "accounts":
		demoSeedExec(t, db, `INSERT INTO accounts (id,workspace_id,name,type,initial_balance,current_balance,created_at,updated_at) VALUES (?,'foreign-ws','Foreign Account Sentinel','CHECKING',123,456,1,2)`, id)
	case "categories":
		demoSeedExec(t, db, `INSERT INTO categories (id,workspace_id,name,type,macro_group,icon,color,created_at) VALUES (?,'foreign-ws','Foreign Category Sentinel','INCOME','foreign','foreign','foreign',1)`, id)
	case "contacts":
		demoSeedExec(t, db, `INSERT INTO contacts (id,workspace_id,name,document,type,email,phone,created_at) VALUES (?,'foreign-ws','Foreign Contact Sentinel','foreign','client','foreign','foreign',1)`, id)
	case "transactions":
		demoSeedExec(t, db, `INSERT INTO accounts (id,workspace_id,name,type) VALUES ('foreign-account','foreign-ws','Foreign Account Sentinel','CHECKING')`)
		demoSeedExec(t, db, `INSERT INTO transactions (id,workspace_id,user_id,account_id,type,amount,date,date_civil,description,notes,status,payment_status,paid_at,created_at,updated_at) VALUES (?,'foreign-ws','foreign-user','foreign-account','INCOME',987654,123,'2026-01-01','Foreign Transaction Sentinel','foreign','paid','PAID',123,1,2)`, id)
	}
	return id
}

func TestTenantDemoSeedForeignCollisionRollsBackEveryPosition(t *testing.T) {
	for _, site := range demoSeedCollisionSites {
		t.Run(site.table, func(t *testing.T) {
			db, p := openTenantDemoSeedDB(t)
			id := insertDemoSeedForeignTarget(t, db, site.table, site.key)
			before := demoSeedSnapshot(t, db)
			*p = demoSeedProbe{}
			err := seedDemoB2B(db, "seed-ws", "seed-user")
			if err == nil || err.Error() != "seed demo B2B: cardinalidade inválida em "+site.table {
				t.Fatalf("foreign collision error=%v", err)
			}
			assertDemoSeedWrites(t, p, site.counts, true)
			if after := demoSeedSnapshot(t, db); before != after {
				t.Fatal("pre-seed snapshot != post-failure snapshot; foreign mutation or partial writes")
			}
			target := demoSeedReadRows(t, db, "SELECT * FROM "+site.table+" WHERE id = ?", id)
			if len(target) != 1 || target[0]["workspace_id"] != "foreign-ws" {
				t.Fatal("foreign workspace ownership changed")
			}
		})
	}
}

func TestTenantDemoSeedCardinalityAndExecErrorsFailClosed(t *testing.T) {
	for _, site := range demoSeedCollisionSites {
		for _, mode := range []string{"zero", "multiple", "rows-error", "exec-error"} {
			t.Run(site.table+"/"+mode, func(t *testing.T) {
				db, p := openTenantDemoSeedDB(t)
				before := demoSeedSnapshot(t, db)
				*p = demoSeedProbe{faultTable: site.table, faultMode: mode}
				err := seedDemoB2B(db, "seed-ws", "seed-user")
				if err == nil {
					t.Fatal("seed accepted invalid result")
				}
				if mode == "rows-error" && !errors.Is(err, demoSeedResultError) {
					t.Fatalf("RowsAffected error not propagated: %v", err)
				}
				if mode == "exec-error" && !errors.Is(err, demoSeedExecError) {
					t.Fatalf("Exec error not propagated: %v", err)
				}
				if (mode == "zero" || mode == "multiple") && err.Error() != "seed demo B2B: cardinalidade inválida em "+site.table {
					t.Fatalf("cardinality error=%v", err)
				}
				if p.begins != 1 || p.commits != 0 || p.rollbacks != 1 {
					t.Fatalf("failed seed transaction boundary: %+v", p)
				}
				wantPrevious := map[string]int{"accounts": 0, "categories": 1, "contacts": 7, "transactions": 13}[site.table]
				wantWrites := wantPrevious
				if mode != "exec-error" {
					wantWrites++
				}
				if len(p.writes) != wantWrites {
					t.Fatalf("seed continued after failure: writes=%d, want %d", len(p.writes), wantWrites)
				}
				for _, w := range p.writes {
					if !w.checked || w.rows != 1 {
						t.Fatalf("real SQLite result not checked: %+v", w)
					}
				}
				if after := demoSeedSnapshot(t, db); before != after {
					t.Fatal("failed seed left partial writes")
				}
			})
		}
	}
}

func TestTenantDemoSeedHTTPForeignCollisionCannotSucceed(t *testing.T) {
	t.Setenv("APP_DEBUG", "true")
	for _, site := range demoSeedCollisionSites {
		t.Run(site.table, func(t *testing.T) {
			db, _ := openTenantDemoSeedDB(t)
			insertDemoSeedForeignTarget(t, db, site.table, site.key)
			service := auth.NewService(db)
			token, _, err := service.CreateSession("seed-user", "seed-ws", time.Hour, false)
			if err != nil {
				t.Fatal(err)
			}
			signer := &csrfSigner{secret: bytes.Repeat([]byte{19}, 32), ttl: time.Hour}
			handler := seedDemoB2BRoute(db, service, signer, newMemoryRateLimiter(), rateLimitPolicy{Limit: 3, Window: time.Hour})
			req := httptest.NewRequest(http.MethodPost, "/admin/seed-demo-b2b", nil)
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
			addAdminUsersRBACCSRF(t, req, signer)
			before := map[string][]map[string]any{}
			for _, table := range []string{"accounts", "categories", "contacts", "transactions"} {
				before[table] = demoSeedReadRows(t, db, "SELECT * FROM "+table)
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			wantBody := "erro ao gerar seed demo B2B: seed demo B2B: cardinalidade inválida em " + site.table + "\n"
			if rr.Code != http.StatusUnprocessableEntity || rr.Body.String() != wantBody {
				t.Fatalf("response=%d %q; want 422 generic structural error", rr.Code, rr.Body.String())
			}
			for table, want := range before {
				if got := demoSeedReadRows(t, db, "SELECT * FROM "+table); !reflect.DeepEqual(got, want) {
					t.Fatalf("HTTP failure changed %s", table)
				}
			}
		})
	}
}

func TestTenantDemoSeedProductionUpsertsUsePrimaryKeyProbe(t *testing.T) {
	db, p := openTenantDemoSeedDB(t)
	if err := seedDemoB2B(db, "seed-ws", "seed-user"); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, write := range p.writes {
		if seen[write.table] {
			continue
		}
		seen[write.table] = true
		if !strings.Contains(write.query, "WHERE "+write.table+".workspace_id = excluded.workspace_id") {
			t.Fatalf("%s production ownership guard missing", write.table)
		}
		var targetRoot int
		if err := db.QueryRow(`SELECT rootpage FROM sqlite_master WHERE type='table' AND name=?`, write.table).Scan(&targetRoot); err != nil {
			t.Fatal(err)
		}
		rows, err := db.Query("EXPLAIN "+write.query, write.args...)
		if err != nil {
			t.Fatal(err)
		}
		targetCursors := map[int]bool{}
		primaryKeyProbe := false
		for rows.Next() {
			var addr, p1, p2, p3, p5 int
			var opcode string
			var p4, comment sql.NullString
			if err := rows.Scan(&addr, &opcode, &p1, &p2, &p3, &p4, &p5, &comment); err != nil {
				t.Fatal(err)
			}
			// Subsequent address-zero programs are triggers, not the main UPSERT.
			if addr == 0 && len(targetCursors) > 0 {
				break
			}
			if (opcode == "OpenWrite" || opcode == "OpenRead") && p2 == targetRoot && p3 == 0 {
				targetCursors[p1] = true
			}
			if opcode == "NoConflict" {
				primaryKeyProbe = true
			}
			if targetCursors[p1] && (opcode == "Rewind" || opcode == "Next" || opcode == "Prev" || opcode == "Last") {
				t.Fatalf("%s UPSERT scans target table: %s cursor %d", write.table, opcode, p1)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
		if !primaryKeyProbe || len(targetCursors) == 0 {
			t.Fatalf("%s missing indexed conflict probe or target table cursor", write.table)
		}
		t.Logf("%s: indexed conflict probe; no target table scan", write.table)
	}
	if len(seen) != 4 {
		t.Fatalf("EXPLAIN exercised %d sites, want 4", len(seen))
	}
}
