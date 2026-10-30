package repository

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

	"github.com/contabase-app/contabase/internal/database"
	sqliteDriver "modernc.org/sqlite"
)

// Instrument only this DB, delegating successful statements to real SQLite.
// Hooks inject discovery/driver errors and committed changes between snapshots.
type n25Statement struct {
	query string
	args  []driver.NamedValue
	tx    int
	rows  [][]driver.Value
}
type n25Probe struct {
	statements                                       []*n25Statement
	activeRows, activeTx, begins, commits, rollbacks int
	readOnly                                         []bool
	reverse                                          bool
	before                                           func(string, []driver.NamedValue) error
	fault                                            func(string, []driver.NamedValue) string
	afterClose                                       func(string)
	afterSnapshot                                    func()
	resultFault                                      string
	resultCalls                                      int
	failCommit                                       bool
	craftedItem                                      *OrphanCardInfo
	affected                                         []int64
}
type n25Connector struct {
	dsn string
	p   *n25Probe
}

func (c n25Connector) Driver() driver.Driver { return &sqliteDriver.Driver{} }
func (c n25Connector) Connect(context.Context) (driver.Conn, error) {
	cx, err := c.Driver().Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &n25Conn{Conn: cx, p: c.p}, nil
}

type n25Conn struct {
	driver.Conn
	p *n25Probe
}

func (c *n25Conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c.p.activeRows != 0 || c.p.activeTx != 0 {
		return nil, errors.New("overlapping snapshot/rows")
	}
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	c.p.begins++
	c.p.activeTx = c.p.begins
	c.p.readOnly = append(c.p.readOnly, opts.ReadOnly)
	return &n25Tx{Tx: tx, p: c.p, id: c.p.begins}, nil
}

type n25Tx struct {
	driver.Tx
	p  *n25Probe
	id int
}

func (tx *n25Tx) Commit() error {
	if tx.p.activeRows != 0 {
		return errors.New("commit with open rows")
	}
	if tx.id == 2 && tx.p.failCommit {
		err := tx.Tx.Rollback()
		tx.p.activeTx = 0
		tx.p.rollbacks++
		if err != nil {
			return err
		}
		return errN25Probe
	}
	err := tx.Tx.Commit()
	tx.p.activeTx = 0
	tx.p.commits++
	if err == nil && tx.id == 1 && tx.p.afterSnapshot != nil {
		tx.p.afterSnapshot()
	}
	return err
}
func (tx *n25Tx) Rollback() error {
	err := tx.Tx.Rollback()
	tx.p.activeTx = 0
	tx.p.rollbacks++
	return err
}
func (c *n25Conn) record(q string, args []driver.NamedValue) (*n25Statement, error) {
	if c.p.activeRows != 0 {
		return nil, errors.New("overlapping rows")
	}
	s := &n25Statement{query: q, args: append([]driver.NamedValue(nil), args...), tx: c.p.activeTx}
	c.p.statements = append(c.p.statements, s)
	if c.p.before != nil {
		if err := c.p.before(q, args); err != nil {
			return nil, err
		}
	}
	return s, nil
}
func (c *n25Conn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	s, err := c.record(q, args)
	if err != nil {
		return nil, err
	}
	actual := q
	if c.p.reverse && q == "SELECT id FROM workspaces ORDER BY id" {
		actual += " DESC"
	}
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, actual, args)
	if err != nil {
		return nil, err
	}
	fault := ""
	if c.p.fault != nil {
		fault = c.p.fault(q, args)
	}
	c.p.activeRows++
	return &n25Rows{Rows: rows, p: c.p, s: s, fault: fault}, nil
}
func (c *n25Conn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if _, err := c.record(q, args); err != nil {
		return nil, err
	}
	res, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
	if err != nil {
		return nil, err
	}
	return n25Result{Result: res, p: c.p}, nil
}

type n25Result struct {
	driver.Result
	p *n25Probe
}

func (r n25Result) RowsAffected() (int64, error) {
	r.p.resultCalls++
	if r.p.resultCalls == 4 {
		switch r.p.resultFault {
		case "zero":
			return 0, nil
		case "many":
			return 2, nil
		case "error":
			return 0, errN25Probe
		}
	}
	n, err := r.Result.RowsAffected()
	if err == nil {
		r.p.affected = append(r.p.affected, n)
	}
	return n, err
}

type n25Rows struct {
	driver.Rows
	p            *n25Probe
	s            *n25Statement
	fault        string
	read, closed bool
}

func (r *n25Rows) Next(values []driver.Value) error {
	if r.fault == "next" && r.read {
		return errN25Probe
	}
	err := r.Rows.Next(values)
	if err == nil {
		r.read = true
		if r.fault == "scan" {
			values[0] = nil
		}
		r.s.rows = append(r.s.rows, append([]driver.Value(nil), values...))
		// Test-only injection of an inconsistent internal repair item. Preserve
		// the real scoped SQLite rows above; this is not a legitimate N25 output.
		if item := r.p.craftedItem; item != nil && strings.Contains(r.s.query, "FROM accounts a") && values[0] == item.AccountID {
			values[1], values[2] = item.Name, item.WorkspaceID
		}
	}
	return err
}
func (r *n25Rows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	err := r.Rows.Close()
	r.p.activeRows--
	if r.p.afterClose != nil {
		r.p.afterClose(r.s.query)
	}
	if err == nil && r.fault == "close" {
		return errN25Probe
	}
	return err
}

var errN25Probe = errors.New("N25/N26 injected error")

func n25DB(t *testing.T) (*sql.DB, *sql.DB, *n25Probe) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cards.db")
	runtimeDBs, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtimeDBs.Close() })
	p := &n25Probe{}
	db := sql.OpenDB(n25Connector{dsn: "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", p: p})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db, runtimeDBs.DB, p
}
func n25Exec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}
func n25RowsOf(t *testing.T, db *sql.DB, q string, args ...any) [][]any {
	t.Helper()
	rows, err := db.Query(q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out [][]any
	for rows.Next() {
		v := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range v {
			ptrs[i] = &v[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
func n25Seed(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, ws := range []string{"A", "B"} {
		n25Exec(t, db, `INSERT INTO workspaces(id,name) VALUES(?,?)`, ws, ws)
		// Deliberately reverse insertion and name order; archived cards stay eligible.
		for _, suffix := range []string{"2", "1", "valid", "checking"} {
			typ, name := "CREDIT_CARD", "Private-"+ws+"-"+suffix
			if suffix == "checking" {
				typ = "CHECKING"
			}
			n25Exec(t, db, `INSERT INTO accounts(id,workspace_id,name,type,initial_balance,current_balance,created_at,updated_at) VALUES(?,?,?,?,100,100,111,111)`, ws+suffix, ws, name, typ)
		}
		n25Exec(t, db, `UPDATE accounts SET archived_at=123 WHERE id=?`, ws+"2")
		n25Exec(t, db, `INSERT INTO credit_cards(id,account_id,closing_day,due_day,credit_limit) VALUES(?,?,25,5,500000)`, "valid-"+ws, ws+"valid")
	}
}
func n25Cards(t *testing.T, db *sql.DB) [][]any {
	return n25RowsOf(t, db, `SELECT * FROM credit_cards ORDER BY account_id`)
}
func n25IDs(r RepairOrphanCardsResult) []string {
	var ids []string
	for _, item := range r.Orphans {
		ids = append(ids, item.AccountID)
	}
	return ids
}

const n25ParentSQL = `
		SELECT a.id, a.name, a.workspace_id
		FROM accounts a
		LEFT JOIN credit_cards cc ON cc.account_id = a.id
		WHERE a.type = 'CREDIT_CARD' AND cc.account_id IS NULL
		ORDER BY a.workspace_id, a.name
	`

func TestTenantOrphanCardsN25N26ScopeOrderAndCount(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			db, writer, p := n25DB(t)
			n25Seed(t, writer)
			p.reverse = reverse
			before := n25Cards(t, writer)
			r, err := NewConfigRepository(db).RepairOrphanCreditCards(true)
			want := []string{"A1", "A2", "B1", "B2"}
			if err != nil || r.Diagnosed != 4 || r.Repaired != 0 || !reflect.DeepEqual(n25IDs(r), want) {
				t.Fatalf("dry=%+v err=%v", r, err)
			}
			if !reflect.DeepEqual(before, n25Cards(t, writer)) {
				t.Fatal("dry-run repair write")
			}
			if len(p.statements) != 3 || p.begins != 1 || p.commits != 1 || !reflect.DeepEqual(p.readOnly, []bool{true}) {
				t.Fatalf("discovery=%+v", p)
			}
			if p.statements[0].query != "SELECT id FROM workspaces ORDER BY id" || len(p.statements[0].args) != 0 {
				t.Fatal("control-plane must return only IDs")
			}
			for _, s := range p.statements {
				if s.tx != 1 {
					t.Fatal("escaped single snapshot")
				}
			}
			for _, s := range p.statements[1:] {
				if len(s.args) != 1 {
					t.Fatal("workspace bind missing")
				}
				ws := s.args[0].Value.(string)
				if len(s.rows) != 2 {
					t.Fatalf("scoped rows=%v", s.rows)
				}
				for _, row := range s.rows {
					if row[2] != ws || !strings.HasPrefix(row[0].(string), ws) || !strings.HasPrefix(row[1].(string), "Private-"+ws+"-") {
						t.Fatalf("foreign data=%v", row)
					}
				}
				old := strings.Replace(s.query, "a.workspace_id = ? AND ", "", 1)
				if strings.Join(strings.Fields(old), " ") != strings.Join(strings.Fields(n25ParentSQL), " ") {
					t.Fatal("orphan definition/order changed")
				}
			}
			parent := n25RowsOf(t, writer, n25ParentSQL)
			for i, row := range parent {
				if row[0] != want[i] {
					t.Fatalf("parent=%v", parent)
				}
			}
			*p = n25Probe{reverse: reverse}
			r, err = NewConfigRepository(db).RepairOrphanCreditCards(false)
			if err != nil || r.Repaired != 4 || !reflect.DeepEqual(n25IDs(r), want) {
				t.Fatalf("repair=%+v err=%v", r, err)
			}
			if len(p.statements) != 7 || p.begins != 2 || p.commits != 2 || p.activeRows != 0 || p.activeTx != 0 || p.resultCalls != 4 || !reflect.DeepEqual(p.readOnly, []bool{true, false}) {
				t.Fatalf("repair boundaries/count=%+v", p)
			}
			for i, s := range p.statements[3:] {
				if s.tx != 2 || len(s.args) != 3 || s.args[1].Value != want[i] || s.args[2].Value != string(want[i][0]) || !strings.Contains(s.query, "owner.workspace_id = ?") || !strings.Contains(s.query, "owner.type = 'CREDIT_CARD'") || !strings.Contains(s.query, "SELECT ?, owner.id, 20, 10, 0") {
					t.Fatalf("unsafe mutation=%+v", s)
				}
			}
			r, err = NewConfigRepository(db).RepairOrphanCreditCards(false)
			if err != nil || r.Diagnosed != 0 || r.Repaired != 0 {
				t.Fatalf("rerun=%+v err=%v", r, err)
			}
			t.Log("W=2 K=4; dry SELECTs=3; repair statements=7; one repair transaction; RowsAffected=1/item; N+1 per orphan=0")
		})
	}
}
func TestTenantOrphanCardsN25N26ZeroAndFanout(t *testing.T) {
	for _, w := range []int{0, 2, 10} {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			db, writer, p := n25DB(t)
			for i := 0; i < w; i++ {
				n25Exec(t, writer, `INSERT INTO workspaces(id,name) VALUES(?,?)`, fmt.Sprint(i), fmt.Sprint(i))
			}
			r, err := NewConfigRepository(db).RepairOrphanCreditCards(false)
			if err != nil || r.Diagnosed != 0 || r.Repaired != 0 || len(p.statements) != 1+w || p.begins != 1 || p.activeRows != 0 {
				t.Fatalf("W=%d r=%+v probe=%+v err=%v", w, r, p, err)
			}
			for _, s := range p.statements {
				if !strings.HasPrefix(strings.TrimSpace(s.query), "SELECT") {
					t.Fatal("empty repair mutation")
				}
			}
			t.Logf("W=%d K=0 SELECTs=%d mutations=0", w, 1+w)
		})
	}
}
func TestTenantOrphanCardsN25N26DiagnosisErrors(t *testing.T) {
	for _, phase := range []string{"enumeration", "A-query", "B-query", "enumeration-scan", "enumeration-next", "enumeration-close", "A-scan", "A-next", "A-close", "B-scan", "B-next", "B-close"} {
		t.Run(phase, func(t *testing.T) {
			db, writer, p := n25DB(t)
			n25Seed(t, writer)
			before := n25Cards(t, writer)
			target := func(q string, args []driver.NamedValue) bool {
				if strings.HasPrefix(phase, "enumeration") {
					return q == "SELECT id FROM workspaces ORDER BY id"
				}
				return strings.Contains(q, "FROM accounts a") && args[0].Value == phase[:1]
			}
			if phase == "enumeration" || strings.HasSuffix(phase, "query") {
				p.before = func(q string, args []driver.NamedValue) error {
					if target(q, args) {
						return errN25Probe
					}
					return nil
				}
			} else {
				p.fault = func(q string, args []driver.NamedValue) string {
					if target(q, args) {
						return phase[strings.Index(phase, "-")+1:]
					}
					return ""
				}
			}
			r, err := NewConfigRepository(db).RepairOrphanCreditCards(false)
			if err == nil || r.Repaired != 0 || r.Diagnosed != 0 || len(r.Orphans) != 0 || p.begins != 1 || p.rollbacks != 1 || p.activeRows != 0 || p.activeTx != 0 {
				t.Fatalf("r=%+v err=%v probe=%+v", r, err, p)
			}
			if !reflect.DeepEqual(before, n25Cards(t, writer)) {
				t.Fatal("diagnosis error repaired cards")
			}
			for _, s := range p.statements {
				if !strings.HasPrefix(strings.TrimSpace(s.query), "SELECT") {
					t.Fatal("diagnosis error reached mutation")
				}
			}
			if len(p.statements) > 3 {
				t.Fatal("diagnosis retried")
			}
		})
	}
}
func TestTenantOrphanCardsN25N26OwnerLossRollback(t *testing.T) {
	for _, mode := range []string{"missing", "foreign", "type"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "foreign" {
				n26AssertForeignOwnerRollback(t)
				return
			}
			db, writer, p := n25DB(t)
			n25Seed(t, writer)
			before := n25Cards(t, writer)
			p.afterSnapshot = func() {
				switch mode {
				case "missing":
					n25Exec(t, writer, `DELETE FROM accounts WHERE id='B2'`)
				case "type":
					n25Exec(t, writer, `UPDATE accounts SET type='CHECKING' WHERE id='B2'`)
				}
			}
			r, err := NewConfigRepository(db).RepairOrphanCreditCards(false)
			if err == nil || !strings.Contains(err.Error(), "obtidos 0") || r.Diagnosed != 4 || r.Repaired != 0 || p.resultCalls != 4 || p.begins != 2 || p.rollbacks != 1 || p.commits != 1 {
				t.Fatalf("r=%+v err=%v p=%+v", r, err, p)
			}
			if !reflect.DeepEqual(before, n25Cards(t, writer)) {
				t.Fatal("owner loss left partial repair")
			}
			if len(p.statements) != 7 {
				t.Fatal("unexpected owner query/retry")
			}
		})
	}
}

// N26 defense-in-depth: feed a crafted internal item to the inline production
// repair through the test driver. No ownership change in SQLite, copied repair
// SQL, altered guard, or production hook is involved. Reachable N25 behavior is
// tested independently by ScopeOrderAndCount with unmodified SQLite results.
func n26AssertForeignOwnerRollback(t *testing.T) {
	t.Helper()
	db, writer, p := n25DB(t)
	n25Seed(t, writer) // B2 is CREDIT_CARD, originally and permanently in B.
	beforeCards := n25Cards(t, writer)
	beforeOwner := n25RowsOf(t, writer, `SELECT * FROM accounts WHERE id='B2'`)
	owner := n25RowsOf(t, writer, `SELECT workspace_id,type FROM accounts WHERE id='B2'`)
	if !reflect.DeepEqual(owner, [][]any{{"B", "CREDIT_CARD"}}) {
		t.Fatalf("foreign owner was not created in B: %v", owner)
	}
	item := OrphanCardInfo{AccountID: "B2", Name: "Private-B-2", WorkspaceID: "A"}
	p.craftedItem = &item
	r, err := NewConfigRepository(db).RepairOrphanCreditCards(false)
	if err == nil || !strings.Contains(err.Error(), "obtidos 0") || r.Diagnosed != 4 || r.Repaired != 0 {
		t.Fatalf("foreign item outcome=%+v err=%v", r, err)
	}
	if !reflect.DeepEqual(p.affected, []int64{1, 1, 0}) || p.resultCalls != 3 {
		t.Fatalf("expected two legitimate INSERTs then real foreign RowsAffected=0: %v", p.affected)
	}
	if p.begins != 2 || p.commits != 1 || p.rollbacks != 1 || p.activeTx != 0 || p.activeRows != 0 || len(p.statements) != 6 {
		t.Fatalf("global rollback boundaries: %+v", p)
	}
	if !reflect.DeepEqual(n25IDs(r), []string{"A1", "A2", "B2", "B1"}) || r.Orphans[2] != item {
		t.Fatalf("crafted item did not reach production repair: %+v", r.Orphans)
	}
	last := p.statements[5]
	if last.tx != 2 || len(last.args) != 3 || last.args[1].Value != "B2" || last.args[2].Value != "A" || !strings.Contains(last.query, "owner.workspace_id = ?") {
		t.Fatalf("production N26 did not bind crafted ownership: %+v", last)
	}
	for _, s := range p.statements[1:3] {
		ws := s.args[0].Value
		for _, row := range s.rows {
			if row[2] != ws {
				t.Fatalf("real N25 query leaked workspace data: %v", row)
			}
		}
	}
	if !reflect.DeepEqual(beforeCards, n25Cards(t, writer)) || !reflect.DeepEqual(beforeOwner, n25RowsOf(t, writer, `SELECT * FROM accounts WHERE id='B2'`)) {
		t.Fatal("foreign attempt left a repair or changed the foreign account")
	}
	t.Log("FOREIGN FIXTURE=VALID WITHOUT WORKSPACE UPDATE; OWNER=B/CREDIT_CARD; ITEM=A/B2; RowsAffected=[1,1,0]; rollback=TOTAL; partial success=0; repaired=0; foreign account preserved")
}
func TestTenantOrphanCardsN25N26LateFailureAndUniqueRollback(t *testing.T) {
	for _, mode := range []string{"trigger", "unique", "zero", "many", "error", "commit"} {
		t.Run(mode, func(t *testing.T) {
			db, writer, p := n25DB(t)
			n25Seed(t, writer)
			before := n25Cards(t, writer)
			switch mode {
			case "trigger":
				n25AdminExec(t, writer, `CREATE TRIGGER n26_fail BEFORE INSERT ON credit_cards WHEN NEW.account_id='B2' BEGIN SELECT RAISE(ABORT,'N26 late failure'); END`)
			case "unique":
				p.afterSnapshot = func() {
					n25Exec(t, writer, `INSERT INTO credit_cards(id,account_id,closing_day,due_day,credit_limit) VALUES('concurrent','B2',30,15,999)`)
					before = n25Cards(t, writer)
				}
			case "commit":
				p.failCommit = true
			default:
				p.resultFault = mode
			}
			r, err := NewConfigRepository(db).RepairOrphanCreditCards(false)
			if err == nil || r.Repaired != 0 || r.Diagnosed != 4 || p.begins != 2 || p.rollbacks != 1 || p.commits != 1 || p.activeTx != 0 {
				t.Fatalf("r=%+v err=%v p=%+v", r, err, p)
			}
			if mode == "unique" && !strings.Contains(err.Error(), "UNIQUE") {
				t.Fatalf("unique conflict became success: %v", err)
			}
			if mode == "error" && !errors.Is(err, errN25Probe) {
				t.Fatal("RowsAffected error not propagated")
			}
			if !reflect.DeepEqual(before, n25Cards(t, writer)) {
				t.Fatal("late failure left partial repair")
			}
			if len(p.statements) != 7 {
				t.Fatal("extra per-owner query/retry")
			}
		})
	}
}
func TestTenantOrphanCardsN25N26SnapshotCut(t *testing.T) {
	db, writer, p := n25DB(t)
	n25Seed(t, writer)
	inserted := false
	p.afterClose = func(q string) {
		if q != "SELECT id FROM workspaces ORDER BY id" || inserted {
			return
		}
		inserted = true
		n25Exec(t, writer, `INSERT INTO workspaces(id,name) VALUES('C','C')`)
		for _, ws := range []string{"A", "B", "C"} {
			n25Exec(t, writer, `INSERT INTO accounts(id,workspace_id,name,type) VALUES(?,?,?,'CREDIT_CARD')`, ws+"late", ws, "Late-"+ws)
		}
	}
	r, err := NewConfigRepository(db).RepairOrphanCreditCards(false)
	if err != nil || r.Repaired != 4 || !reflect.DeepEqual(n25IDs(r), []string{"A1", "A2", "B1", "B2"}) {
		t.Fatalf("snapshot=%+v err=%v", r, err)
	}
	p.afterClose = nil
	r, err = NewConfigRepository(db).RepairOrphanCreditCards(true)
	if err != nil || !reflect.DeepEqual(n25IDs(r), []string{"Alate", "Blate", "Clate"}) {
		t.Fatalf("next snapshot=%+v err=%v", r, err)
	}
}

// Also run once against the exact parent production file via a temporary overlay.
func TestTenantOrphanCardsN25N26ParentCompatible(t *testing.T) {
	db, writer, _ := n25DB(t)
	n25Seed(t, writer)
	before := n25Cards(t, writer)
	dry, err := NewConfigRepository(db).RepairOrphanCreditCards(true)
	if err != nil || dry.Diagnosed != 4 || dry.Repaired != 0 || !reflect.DeepEqual(n25IDs(dry), []string{"A1", "A2", "B1", "B2"}) || !reflect.DeepEqual(before, n25Cards(t, writer)) {
		t.Fatalf("dry=%+v err=%v", dry, err)
	}
	t.Logf("N25_BASELINE_DRY=%#v", dry)
	r, err := NewConfigRepository(db).RepairOrphanCreditCards(false)
	if err != nil || r.Repaired != 4 || !reflect.DeepEqual(r.Orphans, dry.Orphans) {
		t.Fatalf("r=%+v err=%v", r, err)
	}
	state := n25RowsOf(t, writer, `SELECT account_id,closing_day,due_day,credit_limit,default_payment_account_id FROM credit_cards ORDER BY account_id`)
	t.Logf("N26_BASELINE_REPAIR=%#v STATE=%#v", r, state)
	r, err = NewConfigRepository(db).RepairOrphanCreditCards(false)
	if err != nil || r.Repaired != 0 || r.Diagnosed != 0 {
		t.Fatalf("rerun=%+v err=%v", r, err)
	}
}
func TestTenantOrphanCardsN25N26QueryPlans(t *testing.T) {
	db, writer, p := n25DB(t)
	n25Seed(t, writer)
	if _, err := NewConfigRepository(db).RepairOrphanCreditCards(false); err != nil {
		t.Fatal(err)
	}
	for _, idx := range []int{1, 3} {
		s := p.statements[idx]
		args := make([]any, len(s.args))
		for i, a := range s.args {
			args[i] = a.Value
		}
		plan := n25RowsOf(t, writer, "EXPLAIN QUERY PLAN "+s.query, args...)
		hasOwner, hasCard := false, idx == 3
		for _, row := range plan {
			detail := fmt.Sprint(row[3])
			t.Log(detail)
			if strings.HasPrefix(detail, "SCAN ") && !strings.Contains(detail, "USING") {
				t.Fatalf("avoidable scan: %s", detail)
			}
			if idx == 1 && strings.Contains(detail, "SEARCH a USING") && strings.Contains(detail, "workspace_id=?") {
				hasOwner = true
			}
			if idx == 3 && strings.Contains(detail, "SEARCH owner USING") && strings.Contains(detail, "id=?") {
				hasOwner = true
			}
			if strings.Contains(detail, "SEARCH cc USING") && strings.Contains(detail, "account_id=?") {
				hasCard = true
			}
		}
		if !hasOwner || !hasCard {
			t.Fatalf("unscoped/nonindexed plan=%v", plan)
		}
	}
}

func n25AdminExec(t *testing.T, pool *sql.DB, q string) {
	t.Helper()
	var path string
	if err := pool.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	admin.SetMaxOpenConns(1)
	if _, err = admin.Exec(q); err != nil {
		t.Fatal(err)
	}
}
