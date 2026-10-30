package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func f10Fixture(t *testing.T, only12 bool) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "historic.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	baseline, err := os.ReadFile("testdata/migration12_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	f10Exec(t, db, string(baseline))
	f10Exec(t, db, `CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL DEFAULT (unixepoch()))`)
	for _, m := range migrations {
		if m.version >= 12 {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := m.up(tx); err != nil {
			tx.Rollback()
			t.Fatalf("historical migration %d: %v", m.version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version,name) VALUES(?,?)`, m.version, m.name); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	// Isolated-v12 tests skip later transformations, without replacing m12.up or
	// the production runner. Full-upgrade tests leave these versions pending.
	if only12 {
		for _, m := range migrations {
			if m.version > 12 {
				f10Exec(t, db, `INSERT INTO schema_migrations(version,name) VALUES(?,?)`, m.version, m.name)
			}
		}
	}
	f10Seed(t, db)
	if err := assertDatabaseForeignKeyIntegrity(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func f10Seed(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, ws := range []string{"personal", "business"} {
		f10Exec(t, db, `INSERT INTO users(id,name,email,password_hash) VALUES(?,?,?,'hash')`, ws, ws, ws+"@example.test")
		f10Exec(t, db, `INSERT INTO workspaces(id,name,type) VALUES(?,?,?)`, ws, ws, ws)
		f10Exec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES(?,?,'ADMIN')`, ws, ws)
		f10Exec(t, db, `INSERT INTO accounts(id,workspace_id,name,type,initial_balance,current_balance) VALUES(?,?,?,'CHECKING',100000,98765)`, ws, ws, ws)
		for _, suffix := range []string{"9001", "3", "8000000", "77"} {
			f10Exec(t, db, `INSERT INTO categories(id,workspace_id,name,type,macro_group,icon,color,is_fixed,created_at) VALUES(?,?,?,'EXPENSE','Essencial','star','#123456',1,1700000000)`, ws+suffix, ws, "Categoria "+suffix)
		}
		f10Exec(t, db, `UPDATE categories SET parent_id=? WHERE id=?`, ws+"9001", ws+"3")
		for i := 0; i < 12; i++ {
			var cat any = ws + "3"
			if i == 11 {
				cat = nil
			}
			f10Exec(t, db, `INSERT INTO transactions(id,workspace_id,user_id,account_id,category_id,type,amount,date,description,status,payment_status,created_at,updated_at) VALUES(?,?,?,?,?,'EXPENSE',1234,1700000000,'historical','pending','PENDING',1700000001,1700000002)`, fmt.Sprintf("%s-t%d", ws, i), ws, ws, ws, cat)
		}
		for _, cat := range []string{ws + "3", ws + "8000000"} {
			f10Exec(t, db, `INSERT INTO cost_limits(id,workspace_id,category_id,max_amount_monthly) VALUES(?,?,?,87654)`, "limit-"+cat, ws, cat)
		}
		f10Exec(t, db, `INSERT INTO recurring_rules(id,workspace_id,user_id,account_id,category_id,type,amount,start_date,frequency,active) VALUES(?,?,?,?,?,'EXPENSE',1234,1700000000,'MONTHLY',0)`, ws, ws, ws, ws, ws+"3")
		f10Exec(t, db, `INSERT INTO boxes(id,workspace_id,name,category_id,target_amount,monthly_recharge) VALUES(?,?,?, ?,50000,1000)`, ws, ws, ws, ws+"3")
		f10Exec(t, db, `INSERT INTO box_virtual_ledger(id,box_id,amount,type,reference_date) VALUES(?,?,5678,'RECHARGE',1700000000)`, ws, ws)
	}
}

func f10Data(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	out := map[string][][]any{}
	for _, table := range f10Tables {
		out[table] = f10Rows(t, db, `SELECT * FROM `+table+` ORDER BY id`)
	}
	return out
}

func f10AssertFKOn(t *testing.T, db *sql.DB) {
	t.Helper()
	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys=%d err=%v", fk, err)
	}
}

func TestF10Migration12RollbackAndRetry(t *testing.T) {
	for _, boundary := range []string{"after-create", "during-copy", "before-drop", "after-rebuild", "semantic-loss", "foreign-key", "register"} {
		t.Run(boundary, func(t *testing.T) {
			db := f10Fixture(t, true)
			before := f10Data(t, db)
			schema := f10Rows(t, db, `SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY type,name`)
			m := migrations[11]
			original := m.up
			injected := errors.New("F10 injected failure")
			if boundary == "register" {
				f10Exec(t, db, `CREATE TRIGGER f10_register_failure BEFORE INSERT ON schema_migrations WHEN NEW.version=12 BEGIN SELECT RAISE(ABORT,'F10 register'); END`)
			}
			m.up = func(tx *sql.Tx) error {
				switch boundary {
				case "after-create", "during-copy", "before-drop":
					if _, err := tx.Exec(`CREATE TABLE categories_new AS SELECT * FROM categories WHERE 0`); err != nil {
						return err
					}
					if boundary == "during-copy" {
						_, err := tx.Exec(`INSERT INTO categories_new SELECT * FROM categories; INSERT INTO missing_f10_table VALUES(1)`)
						return err
					}
					if boundary == "before-drop" {
						if _, err := tx.Exec(`INSERT INTO categories_new SELECT * FROM categories`); err != nil {
							return err
						}
					}
					return injected
				}
				if err := original(tx); err != nil {
					return err
				}
				switch boundary {
				case "after-rebuild":
					return injected
				case "semantic-loss":
					_, err := tx.Exec(`UPDATE transactions SET category_id=NULL`)
					return err
				case "foreign-key":
					_, err := tx.Exec(`INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('missing','personal','USER')`)
					return err
				}
				return nil
			}
			if err := runCategoryMigration12(db, m); err == nil {
				t.Fatal("injected failure accepted")
			}
			f10AssertFKOn(t, db)
			if boundary == "register" {
				f10Exec(t, db, `DROP TRIGGER f10_register_failure`)
			}
			if !reflect.DeepEqual(before, f10Data(t, db)) {
				t.Fatal("rollback changed historical content")
			}
			if !reflect.DeepEqual(schema, f10Rows(t, db, `SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY type,name`)) {
				t.Fatal("rollback changed schema")
			}
			if applied, err := isMigrationApplied(db, 12); err != nil || applied {
				t.Fatal("failed version registered", err)
			}
			if err := runMigrations(db); err != nil {
				t.Fatal("retry", err)
			}
			f10AssertFKOn(t, db)
		})
	}
}

func TestF10Migration12ConnectionAndCategoryObjects(t *testing.T) {
	db := f10Fixture(t, true)
	f10Exec(t, db, `CREATE INDEX f10_category_name ON categories(workspace_id,name); CREATE TRIGGER f10_category_guard BEFORE UPDATE OF name ON categories WHEN NEW.name='' BEGIN SELECT RAISE(ABORT,'empty'); END`)
	// Reserve another real connection: the pragma must affect only the lease
	// used for v12, not accidentally another connection in the pool.
	db.SetMaxOpenConns(2)
	other, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	var fk int
	if err := other.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatal(fk, err)
	}
	f10AssertFKOn(t, db)
	if len(f10Rows(t, db, `SELECT name FROM sqlite_schema WHERE name IN ('f10_category_name','f10_category_guard')`)) != 2 {
		t.Fatal("category objects lost")
	}
	if _, err := db.Exec(`UPDATE categories SET name='' WHERE id='personal3'`); err == nil {
		t.Fatal("category trigger lost")
	}
}

func TestF10Migration12RejectsUnknownScratchAndInvalidInput(t *testing.T) {
	for _, kind := range []string{"scratch", "mixed-case-scratch", "invalid-fk"} {
		t.Run(kind, func(t *testing.T) {
			db := f10Fixture(t, true)
			if kind == "scratch" {
				f10Exec(t, db, `CREATE TABLE categories_new(marker TEXT); INSERT INTO categories_new VALUES('preserve')`)
			} else if kind == "mixed-case-scratch" {
				f10Exec(t, db, `CREATE TABLE Categories_New(marker TEXT); INSERT INTO Categories_New VALUES('preserve')`)
			} else {
				f10Exec(t, db, `PRAGMA foreign_keys=OFF; UPDATE transactions SET category_id='missing' WHERE id='personal-t0'; PRAGMA foreign_keys=ON`)
			}
			before := f10Data(t, db)
			if err := runMigrations(db); err == nil {
				t.Fatal("invalid input accepted")
			}
			if !reflect.DeepEqual(before, f10Data(t, db)) {
				t.Fatal("invalid input modified")
			}
			if applied, _ := isMigrationApplied(db, 12); applied {
				t.Fatal("version recorded")
			}
			f10AssertFKOn(t, db)
		})
	}
}

func TestF10FullHistoricalUpgradeAndAlreadyApplied(t *testing.T) {
	db := f10Fixture(t, false)
	queries := map[string]string{
		// Migration 49 adds canonical Business categories; preserve every original
		// ID/content while allowing that pre-existing, unrelated migration to run.
		"categories":         `SELECT id,workspace_id,name,icon,color,type,macro_group,parent_id,is_fixed,created_at FROM categories WHERE id LIKE 'personal%' OR id LIKE 'business%' ORDER BY id`,
		"transactions":       `SELECT id,workspace_id,user_id,account_id,category_id,type,amount,date,description,status,payment_status,due_date,created_at,updated_at FROM transactions ORDER BY id`,
		"cost_limits":        `SELECT id,workspace_id,category_id,max_amount_monthly FROM cost_limits ORDER BY id`,
		"accounts":           `SELECT id,workspace_id,initial_balance,current_balance FROM accounts ORDER BY id`,
		"boxes":              `SELECT id,workspace_id,category_id,target_amount,monthly_recharge FROM boxes ORDER BY id`,
		"recurring_rules":    `SELECT id,workspace_id,category_id,amount,start_date,frequency,active FROM recurring_rules ORDER BY id`,
		"box_virtual_ledger": `SELECT id,box_id,amount,type,reference_date FROM box_virtual_ledger ORDER BY id`,
	}
	want := map[string][][]any{}
	for k, q := range queries {
		want[k] = f10Rows(t, db, q)
	}
	// Same startup sequence as initializeSQLiteDatabase, without opening jobs
	// or real paths: base DDL ensures missing tables, then the real migrator.
	f10Exec(t, db, ddl)
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	for k, q := range queries {
		if !reflect.DeepEqual(want[k], f10Rows(t, db, q)) {
			t.Errorf("full upgrade changed %s: want %v got %v", k, want[k], f10Rows(t, db, q))
		}
	}
	f10AssertFKOn(t, db)
	if got := f10Rows(t, db, `SELECT count(*) FROM schema_migrations`)[0][0]; got != int64(len(migrations)) {
		t.Fatal("missing migrations", got)
	}
	for _, damaged := range []bool{false, true} {
		if damaged {
			f10Exec(t, db, `UPDATE transactions SET category_id=NULL WHERE id='personal-t0'; DELETE FROM cost_limits WHERE id='limit-personal3'`)
		}
		before := f10Data(t, db)
		versions := f10Rows(t, db, `SELECT * FROM schema_migrations ORDER BY version`)
		// A marker with the old scratch name proves v12 was not replayed.
		f10Exec(t, db, `CREATE TABLE categories_new(marker TEXT)`)
		if err := runMigrations(db); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, f10Data(t, db)) || !reflect.DeepEqual(versions, f10Rows(t, db, `SELECT * FROM schema_migrations ORDER BY version`)) {
			t.Fatal("already applied database changed")
		}
		f10Exec(t, db, `DROP TABLE categories_new`)
	}
	fresh, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	fresh.SetMaxOpenConns(1)
	f10Exec(t, fresh, ddl)
	if err := runMigrations(fresh); err != nil {
		t.Fatal(err)
	}
	// Compare all schema objects, and semantic column/FK/index inventories.
	// Older baseline constraints that later migrations never changed are
	// reported explicitly instead of being silently normalized by F10.
	for _, table := range []string{"categories", "cost_limits", "boxes", "accounts"} {
		for _, q := range []string{`SELECT name,type,"notnull",dflt_value,pk FROM pragma_table_info('%s') ORDER BY name`, `SELECT "table","from","to",on_update,on_delete,match FROM pragma_foreign_key_list('%s') ORDER BY "from"`, `SELECT name,"unique",origin,partial FROM pragma_index_list('%s') ORDER BY name`} {
			q = fmt.Sprintf(q, table)
			if !reflect.DeepEqual(f10Rows(t, db, q), f10Rows(t, fresh, q)) {
				t.Errorf("schema mismatch %s: old=%v fresh=%v", q, f10Rows(t, db, q), f10Rows(t, fresh, q))
			}
		}
	}
	if !reflect.DeepEqual(f10Rows(t, db, `SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name`), f10Rows(t, fresh, `SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name`)) {
		t.Fatal("table inventory differs")
	}
	for _, row := range f10Rows(t, db, `SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name`) {
		table := row[0].(string)
		for label, q := range map[string]string{
			"columns": `SELECT name,type,"notnull",dflt_value,pk FROM pragma_table_info('%s') ORDER BY name`,
			"fks":     `SELECT "table","from","to",on_update,on_delete,match FROM pragma_foreign_key_list('%s') ORDER BY "from"`,
			"indexes": `SELECT name,"unique",origin,partial FROM pragma_index_list('%s') ORDER BY name`,
		} {
			query := fmt.Sprintf(q, table)
			old, new := f10Rows(t, db, query), f10Rows(t, fresh, query)
			// Pre-existing differences: v2/v44 add TEXT columns without the FKs
			// declared by fresh ddl; v13's explicit unique index replaces the
			// fresh contacts inline UNIQUE. F10 must not repair those contracts.
			var inheritedFields map[string]bool
			if label == "fks" && table == "recurring_rules" {
				inheritedFields = map[string]bool{"contact_id": true, "parent_rule_id": true, "series_id": true}
			}
			if label == "fks" && table == "transactions" {
				inheritedFields = map[string]bool{"recurring_rule_id": true, "series_id": true}
			}
			var comparable [][]any
			removed := 0
			for _, item := range new {
				if (inheritedFields != nil && inheritedFields[item[1].(string)]) || (label == "indexes" && table == "contacts" && item[0] == "sqlite_autoindex_contacts_2") {
					removed++
					continue
				}
				comparable = append(comparable, item)
			}
			expectedRemoved := len(inheritedFields)
			if label == "indexes" && table == "contacts" {
				expectedRemoved++
			}
			if removed != expectedRemoved || !reflect.DeepEqual(old, comparable) {
				t.Errorf("unexpected schema delta %s/%s: old=%v new=%v", table, label, old, new)
			}
			if label == "indexes" {
				for _, idx := range old {
					// Physical column ordinals may differ after historical ALTER ADD.
					indexQuery := fmt.Sprintf(`SELECT seqno,name,desc,coll,key FROM pragma_index_xinfo('%s') ORDER BY seqno`, idx[0])
					if !reflect.DeepEqual(f10Rows(t, db, indexQuery), f10Rows(t, fresh, indexQuery)) {
						t.Errorf("index definition differs: %s", idx[0])
					}
				}
			}
		}
	}
	if !reflect.DeepEqual(f10Rows(t, db, `SELECT name,sql FROM sqlite_schema WHERE type='trigger' ORDER BY name`), f10Rows(t, fresh, `SELECT name,sql FROM sqlite_schema WHERE type='trigger' ORDER BY name`)) {
		t.Fatal("trigger definitions differ")
	}
	// Exercise the historical purpose: macro_group is now free text and nullable.
	f10Exec(t, db, `UPDATE categories SET macro_group='Custom historical group' WHERE id='personal77'; UPDATE categories SET macro_group=NULL WHERE id='business77'`)
	if err := assertDatabaseForeignKeyIntegrity(db); err != nil {
		t.Fatal(err)
	}
}

func TestF10FreshCurrentAndHistoricalDamage(t *testing.T) {
	fresh, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	fresh.SetMaxOpenConns(1)
	f10Exec(t, fresh, ddl)
	if err := runMigrations(fresh); err != nil {
		t.Fatal(err)
	}
	f10Seed(t, fresh)
	if got := f10Rows(t, fresh, `SELECT count(*) FROM schema_migrations`)[0][0]; got != int64(len(migrations)) {
		t.Fatal("missing migrations", got)
	}
	for _, damaged := range []bool{false, true} {
		if damaged {
			f10Exec(t, fresh, `UPDATE transactions SET category_id=NULL WHERE id='personal-t0'; DELETE FROM cost_limits WHERE id='limit-personal3'`)
		}
		before := f10Data(t, fresh)
		versions := f10Rows(t, fresh, `SELECT * FROM schema_migrations ORDER BY version`)
		f10Exec(t, fresh, `CREATE TABLE categories_new(marker TEXT)`)
		if err := runMigrations(fresh); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, f10Data(t, fresh)) || !reflect.DeepEqual(versions, f10Rows(t, fresh, `SELECT * FROM schema_migrations ORDER BY version`)) {
			t.Fatal("already current database changed")
		}
		f10Exec(t, fresh, `DROP TABLE categories_new`)
	}
	historical := f10Fixture(t, true)
	if err := runMigrations(historical); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`SELECT name,type,"notnull",dflt_value,pk FROM pragma_table_info('categories') ORDER BY name`,
		`SELECT "table","from","to",on_update,on_delete,match FROM pragma_foreign_key_list('categories') ORDER BY "from"`,
		`SELECT name,sql FROM sqlite_schema WHERE type='trigger' AND tbl_name='categories' ORDER BY name`,
	} {
		if !reflect.DeepEqual(f10Rows(t, historical, query), f10Rows(t, fresh, query)) {
			t.Fatal("category schema differs", query)
		}
	}
	// The isolated fixture runs only v12. Compare its complete index contract
	// with the current schema, accounting explicitly for the index added by v76.
	// Any other extra, missing or changed index still fails this comparison.
	indexQuery := `SELECT name,"unique",origin,partial FROM pragma_index_list('categories')`
	wantHistoricalIndexes := [][]any{
		{"idx_categories_parent", int64(0), "c", int64(0)},
		{"idx_categories_workspace", int64(0), "c", int64(0)},
		{"sqlite_autoindex_categories_1", int64(1), "pk", int64(0)},
	}
	for _, check := range []struct {
		db    *sql.DB
		query string
	}{
		{historical, indexQuery + ` ORDER BY name`},
		{fresh, indexQuery + ` WHERE name!='idx_c2b_category_tenant' ORDER BY name`},
	} {
		if got := f10Rows(t, check.db, check.query); !reflect.DeepEqual(got, wantHistoricalIndexes) {
			t.Fatalf("v12 category index inventory differs: got %v want %v", got, wantHistoricalIndexes)
		}
	}
	for _, index := range wantHistoricalIndexes {
		query := fmt.Sprintf(`SELECT seqno,name,desc,coll,key FROM pragma_index_xinfo('%s') ORDER BY seqno`, index[0])
		if !reflect.DeepEqual(f10Rows(t, historical, query), f10Rows(t, fresh, query)) {
			t.Fatalf("v12 category index definition differs: %s", index[0])
		}
	}
	// Do not merely ignore the later index: require its unique, non-partial,
	// tenant-scoped definition in the fully migrated current database.
	if got := f10Rows(t, fresh, indexQuery+` WHERE name='idx_c2b_category_tenant'`); !reflect.DeepEqual(got, [][]any{{"idx_c2b_category_tenant", int64(1), "c", int64(0)}}) {
		t.Fatalf("v76 category index inventory differs: %v", got)
	}
	wantTenantIndex := [][]any{{int64(0), "id", int64(0), "BINARY", int64(1)}, {int64(1), "workspace_id", int64(0), "BINARY", int64(1)}, {int64(2), nil, int64(0), "BINARY", int64(0)}}
	if got := f10Rows(t, fresh, `SELECT seqno,name,desc,coll,key FROM pragma_index_xinfo('idx_c2b_category_tenant') ORDER BY seqno`); !reflect.DeepEqual(got, wantTenantIndex) {
		t.Fatalf("v76 category index definition differs: %v", got)
	}
	for _, db := range []*sql.DB{historical, fresh} {
		f10Exec(t, db, `UPDATE categories SET macro_group=NULL WHERE id='personal77'; UPDATE categories SET macro_group='Free group' WHERE id='business77'`)
		if _, err := db.Exec(`UPDATE categories SET parent_id='unknown' WHERE id='personal77'`); err == nil {
			t.Fatal("category FK not enforced")
		}
		if _, err := db.Exec(`UPDATE categories SET is_fixed=3 WHERE id='personal77'`); err == nil {
			t.Fatal("category CHECK not enforced")
		}
		if err := assertDatabaseForeignKeyIntegrity(db); err != nil {
			t.Fatal(err)
		}
		f10AssertFKOn(t, db)
	}
}

func f10Exec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func f10Rows(t *testing.T, db *sql.DB, query string) [][]any {
	t.Helper()
	rows, err := db.Query(query)
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
		p := make([]any, len(cols))
		for i := range v {
			p[i] = &v[i]
		}
		if err := rows.Scan(p...); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

var f10Tables = []string{"categories", "transactions", "cost_limits", "recurring_rules", "boxes", "box_virtual_ledger", "accounts", "workspaces"}

func TestF10Migration12PreservesHistoricalRelations(t *testing.T) {
	db := f10Fixture(t, true)
	before := map[string][][]any{}
	// Explicit order insulates comparisons from the intentional category column reorder.
	queries := map[string]string{}
	for _, table := range f10Tables {
		cols := f10Rows(t, db, `SELECT name FROM pragma_table_info('`+table+`') ORDER BY name`)
		query := "SELECT "
		for i, c := range cols {
			if i > 0 {
				query += ","
			}
			query += `"` + c[0].(string) + `"`
		}
		query += " FROM " + table + " ORDER BY id"
		queries[table] = query
		before[table] = f10Rows(t, db, query)
	}
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	for _, table := range f10Tables {
		after := f10Rows(t, db, queries[table])
		if !reflect.DeepEqual(before[table], after) {
			t.Errorf("F10 %s changed: before=%v after=%v", table, before[table], after)
		}
	}
	if err := assertDatabaseForeignKeyIntegrity(db); err != nil {
		t.Fatal(err)
	}
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal(integrity, err)
	}
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
}
