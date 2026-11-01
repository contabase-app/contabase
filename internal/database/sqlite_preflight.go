package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
)

type sqlite79Reader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type sqlite79BasisObject struct{ kind, table, digest string }

var sqliteRuntimeMigrations []migration

func init() { sqliteRuntimeMigrations = migrations }

func sqlite79NormalizeDDL(q string) string {
	var out strings.Builder
	for i := 0; i < len(q); {
		if strings.HasPrefix(q[i:], "--") {
			j := strings.IndexByte(q[i:], '\n')
			if j < 0 {
				break
			}
			i += j + 1
			continue
		}
		if strings.HasPrefix(q[i:], "/*") {
			j := strings.Index(q[i+2:], "*/")
			if j < 0 {
				return ""
			}
			i += j + 4
			continue
		}
		c := q[i]
		if strings.ContainsRune(" \r\n\t;", rune(c)) {
			i++
			continue
		}
		if c == '\'' || c == '"' || c == '`' || c == '[' {
			end := c
			if c == '[' {
				end = ']'
			}
			start := i
			i++
			for i < len(q) {
				if q[i] == end {
					i++
					if end != ']' && i < len(q) && q[i] == end {
						i++
						continue
					}
					break
				}
				i++
			}
			out.WriteString(q[start:i])
			continue
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out.WriteByte(c)
		i++
	}
	return out.String()
}

func sqlite79Schema(q sqlite79Reader) (map[string]cancellationSchemaObject, error) {
	rows, err := q.QueryContext(context.Background(), `SELECT type,name,tbl_name,COALESCE(sql,'') FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	objects := map[string]cancellationSchemaObject{}
	for rows.Next() {
		var o cancellationSchemaObject
		if err := rows.Scan(&o.kind, &o.name, &o.table, &o.ddl); err != nil {
			return nil, err
		}
		objects[o.name] = o
	}
	return objects, rows.Err()
}

func sqlite79Markers(q sqlite79Reader, objects map[string]cancellationSchemaObject) (int, error) {
	if _, ok := objects["schema_migrations"]; !ok {
		return 0, nil
	}
	rows, err := q.QueryContext(context.Background(), `SELECT version,name FROM schema_migrations ORDER BY version`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	i := 0
	version := 0
	for rows.Next() {
		var v int
		var name string
		if err := rows.Scan(&v, &name); err != nil {
			return 0, err
		}
		if i >= len(sqliteRuntimeMigrations) || v != sqliteRuntimeMigrations[i].version || name != sqliteRuntimeMigrations[i].name {
			return 0, fmt.Errorf("unsupported or incomplete SQLite migration registry at version %d", v)
		}
		i++
		version = v
	}
	return version, rows.Err()
}

func sqlite79CheckBasis(objects map[string]cancellationSchemaObject) error {
	for name, want := range sqlite77Basis {
		got, ok := objects[name]
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(sqlite79NormalizeDDL(got.ddl))))
		if !ok || got.kind != want.kind || got.table != want.table || digest != want.digest {
			return fmt.Errorf("SQLite schema basis diverges at %s", name)
		}
	}
	return nil
}

func sqlite79CheckParentColumns(q sqlite79Reader) error {
	for table, required := range map[string][]string{
		"users": {"id:TEXT"}, "workspaces": {"id:TEXT"},
		"accounts":     {"id:TEXT", "workspace_id:TEXT", "type:TEXT", "archived_at:INTEGER", "current_balance:INTEGER", "balance_version:INTEGER"},
		"transactions": {"id:TEXT", "workspace_id:TEXT", "user_id:TEXT", "account_id:TEXT", "destination_account_id:TEXT", "amount:INTEGER", "type:TEXT", "status:TEXT", "effect_mode:TEXT", "date:INTEGER", "date_civil:TEXT", "paid_at:INTEGER", "paid_date_civil:TEXT"},
	} {
		rows, err := q.QueryContext(context.Background(), `PRAGMA table_info(`+table+`)`)
		if err != nil {
			return err
		}
		columns := map[string]string{}
		primary := ""
		for rows.Next() {
			var cid, notnull, pk int
			var name, kind string
			var fallback any
			if err := rows.Scan(&cid, &name, &kind, &notnull, &fallback, &pk); err != nil {
				rows.Close()
				return err
			}
			columns[name] = strings.ToUpper(kind)
			if pk == 1 {
				primary = name
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if primary != "id" {
			return fmt.Errorf("invalid SQLite parent identity: %s", table)
		}
		for _, column := range required {
			name, kind, _ := strings.Cut(column, ":")
			if columns[name] != kind {
				return fmt.Errorf("invalid SQLite parent column: %s.%s", table, name)
			}
		}
	}
	// Prepare the complete relation against an empty synthetic row. SQLite
	// resolves every referenced column/table before any target DDL is allowed.
	var fields []string
	for _, name := range []string{"workspace_id", "id", "investment_id", "purchase_id", "original_transaction_id", "compensation_transaction_id", "investment_account_id", "origin_account_id", "amount_cents", "economic_date", "created_by", "confirmed_at", "investment_after_cents", "investment_after_version", "origin_after_cents", "origin_after_version"} {
		fields = append(fields, "NULL AS "+name)
	}
	var valid int
	if err := q.QueryRowContext(context.Background(), `SELECT `+cancellation79Relation("c", true)+` FROM (SELECT `+strings.Join(fields, ",")+`) c`).Scan(&valid); err != nil {
		return fmt.Errorf("invalid SQLite cancellation parent relation: %w", err)
	}
	return nil
}

func sqlite79Inspect(q sqlite79Reader) (int, error) {
	objects, err := sqlite79Schema(q)
	if err != nil {
		return 0, err
	}
	for name, o := range objects {
		n := strings.ToLower(name)
		ddl := strings.ToLower(o.ddl)
		if n == "investment_adoption_admissions" || strings.Contains(n, "admission78") || strings.HasPrefix(n, "investment_admission_") || strings.HasPrefix(n, "trg_admission_") || strings.HasPrefix(n, "idx_admission_") || strings.Contains(ddl, "admission_occurrence_key") {
			return 0, fmt.Errorf("experimental SQLite schema is incompatible with runtime 79")
		}
	}
	version, err := sqlite79Markers(q, objects)
	if err != nil {
		return 0, err
	}
	for _, o := range objects {
		// Schema 80 extends the same rule: the marker must reach the schema
		// family before any of its objects may exist.
		if version < 79 && (o.name == "investment_cancellations" || strings.Contains(o.name, "cancellation79") || strings.Contains(strings.ToLower(o.ddl), "investment_cancellations")) {
			return 0, fmt.Errorf("partial SQLite cancellation schema without marker 79")
		}
		if version < 80 && (o.name == "investment_purchase_reversals" || strings.Contains(o.name, "reversal80") || strings.Contains(strings.ToLower(o.ddl), "investment_purchase_reversals")) {
			return 0, fmt.Errorf("partial SQLite reversal schema without marker 80")
		}
		if version < 81 && (o.name == "investment_record_cancellations" || strings.Contains(o.name, "recordcancel81") || strings.Contains(strings.ToLower(o.ddl), "investment_record_cancellations")) {
			return 0, fmt.Errorf("partial SQLite record cancellation schema without marker 81")
		}
		if version < 82 && strings.Contains(o.name, "reversal82") {
			return 0, fmt.Errorf("partial SQLite reversal operability schema without marker 82")
		}
	}
	if len(objects) == 0 {
		return 0, sqlite79Integrity(q)
	}
	if version >= 77 {
		if err := sqlite79CheckBasis(objects); err != nil {
			return 0, err
		}
		if err := sqlite79CheckParentColumns(q); err != nil {
			return 0, err
		}
		for name, object := range objects {
			if object.kind == "trigger" && object.table == "schema_migrations" {
				return 0, fmt.Errorf("unrecognized SQLite migration registry trigger: %s", name)
			}
		}
	} else {
		// Historical runtime inputs need the complete baseline family. A lone
		// users/workspaces table or marker does not qualify as a legacy database.
		for _, name := range []string{"users", "workspaces", "workspace_members", "accounts", "credit_cards", "categories", "invoices", "transactions", "boxes", "box_virtual_ledger", "recurring_rules", "sessions", "contacts"} {
			if o, ok := objects[name]; !ok || o.kind != "table" {
				return 0, fmt.Errorf("incomplete historical SQLite schema: %s", name)
			}
		}
		if version == 0 {
			// Unversioned input is supported only for the complete bootstrap DDL;
			// investment objects without their migration history are never inferred.
			for name := range objects {
				if strings.HasPrefix(name, "investment_") || name == "investments" {
					return 0, fmt.Errorf("unversioned investment schema")
				}
			}
		}
		for _, family := range []struct {
			version int
			tables  []string
		}{
			{65, []string{"investments", "investment_valuations"}}, {70, []string{"investment_purchases"}},
			{71, []string{"investment_withdrawals", "investment_withdrawal_allocations"}},
			{72, []string{"investment_reference_revisions", "investment_reference_coverage"}},
			{73, []string{"investment_financial_revisions", "investment_financial_adjustments", "investment_purchase_funding"}},
			{74, []string{"investment_settlement_revisions", "investment_settlement_receipts"}},
			{75, []string{"investment_settlement_applications", "investment_settlement_prior_effects"}},
			{76, []string{"investment_result_classifications"}},
		} {
			for _, name := range family.tables {
				_, exists := objects[name]
				if exists != (version >= family.version) {
					return 0, fmt.Errorf("partial historical SQLite family: %s", name)
				}
			}
		}
	}
	if version >= 79 {
		if err := validateCancellation79Structure(q); err != nil {
			return 0, err
		}
		if err := validateCancellation79Receipts(q); err != nil {
			return 0, err
		}
	}
	if version >= 80 {
		if err := validateReversal80Structure(q, version >= 82); err != nil {
			return 0, err
		}
		if err := validateReversal80Receipts(q); err != nil {
			return 0, err
		}
	}
	if version >= 81 {
		if err := validateRecordCancellation81Structure(q); err != nil {
			return 0, err
		}
		if err := validateRecordCancellation81Receipts(q); err != nil {
			return 0, err
		}
	}
	if err := sqlite79Integrity(q); err != nil {
		return 0, err
	}
	return version, nil
}

func sqlite79Integrity(q sqlite79Reader) error {
	rows, err := q.QueryContext(context.Background(), `PRAGMA integrity_check`)
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			rows.Close()
			return err
		}
		if value != "ok" {
			rows.Close()
			return fmt.Errorf("SQLite integrity check failed")
		}
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("SQLite integrity check incomplete")
	}
	rows, err = q.QueryContext(context.Background(), `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("SQLite foreign key integrity check failed")
	}
	return rows.Err()
}

func preflightInvestmentCancellations79(tx *sql.Tx) error {
	version, err := sqlite79Inspect(tx)
	if err != nil {
		return err
	}
	if version != 77 {
		return fmt.Errorf("migration 79 requires intact schema 77")
	}
	return nil
}

// preflightInvestmentReversals80 runs inside the migration transaction: the
// registry must already carry marker 79 and no reversal object may exist.
func preflightInvestmentReversals80(tx *sql.Tx) error {
	version, err := sqlite79Inspect(tx)
	if err != nil {
		return err
	}
	if version != 79 {
		return fmt.Errorf("migration 80 requires intact schema 79")
	}
	return nil
}

// preflightInvestmentRecordCancellations81 runs inside the migration
// transaction: the registry must already carry marker 80.
func preflightInvestmentRecordCancellations81(tx *sql.Tx) error {
	version, err := sqlite79Inspect(tx)
	if err != nil {
		return err
	}
	if version != 80 {
		return fmt.Errorf("migration 81 requires intact schema 80")
	}
	return nil
}

// preflightInvestmentPurchaseReversals82 runs inside the migration
// transaction: the registry must already carry marker 81 and the schema-80
// reversal family (including every historical receipt) must be intact.
func preflightInvestmentPurchaseReversals82(tx *sql.Tx) error {
	version, err := sqlite79Inspect(tx)
	if err != nil {
		return err
	}
	if version != 81 {
		return fmt.Errorf("migration 82 requires intact schema 81")
	}
	return nil
}

func validateRecordCancellation81Structure(q sqlite79Reader) error {
	objects, err := sqlite79Schema(q)
	if err != nil {
		return err
	}
	expected := map[string]bool{}
	for _, want := range recordCancellation81Objects() {
		expected[want.name] = true
		got, ok := objects[want.name]
		if !ok || got.kind != want.kind || got.table != want.table || sqlite79NormalizeDDL(got.ddl) != sqlite79NormalizeDDL(want.ddl) {
			return fmt.Errorf("SQLite record cancellation schema diverges at %s", want.name)
		}
	}
	for name, o := range objects {
		if (strings.Contains(name, "recordcancel81") || o.table == "investment_record_cancellations") && !expected[name] {
			return fmt.Errorf("unexpected record cancellation schema object: %s", name)
		}
	}
	return nil
}

func validateRecordCancellation81Receipts(q sqlite79Reader) error {
	var bad int
	if err := q.QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM investment_record_cancellations r WHERE NOT COALESCE((`+recordCancellation81Relation("r", false)+`) AND (`+recordCancellation81JSON("r")+`),0))`).Scan(&bad); err != nil {
		return err
	}
	if bad != 0 {
		return fmt.Errorf("SQLite record cancellation receipt evidence diverges")
	}
	// A lot can never carry both the funded cancellation of schema 79 and the
	// record-only cancellation of schema 81.
	if err := q.QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM investment_record_cancellations r JOIN investment_cancellations c ON c.workspace_id=r.workspace_id AND c.investment_id=r.investment_id)`).Scan(&bad); err != nil {
		return err
	}
	if bad != 0 {
		return fmt.Errorf("SQLite record cancellation overlaps a funded cancellation")
	}
	return nil
}

func validateReversal80Structure(q sqlite79Reader, current bool) error {
	objects, err := sqlite79Schema(q)
	if err != nil {
		return err
	}
	expected := map[string]bool{}
	for _, want := range reversal80Objects() {
		if current && isReversal82Dropped(want.name) {
			continue
		}
		expected[want.name] = true
		got, ok := objects[want.name]
		if !ok || got.kind != want.kind || got.table != want.table || sqlite79NormalizeDDL(got.ddl) != sqlite79NormalizeDDL(want.ddl) {
			return fmt.Errorf("SQLite reversal schema diverges at %s", want.name)
		}
	}
	if current {
		for _, want := range reversal82Objects() {
			expected[want.name] = true
			got, ok := objects[want.name]
			if !ok || got.kind != want.kind || got.table != want.table || sqlite79NormalizeDDL(got.ddl) != sqlite79NormalizeDDL(want.ddl) {
				return fmt.Errorf("SQLite reversal schema diverges at %s", want.name)
			}
		}
	}
	for name, o := range objects {
		if (strings.Contains(name, "reversal80") || strings.Contains(name, "reversal82") || o.table == "investment_purchase_reversals") && !expected[name] {
			return fmt.Errorf("unexpected reversal schema object: %s", name)
		}
	}
	return nil
}

func isReversal82Dropped(name string) bool {
	for _, dropped := range reversal82Drops() {
		if dropped == name {
			return true
		}
	}
	return false
}

func validateReversal80Receipts(q sqlite79Reader) error {
	var bad int
	if err := q.QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM investment_purchase_reversals r WHERE NOT COALESCE((`+reversal80Relation("r", false)+`) AND (`+reversal80JSON("r")+`),0))`).Scan(&bad); err != nil {
		return err
	}
	if bad != 0 {
		return fmt.Errorf("SQLite reversal receipt evidence diverges")
	}
	if err := q.QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM investment_purchase_reversals a JOIN investment_purchase_reversals b ON b.workspace_id=a.workspace_id AND b.id<>a.id AND ((length(a.original_transaction_id)>0 AND a.original_transaction_id IN(b.original_transaction_id,b.compensation_transaction_id)) OR (length(a.compensation_transaction_id)>0 AND a.compensation_transaction_id IN(b.original_transaction_id,b.compensation_transaction_id))))`).Scan(&bad); err != nil {
		return err
	}
	if bad != 0 {
		return fmt.Errorf("SQLite reversal receipt identities overlap")
	}
	return nil
}

func validateCancellation79Structure(q sqlite79Reader) error {
	objects, err := sqlite79Schema(q)
	if err != nil {
		return err
	}
	expected := map[string]bool{}
	for _, want := range cancellation79Objects() {
		expected[want.name] = true
		got, ok := objects[want.name]
		if !ok || got.kind != want.kind || got.table != want.table || sqlite79NormalizeDDL(got.ddl) != sqlite79NormalizeDDL(want.ddl) {
			return fmt.Errorf("SQLite cancellation schema diverges at %s", want.name)
		}
	}
	for name, o := range objects {
		if (strings.Contains(name, "cancellation79") || o.table == "investment_cancellations") && !expected[name] {
			return fmt.Errorf("unexpected cancellation schema object: %s", name)
		}
	}
	return nil
}

func validateCancellation79Receipts(q sqlite79Reader) error {
	var bad int
	if err := q.QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM investment_cancellations c WHERE NOT COALESCE((`+cancellation79Relation("c", false)+`) AND (`+cancellation79JSON("c")+`),0))`).Scan(&bad); err != nil {
		return err
	}
	if bad != 0 {
		return fmt.Errorf("SQLite cancellation receipt evidence diverges")
	}
	if err := q.QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM investment_cancellations a JOIN investment_cancellations b ON b.workspace_id=a.workspace_id AND b.id<>a.id AND (a.original_transaction_id=b.compensation_transaction_id OR a.compensation_transaction_id=b.original_transaction_id))`).Scan(&bad); err != nil {
		return err
	}
	if bad != 0 {
		return fmt.Errorf("SQLite cancellation receipt identities overlap")
	}
	return nil
}

// ValidateSQLiteRuntimeSchema performs no migrations, checkpoints or repairs.
// The URI is rebuilt from the path, never from caller-supplied DSN pragmas.
func ValidateSQLiteRuntimeSchema(dbURL string) error {
	path, err := sqlite79Path(dbURL)
	if err != nil {
		return err
	}
	return validateSQLiteRuntimePath(path)
}

func validateSQLiteRuntimePath(path string) error {
	if path == ":memory:" {
		return fmt.Errorf("schema inspection requires an existing SQLite file")
	}
	if _, err := os.Stat(path); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", sqliteBackupURI(path, "mode=ro&cache=private"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = sqlite79Inspect(tx)
	return err
}

func sqlite79Path(dbURL string) (string, error) {
	if isSQLiteMemoryDSN(dbURL) {
		return ":memory:", nil
	}
	if !strings.HasPrefix(dbURL, "file:") {
		p, _, _ := strings.Cut(dbURL, "?")
		return p, nil
	}
	u, err := url.Parse(dbURL)
	if err != nil {
		return "", err
	}
	path := u.Path
	if u.Opaque != "" {
		path, err = url.PathUnescape(u.Opaque)
	}
	if err != nil {
		return "", err
	}
	if path == "" || u.Host != "" {
		return "", fmt.Errorf("invalid SQLite file URI")
	}
	return path, nil
}

func sqlite79OpeningDSN(dsn string) (string, error) {
	base, raw, _ := strings.Cut(dsn, "?")
	params, err := url.ParseQuery(raw)
	if err != nil {
		return "", err
	}
	pragmas := params["_pragma"]
	delete(params, "_pragma")
	for _, p := range pragmas {
		fields := strings.FieldsFunc(p, func(r rune) bool { return r == '(' || r == '=' })
		if len(fields) == 0 {
			return "", fmt.Errorf("empty SQLite opening pragma")
		}
		name := strings.ToLower(strings.TrimSpace(fields[0]))
		switch name {
		case "journal_mode": // WAL is configured only after backup and revalidation.
		case "foreign_keys", "defer_foreign_keys", "synchronous", "busy_timeout", "wal_autocheckpoint", "temp_store":
			params.Add("_pragma", p)
		default:
			return "", fmt.Errorf("unsupported SQLite opening pragma: %s", name)
		}
	}
	return base + "?" + params.Encode(), nil
}

func publishSQLiteRuntime79(db *sql.DB, guard *sqliteGuardConnector) error {
	version, err := sqlite79Inspect(db)
	if err != nil {
		return err
	}
	// The publish gate tracks the registry head: a runtime database is only
	// served after the full chain (now through schema 82) has been applied
	// and revalidated.
	if version != 82 {
		return fmt.Errorf("runtime requires SQLite schema 82")
	}
	return publishSQLiteGuard(db, guard)
}
