package database

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Real historical progression through v26; only later versions are marked out
// of scope so runMigrations executes the actual v27 in isolation.
func f10Before27(t *testing.T) *sql.DB {
	t.Helper()
	db := f10Fixture(t, false)
	if err := runCategoryMigration12(db, migrations[11]); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.version <= 12 || m.version >= 27 {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := m.up(tx); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version,name) VALUES(?,?)`, m.version, m.name); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range migrations {
		if m.version > 27 {
			f10Exec(t, db, `INSERT INTO schema_migrations(version,name) VALUES(?,?)`, m.version, m.name)
		}
	}
	return db
}

func TestF10Migration27ReproducesPublishedSQLFailure(t *testing.T) {
	db := f10Before27(t)
	before := f10Data(t, db)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	// Exact first statement used by published v27 on a populated table.
	_, err = tx.Exec(`ALTER TABLE cost_limits ADD COLUMN created_at INTEGER NOT NULL DEFAULT (unixepoch())`)
	if err == nil || !strings.Contains(err.Error(), "non-constant default") {
		t.Fatalf("historical failure not reproduced: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, f10Data(t, db)) {
		t.Fatal("failed historical DDL changed data")
	}
}

func TestF10Migration27PopulatedDefaultsAndPartialColumns(t *testing.T) {
	for _, column := range []string{"none", "created_at", "updated_at"} {
		t.Run(column, func(t *testing.T) {
			db := f10Before27(t)
			if column != "none" {
				f10Exec(t, db, `ALTER TABLE cost_limits ADD COLUMN `+column+` INTEGER NOT NULL DEFAULT 123`)
			}
			f10Exec(t, db, `CREATE INDEX f10_limit_amount ON cost_limits(max_amount_monthly); CREATE TRIGGER f10_limit_guard BEFORE UPDATE OF alert_threshold ON cost_limits WHEN NEW.alert_threshold<0 BEGIN SELECT RAISE(ABORT,'threshold'); END`)
			before := f10Rows(t, db, `SELECT id,workspace_id,category_id,max_amount_monthly,alert_threshold FROM cost_limits ORDER BY id`)
			start := time.Now().Unix()
			if err := runMigrations(db); err != nil {
				t.Fatal(err)
			}
			end := time.Now().Unix()
			if !reflect.DeepEqual(before, f10Rows(t, db, `SELECT id,workspace_id,category_id,max_amount_monthly,alert_threshold FROM cost_limits ORDER BY id`)) {
				t.Fatal("limit data changed")
			}
			for _, r := range f10Rows(t, db, `SELECT created_at,updated_at FROM cost_limits`) {
				for i, name := range []string{"created_at", "updated_at"} {
					v := r[i].(int64)
					if name == column {
						if v != 123 {
							t.Fatal("existing timestamp changed")
						}
					} else if v < start || v > end {
						t.Fatal("timestamp outside migration interval", v)
					}
				}
			}
			if len(f10Rows(t, db, `SELECT name FROM sqlite_schema WHERE name IN ('f10_limit_amount','f10_limit_guard')`)) != 2 {
				t.Fatal("limit objects lost")
			}
			if _, err := db.Exec(`UPDATE cost_limits SET alert_threshold=-1`); err == nil {
				t.Fatal("limit trigger not enforced")
			}
			f10Exec(t, db, `INSERT INTO cost_limits(id,workspace_id,category_id,max_amount_monthly) VALUES('new','personal','personal77',111)`)
			for _, r := range f10Rows(t, db, `SELECT created_at,updated_at FROM cost_limits WHERE id='new'`) {
				for _, value := range r {
					v := value.(int64)
					if v < start || v > time.Now().Unix() {
						t.Fatal("new row default lost", v)
					}
				}
			}
			current := f10Data(t, db)
			versions := f10Rows(t, db, `SELECT * FROM schema_migrations ORDER BY version`)
			f10Exec(t, db, `CREATE TABLE cost_limits_new(marker TEXT)`)
			if err := runMigrations(db); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(current, f10Data(t, db)) || !reflect.DeepEqual(versions, f10Rows(t, db, `SELECT * FROM schema_migrations ORDER BY version`)) {
				t.Fatal("v27 repeated")
			}
			f10AssertFKOn(t, db)
			if err := assertDatabaseForeignKeyIntegrity(db); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestF10Migration27RollbackAndRetry(t *testing.T) {
	for _, failure := range []string{"copy", "register"} {
		t.Run(failure, func(t *testing.T) {
			db := f10Before27(t)
			if failure == "copy" {
				f10Exec(t, db, `PRAGMA ignore_check_constraints=ON; UPDATE cost_limits SET max_amount_monthly=-1 WHERE id='limit-personal3'; PRAGMA ignore_check_constraints=OFF`)
			} else {
				f10Exec(t, db, `CREATE TRIGGER f10_reject_27 BEFORE INSERT ON schema_migrations WHEN NEW.version=27 BEGIN SELECT RAISE(ABORT,'F10 reject'); END`)
			}
			before := f10Data(t, db)
			schema := f10Rows(t, db, `SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY type,name`)
			if err := runMigrations(db); err == nil {
				t.Fatal("failed migration accepted")
			}
			if !reflect.DeepEqual(before, f10Data(t, db)) || !reflect.DeepEqual(schema, f10Rows(t, db, `SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY type,name`)) {
				t.Fatal("rollback changed data/schema")
			}
			if applied, _ := isMigrationApplied(db, 27); applied {
				t.Fatal("failed migration recorded")
			}
			f10AssertFKOn(t, db)
			if failure == "copy" {
				f10Exec(t, db, `UPDATE cost_limits SET max_amount_monthly=87654 WHERE id='limit-personal3'`)
			} else {
				f10Exec(t, db, `DROP TRIGGER f10_reject_27`)
			}
			if err := runMigrations(db); err != nil {
				t.Fatal("retry", err)
			}
		})
	}
}

func TestF10Migration27RejectsUnknownSchema(t *testing.T) {
	for _, kind := range []string{"dependent", "extra-column", "scratch"} {
		t.Run(kind, func(t *testing.T) {
			db := f10Before27(t)
			switch kind {
			case "dependent":
				f10Exec(t, db, `CREATE TABLE extra_limits(id TEXT PRIMARY KEY,limit_id TEXT REFERENCES cost_limits(id) ON DELETE CASCADE); INSERT INTO extra_limits VALUES('keep','limit-personal3')`)
			case "extra-column":
				f10Exec(t, db, `ALTER TABLE cost_limits ADD COLUMN custom_note TEXT DEFAULT 'keep'`)
			case "scratch":
				f10Exec(t, db, `CREATE TABLE cost_limits_new(marker TEXT); INSERT INTO cost_limits_new VALUES('keep')`)
			}
			before := f10Data(t, db)
			if err := runMigrations(db); err == nil {
				t.Fatal("unknown schema accepted")
			}
			if !reflect.DeepEqual(before, f10Data(t, db)) {
				t.Fatal("unknown schema changed")
			}
			if kind == "dependent" && len(f10Rows(t, db, `SELECT * FROM extra_limits`)) != 1 {
				t.Fatal("dependent removed")
			}
		})
	}
}
