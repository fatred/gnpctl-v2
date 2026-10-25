// Package state is the SQLite store: the targets table (renderer read
// contract) and the inventory-source registry.
package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DefaultStaleWindow is the periodic rediscovery window (seconds), matching
// discover --periodic.
const DefaultStaleWindow = 3600

const schemaTargets = `
CREATE TABLE IF NOT EXISTS targets (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  address TEXT,
  port INTEGER,
  platform_hint TEXT,
  source TEXT,
  discovered INTEGER NOT NULL DEFAULT 0,
  last_discovery_run INTEGER NOT NULL DEFAULT 0,
  catalog_key TEXT,
  vendor TEXT,
  os TEXT,
  model TEXT,
  os_version TEXT,
  capabilities_hash TEXT,
  last_error TEXT
)`

const schemaSources = `
CREATE TABLE IF NOT EXISTS inventory_sources (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  type TEXT NOT NULL,
  parameters TEXT,
  credential_ref TEXT,
  last_poll INTEGER NOT NULL DEFAULT 0,
  last_error TEXT
)`

// StateError is a state-store integrity violation (e.g. duplicate source).
type StateError struct{ Message string }

func (e *StateError) Error() string { return e.Message }

// State owns the SQLite connection. Rows are map[string]any (SQL NULL -> nil,
// INTEGER -> int64) so callers read columns exactly like the Python dicts.
type State struct {
	db *sql.DB
}

// Open opens (creating if needed) the database in WAL mode, applies the schema
// and adds a mod_<name> column for every module name.
func Open(path string, modules []string) (*State, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	s := &State{db: db}
	// Python uses a single sqlite3 connection; keep one so the per-connection
	// PRAGMAs below apply and writes serialize instead of hitting SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	// Python's sqlite3.connect default timeout is 5s; keep concurrent writers waiting.
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, err
	}
	for _, ddl := range []string{schemaTargets, schemaSources} {
		if _, err := db.Exec(ddl); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err := s.EnsureModuleColumns(modules); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying connection.
func (s *State) Close() error { return s.db.Close() }

// ModuleColumn is the SQLite column for a module's per-target enum
// ("bgp-peers" -> "mod_bgp_peers").
func ModuleColumn(name string) string {
	return "mod_" + strings.ReplaceAll(name, "-", "_")
}

func nowOr(now *int64) int64 {
	if now == nil {
		return time.Now().Unix()
	}
	return *now
}

func intOrNil(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullIfEmpty(text string) any {
	if text == "" {
		return nil
	}
	return text
}

func queryRows(db *sql.DB, query string, args ...any) ([]map[string]any, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			row[c] = vals[i]
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func queryRow(db *sql.DB, query string, args ...any) (map[string]any, error) {
	rows, err := queryRows(db, query, args...)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

// ----- inventory sources -----

// AddSource registers an inventory source; a duplicate name is a StateError.
func (s *State) AddSource(name, typ string, parameters map[string]any, credentialRef string) error {
	params := parameters
	if params == nil {
		params = map[string]any{}
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		"INSERT INTO inventory_sources(name,type,parameters,credential_ref) VALUES(?,?,?,?)",
		name, typ, string(encoded), nullIfEmpty(credentialRef),
	)
	// Python catches sqlite3.IntegrityError instead of checking first: the insert
	// is the atomic duplicate test.
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return &StateError{Message: fmt.Sprintf("source '%s' already exists", name)}
	}
	return err
}

// RemoveSource deletes a source, reporting whether a row was removed. Targets
// are left intact.
func (s *State) RemoveSource(name string) (bool, error) {
	res, err := s.db.Exec("DELETE FROM inventory_sources WHERE name=?", name)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// GetSource returns the named source row, or nil when absent.
func (s *State) GetSource(name string) (map[string]any, error) {
	row, err := queryRow(s.db, "SELECT * FROM inventory_sources WHERE name=?", name)
	if err != nil || row == nil {
		return nil, err
	}
	return sourceRow(row)
}

// ListSources returns all sources ordered by name.
func (s *State) ListSources() ([]map[string]any, error) {
	rows, err := queryRows(s.db, "SELECT * FROM inventory_sources ORDER BY name")
	if err != nil {
		return nil, err
	}
	for i, row := range rows {
		if rows[i], err = sourceRow(row); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func sourceRow(row map[string]any) (map[string]any, error) {
	var params any = map[string]any{}
	if raw, ok := row["parameters"].(string); ok && raw != "" {
		if err := json.Unmarshal([]byte(raw), &params); err != nil {
			return nil, err
		}
	}
	row["parameters"] = params
	return row, nil
}

// RecordSourcePoll records a successful poll (updating last_poll and clearing
// last_error) or, when err is non-nil, only the error.
func (s *State) RecordSourcePoll(name string, pollErr error, now *int64) error {
	if pollErr == nil {
		_, err := s.db.Exec(
			"UPDATE inventory_sources SET last_poll=?, last_error=NULL WHERE name=?",
			nowOr(now), name,
		)
		return err
	}
	_, err := s.db.Exec(
		"UPDATE inventory_sources SET last_error=? WHERE name=?",
		pollErr.Error(), name,
	)
	return err
}

// ----- per-module enum columns -----

func (s *State) targetColumns() (map[string]bool, error) {
	rows, err := queryRows(s.db, "PRAGMA table_info(targets)")
	if err != nil {
		return nil, err
	}
	cols := make(map[string]bool, len(rows))
	for _, r := range rows {
		cols[r["name"].(string)] = true
	}
	return cols, nil
}

// EnsureModuleColumns adds a mod_<name> INTEGER column (default 0) for any
// module that lacks one. Additive: existing rows/columns are untouched.
func (s *State) EnsureModuleColumns(names []string) error {
	existing, err := s.targetColumns()
	if err != nil {
		return err
	}
	for _, name := range names {
		col := ModuleColumn(name)
		if existing[col] {
			continue
		}
		if _, err := s.db.Exec(fmt.Sprintf(`ALTER TABLE targets ADD COLUMN "%s" INTEGER NOT NULL DEFAULT 0`, col)); err != nil {
			return err
		}
		existing[col] = true
	}
	return nil
}

// SetModule sets a target's module enum (-1 force-off / 0 off / 1 on / 2
// force-on).
func (s *State) SetModule(target, name string, value int) error {
	_, err := s.db.Exec(
		fmt.Sprintf(`UPDATE targets SET "%s"=? WHERE name=?`, ModuleColumn(name)),
		value, target,
	)
	return err
}

// ModuleValue returns a target's module enum, or nil when the target is
// unknown.
func (s *State) ModuleValue(target, name string) (*int, error) {
	row, err := queryRow(s.db,
		fmt.Sprintf(`SELECT "%s" AS v FROM targets WHERE name=?`, ModuleColumn(name)),
		target,
	)
	if err != nil || row == nil {
		return nil, err
	}
	v, ok := row["v"].(int64)
	if !ok {
		return nil, nil
	}
	n := int(v)
	return &n, nil
}

// Enabled returns the names whose mod_ column in a target row is enabled (>= 1).
func Enabled(row map[string]any, names []string) []string {
	var enabled []string
	for _, name := range names {
		if v, _ := row[ModuleColumn(name)].(int64); v >= 1 {
			enabled = append(enabled, name)
		}
	}
	return enabled
}

// ReconcileModules sets discovery-owned columns (current 0/1) to the profile
// default; operator pins (-1 force-off / 2 force-on) are preserved. All updates
// commit together, as Python's single commit does.
func (s *State) ReconcileModules(target string, defaults map[string]int) error {
	names := make([]string, 0, len(defaults))
	for name := range defaults {
		names = append(names, name)
	}
	sort.Strings(names)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, name := range names {
		col := ModuleColumn(name)
		if _, err := tx.Exec(
			fmt.Sprintf(`UPDATE targets SET "%s"=? WHERE name=? AND "%s" IN (0, 1)`, col, col),
			defaults[name], target,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ----- targets -----

// UpsertTarget inserts a new target or refreshes inventory-owned fields,
// preserving discovery-owned fields. Nil address, port and platform_hint store
// SQL NULL (Python's None); non-nil empty strings stay "". Port is only
// overwritten when non-nil.
func (s *State) UpsertTarget(name string, address *string, port *int, platformHint *string, source string) error {
	existing, err := s.GetTarget(name)
	if err != nil {
		return err
	}
	if existing == nil {
		_, err = s.db.Exec(
			"INSERT INTO targets(name,address,port,platform_hint,source) VALUES(?,?,?,?,?)",
			name, address, intOrNil(port), platformHint, source,
		)
		return err
	}
	if port != nil {
		_, err = s.db.Exec(
			"UPDATE targets SET address=?, port=?, platform_hint=?, source=? WHERE name=?",
			address, *port, platformHint, source, name,
		)
		return err
	}
	_, err = s.db.Exec(
		"UPDATE targets SET address=?, platform_hint=?, source=? WHERE name=?",
		address, platformHint, source, name,
	)
	return err
}

// Enrichment carries the discovery-owned target fields written in one
// statement; an empty text value is stored as NULL, like Python's None (the
// pipeline passes "" for an absent identity value).
type Enrichment struct {
	Discovered       bool
	LastDiscoveryRun int64
	CatalogKey       string
	Vendor           string
	OS               string
	Model            string
	OSVersion        string
	CapabilitiesHash string
	Port             *int
}

// EnrichTarget atomically writes discovery-owned fields and clears last_error.
func (s *State) EnrichTarget(name string, e Enrichment) error {
	discovered := 0
	if e.Discovered {
		discovered = 1
	}
	_, err := s.db.Exec(
		`UPDATE targets SET
		   discovered=?, last_discovery_run=?, catalog_key=?,
		   vendor=?, os=?, model=?, os_version=?, capabilities_hash=?,
		   port=COALESCE(?, port), last_error=NULL
		 WHERE name=?`,
		discovered, e.LastDiscoveryRun, nullIfEmpty(e.CatalogKey),
		nullIfEmpty(e.Vendor), nullIfEmpty(e.OS), nullIfEmpty(e.Model),
		nullIfEmpty(e.OSVersion), nullIfEmpty(e.CapabilitiesHash),
		intOrNil(e.Port), name,
	)
	return err
}

// RecordTargetError records a connection/transport error without touching
// discovery state.
func (s *State) RecordTargetError(name string, targetErr error) error {
	_, err := s.db.Exec("UPDATE targets SET last_error=? WHERE name=?", targetErr.Error(), name)
	return err
}

// SetTargetPort persists the resolved effective gNMI port for a target.
func (s *State) SetTargetPort(name string, port int) error {
	_, err := s.db.Exec("UPDATE targets SET port=? WHERE name=?", port, name)
	return err
}

// GetTarget returns the named target row, or nil when absent.
func (s *State) GetTarget(name string) (map[string]any, error) {
	return queryRow(s.db, "SELECT * FROM targets WHERE name=?", name)
}

// AllTargets returns every target ordered by name.
func (s *State) AllTargets() ([]map[string]any, error) {
	return queryRows(s.db, "SELECT * FROM targets ORDER BY name")
}

// SelectTargets selects targets by mode ("default" undiscovered, "periodic"
// discovered and not run within window, "force" all), or just the named
// target when target is non-nil.
func (s *State) SelectTargets(mode string, window int64, target *string, now *int64) ([]map[string]any, error) {
	if target != nil {
		return queryRows(s.db, "SELECT * FROM targets WHERE name=?", *target)
	}
	switch mode {
	case "default":
		return queryRows(s.db, "SELECT * FROM targets WHERE discovered=0 ORDER BY name")
	case "periodic":
		return queryRows(s.db,
			"SELECT * FROM targets WHERE discovered=1 AND last_discovery_run < ? ORDER BY name",
			nowOr(now)-window)
	case "force":
		return queryRows(s.db, "SELECT * FROM targets ORDER BY name")
	}
	return nil, &StateError{Message: fmt.Sprintf("unknown selection mode '%s'", mode)}
}

// ShowTargets returns targets matching any of the requested filters, or all
// targets when no filter is set.
func (s *State) ShowTargets(withErrors, undiscovered, stale bool, window int64, now *int64) ([]map[string]any, error) {
	if !(withErrors || undiscovered || stale) {
		return s.AllTargets()
	}
	query := "SELECT * FROM targets WHERE "
	var clauses []string
	var params []any
	if withErrors {
		clauses = append(clauses, "(last_error IS NOT NULL AND last_error != '')")
	}
	if undiscovered {
		clauses = append(clauses, "discovered=0")
	}
	if stale {
		clauses = append(clauses, "(discovered=1 AND last_discovery_run < ?)")
		params = append(params, nowOr(now)-window)
	}
	for i, c := range clauses {
		if i > 0 {
			query += " OR "
		}
		query += c
	}
	return queryRows(s.db, query+" ORDER BY name", params...)
}
