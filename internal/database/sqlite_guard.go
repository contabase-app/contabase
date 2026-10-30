package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync/atomic"
)

var errSQLiteWriteGuard = errors.New("sqlite connection integrity")

func publishSQLiteGuard(db *sql.DB, guard *sqliteGuardConnector) error {
	guard.enabled.Store(true)
	conn, err := db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = conn.Raw(func(raw any) error { return raw.(*sqliteGuardConn).check(context.Background()) }); err != nil {
		return err
	}
	return validateAdmission78Reopen(context.Background(), conn)
}

// The switch belongs to the opener, never to application callers or Conn.Raw.
// Only startup's existing migrations run before publication of this capability.
type sqliteGuardConnector struct {
	dsn     string
	base    driver.Driver
	enabled atomic.Bool
}

func newSQLiteGuardPool(dsn string) (*sql.DB, *sqliteGuardConnector, error) {
	registration, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, err
	}
	c := &sqliteGuardConnector{dsn: dsn, base: registration.Driver()}
	_ = registration.Close()
	db := sql.OpenDB(c)
	db.SetMaxOpenConns(1)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, c, nil
}
func (c *sqliteGuardConnector) Driver() driver.Driver { return &sqliteGuardDriver{c} }

type sqliteGuardDriver struct{ connector *sqliteGuardConnector }

func (d *sqliteGuardDriver) Open(string) (driver.Conn, error) { return nil, errSQLiteWriteGuard }
func (c *sqliteGuardConnector) Connect(ctx context.Context) (driver.Conn, error) {
	raw, err := c.base.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	g := &sqliteGuardConn{raw: raw, owner: c}
	// DSN values are not authority. Configure and read back every physical open.
	// Explicit guarded transactions need SQLite's statement rollback journal.
	// Keep that transient journal in memory rather than paying file I/O for
	// every row. The durable database/WAL and every D5 check are unchanged.
	// Configure only at physical open: changing temp_store later drops TEMP
	// objects, and business callers must never be able to change it.
	for _, q := range []string{`PRAGMA foreign_keys=ON`, `PRAGMA defer_foreign_keys=OFF`, `PRAGMA temp_store=MEMORY`} {
		if _, err = g.rawExec(ctx, q, nil); err != nil {
			raw.Close()
			return nil, errSQLiteWriteGuard
		}
	}
	if err = g.check(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	return g, nil
}

type sqliteGuardConn struct {
	raw    driver.Conn
	owner  *sqliteGuardConnector
	active driver.Tx
	manual bool
	bad    bool
}

func (c *sqliteGuardConn) rawExec(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	return c.raw.(driver.ExecerContext).ExecContext(ctx, q, args)
}
func (c *sqliteGuardConn) check(ctx context.Context) error {
	if c.bad {
		return errSQLiteWriteGuard
	}
	for _, p := range []struct {
		name string
		want int64
	}{{"foreign_keys", 1}, {"defer_foreign_keys", 0}} {
		r, err := c.raw.(driver.QueryerContext).QueryContext(ctx, "PRAGMA "+p.name, nil)
		if err != nil {
			return c.quarantine()
		}
		v := make([]driver.Value, 1)
		err = r.Next(v)
		closeErr := r.Close()
		if err != nil || closeErr != nil || v[0] != p.want {
			return c.quarantine()
		}
	}
	return nil
}
func (c *sqliteGuardConn) quarantine() error {
	c.bad = true
	if c.active != nil {
		_ = c.active.Rollback()
		c.active = nil
	}
	if c.manual {
		_, _ = c.rawExec(context.Background(), "ROLLBACK", nil)
		c.manual = false
	}
	return errSQLiteWriteGuard
}
func (c *sqliteGuardConn) Close() error  { return c.raw.Close() }
func (c *sqliteGuardConn) IsValid() bool { return !c.bad && c.raw.(driver.Validator).IsValid() }
func (c *sqliteGuardConn) ResetSession(ctx context.Context) error {
	if c.bad {
		return driver.ErrBadConn
	}
	if c.owner.enabled.Load() {
		if err := c.check(ctx); err != nil {
			return err
		}
	}
	return c.raw.(driver.SessionResetter).ResetSession(ctx)
}
func (c *sqliteGuardConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *sqliteGuardConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c.owner.enabled.Load() {
		if err := c.check(ctx); err != nil {
			return nil, err
		}
	}
	t, err := c.raw.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	c.active = t
	return &sqliteGuardTx{c: c, raw: t, ctx: ctx}, nil
}

type sqliteGuardTx struct {
	c   *sqliteGuardConn
	raw driver.Tx
	ctx context.Context
}

func (t *sqliteGuardTx) Commit() error {
	if t.c.owner.enabled.Load() {
		if err := t.c.check(t.ctx); err != nil {
			return err
		}
	}
	err := t.raw.Commit()
	t.c.active = nil
	if err != nil {
		t.c.bad = true
	} // Never retry an uncertain commit or reuse its connection.
	return err
}
func (t *sqliteGuardTx) Rollback() error {
	if t.c.active == nil {
		return nil
	}
	err := t.raw.Rollback()
	t.c.active = nil
	if err != nil {
		t.c.bad = true
	}
	return err
}

// Lex SQL without interpreting quoted text as syntax. Only one statement is
// accepted. Neither a trailing comment nor a prepared execution bypasses this.
func sqliteGuardWords(q string) ([]string, bool) {
	var words []string
	ended := false
	for i := 0; i < len(q); {
		x := q[i]
		if x == ' ' || x == '\t' || x == '\r' || x == '\n' {
			i++
			continue
		}
		if x == '-' && i+1 < len(q) && q[i+1] == '-' {
			i += 2
			for i < len(q) && q[i] != '\n' {
				i++
			}
			continue
		}
		if x == '/' && i+1 < len(q) && q[i+1] == '*' {
			j := strings.Index(q[i+2:], "*/")
			if j < 0 {
				return nil, false
			}
			i += j + 4
			continue
		}
		if ended {
			return nil, false
		}
		if x == ';' {
			ended = true
			i++
			continue
		}
		if x == '\'' || x == '"' || x == '`' || x == '[' {
			end := x
			if x == '[' {
				end = ']'
			}
			i++
			closed := false
			for i < len(q) {
				if q[i] == end {
					i++
					if end != ']' && i < len(q) && q[i] == end {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, false
			}
			continue
		}
		if x >= 'a' && x <= 'z' || x >= 'A' && x <= 'Z' || x == '_' {
			j := i + 1
			for j < len(q) && (q[j] >= 'a' && q[j] <= 'z' || q[j] >= 'A' && q[j] <= 'Z' || q[j] >= '0' && q[j] <= '9' || q[j] == '_') {
				j++
			}
			words = append(words, strings.ToUpper(q[i:j]))
			i = j
			continue
		}
		i++
	}
	return words, len(words) > 0
}
func (c *sqliteGuardConn) authorize(ctx context.Context, q string) (string, error) {
	if !c.owner.enabled.Load() {
		return "MAINTENANCE", nil
	}
	if err := c.check(ctx); err != nil {
		return "", err
	}
	w, ok := sqliteGuardWords(q)
	if !ok {
		return "", c.quarantine()
	}
	first := w[0]
	switch first {
	case "SELECT", "INSERT", "UPDATE", "DELETE", "REPLACE", "WITH", "EXPLAIN":
		return first, nil
	case "SAVEPOINT", "RELEASE":
		if c.active != nil || c.manual {
			return first, nil
		}
	case "BEGIN":
		if len(w) == 2 && (w[1] == "IMMEDIATE" || w[1] == "DEFERRED") && c.active == nil && !c.manual {
			return first, nil
		}
	case "COMMIT":
		if len(w) == 1 && c.manual {
			return first, nil
		}
	case "ROLLBACK":
		if len(w) > 2 && w[1] == "TO" && (c.manual || c.active != nil) {
			return "SAVEPOINT", nil
		}
		if len(w) == 1 && c.manual {
			return first, nil
		}
	case "PRAGMA":
		// These connection-local settings cannot disable FK retention. Read
		// restriction is allowed only in the direction that enables it.
		if len(w) >= 2 && w[1] == "BUSY_TIMEOUT" {
			return "SELECT", nil
		}
		if len(w) == 3 && w[1] == "QUERY_ONLY" && w[2] == "ON" {
			return "SELECT", nil
		}
		// Read-only inspection and the existing checkpoint job. In particular,
		// foreign_keys(0), defer=ON and schema-qualified/quoted toggles fail.
		if len(w) == 2 && !strings.ContainsAny(q, "=()") {
			switch w[1] {
			case "FOREIGN_KEYS", "DEFER_FOREIGN_KEYS", "JOURNAL_MODE", "SYNCHRONOUS", "BUSY_TIMEOUT", "PAGE_SIZE", "PAGE_COUNT", "QUICK_CHECK", "INTEGRITY_CHECK", "FOREIGN_KEY_CHECK":
				return "SELECT", nil
			}
		}
		if len(w) >= 2 && (w[1] == "TABLE_INFO" || w[1] == "TABLE_XINFO" || w[1] == "INDEX_LIST" || w[1] == "INDEX_INFO" || w[1] == "FOREIGN_KEY_LIST" || w[1] == "WAL_CHECKPOINT") && !strings.Contains(q, "=") {
			return "SELECT", nil
		}
	}
	return "", c.quarantine()
}
func (c *sqliteGuardConn) execute(ctx context.Context, q string, run func() (driver.Result, error)) (driver.Result, error) {
	kind, err := c.authorize(ctx, q)
	if err != nil {
		return nil, err
	}
	if kind == "BEGIN" {
		r, err := run()
		if err == nil {
			c.manual = true
		}
		return r, err
	}
	if kind == "COMMIT" || kind == "ROLLBACK" {
		r, err := run()
		c.manual = false
		if err != nil {
			c.bad = true
		}
		return r, err
	}
	// A direct DB.Exec is a guarded transaction too, including a final check.
	if kind != "MAINTENANCE" && kind != "SELECT" && kind != "EXPLAIN" && kind != "SAVEPOINT" && kind != "RELEASE" && c.active == nil && !c.manual {
		t, err := c.BeginTx(ctx, driver.TxOptions{})
		if err != nil {
			return nil, err
		}
		r, err := run()
		if err != nil {
			_ = t.Rollback()
			return nil, err
		}
		if err = t.Commit(); err != nil {
			return nil, err
		}
		return r, nil
	}
	return run()
}
func (c *sqliteGuardConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	return c.execute(ctx, q, func() (driver.Result, error) { return c.rawExec(ctx, q, args) })
}
func (c *sqliteGuardConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	kind, err := c.authorize(ctx, q)
	if err != nil {
		return nil, err
	}
	if kind == "BEGIN" || kind == "COMMIT" || kind == "ROLLBACK" || kind == "SAVEPOINT" || kind == "RELEASE" {
		return nil, c.quarantine()
	}
	var own driver.Tx
	if kind != "MAINTENANCE" && kind != "SELECT" && kind != "EXPLAIN" && c.active == nil && !c.manual {
		own, err = c.BeginTx(ctx, driver.TxOptions{})
		if err != nil {
			return nil, err
		}
	}
	r, err := c.raw.(driver.QueryerContext).QueryContext(ctx, q, args)
	if err != nil {
		if own != nil {
			_ = own.Rollback()
		}
		return nil, err
	}
	if own != nil {
		return &sqliteGuardRows{Rows: r, tx: own}, nil
	}
	return r, nil
}

type sqliteGuardRows struct {
	driver.Rows
	tx     driver.Tx
	failed bool
	closed bool
}

func (r *sqliteGuardRows) Next(v []driver.Value) error {
	err := r.Rows.Next(v)
	if err != nil && err != io.EOF {
		r.failed = true
	}
	return err
}
func (r *sqliteGuardRows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	err := r.Rows.Close()
	if err != nil || r.failed {
		return errors.Join(err, r.tx.Rollback())
	}
	return r.tx.Commit()
}
func (c *sqliteGuardConn) Prepare(q string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), q)
}
func (c *sqliteGuardConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	if _, err := c.authorize(ctx, q); err != nil {
		return nil, err
	}
	s, err := c.raw.(driver.ConnPrepareContext).PrepareContext(ctx, q)
	if err != nil {
		return nil, err
	}
	return &sqliteGuardStmt{raw: s, c: c, q: q}, nil
}

type sqliteGuardStmt struct {
	raw driver.Stmt
	c   *sqliteGuardConn
	q   string
}

func (s *sqliteGuardStmt) Close() error  { return s.raw.Close() }
func (s *sqliteGuardStmt) NumInput() int { return s.raw.NumInput() }
func guardNamed(args []driver.Value) []driver.NamedValue {
	v := make([]driver.NamedValue, len(args))
	for i, a := range args {
		v[i] = driver.NamedValue{Ordinal: i + 1, Value: a}
	}
	return v
}
func (s *sqliteGuardStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), guardNamed(args))
}
func (s *sqliteGuardStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), guardNamed(args))
}
func (s *sqliteGuardStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.c.execute(ctx, s.q, func() (driver.Result, error) { return s.raw.(driver.StmtExecContext).ExecContext(ctx, args) })
}
func (s *sqliteGuardStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	// Reuse the same reserved connection, never the pool. Query's own implicit
	// transaction lifecycle also covers DML RETURNING and cursor errors.
	return s.c.QueryContext(ctx, s.q, args)
}
