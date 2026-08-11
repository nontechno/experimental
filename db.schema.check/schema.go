package main

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

// Schema is the full picture of one Oracle schema.
type Schema struct {
	Owner     string     `json:"owner"`
	Database  DBInfo     `json:"database"`
	Tables    []*Table   `json:"tables"`
	Views     []*View    `json:"views"`
	Sequences []Sequence `json:"sequences,omitempty"`
}

// DBInfo describes the server we are talking to.
type DBInfo struct {
	Banner        string `json:"banner,omitempty"`
	InstanceName  string `json:"instanceName,omitempty"`
	DatabaseName  string `json:"databaseName,omitempty"`
	SessionUser   string `json:"sessionUser,omitempty"`
	CurrentSchema string `json:"currentSchema,omitempty"`
	Endpoint      string `json:"endpoint,omitempty"`
}

// Table is a relational table plus everything hanging off it.
type Table struct {
	Name        string        `json:"name"`
	Comment     string        `json:"comment,omitempty"`
	NumRows     *int64        `json:"numRows,omitempty"` // optimizer estimate, may be stale
	Temporary   bool          `json:"temporary,omitempty"`
	Partitioned bool          `json:"partitioned,omitempty"`
	Columns     []*Column     `json:"columns"`
	Constraints []*Constraint `json:"constraints,omitempty"`
	Indexes     []*Index      `json:"indexes,omitempty"`
}

// View is a view and its projected columns.
type View struct {
	Name    string    `json:"name"`
	Comment string    `json:"comment,omitempty"`
	Columns []*Column `json:"columns"`
}

// Column is one column of a table or view.
type Column struct {
	Name     string `json:"name"`
	Position int    `json:"position"`
	Type     string `json:"type"`     // rendered, e.g. VARCHAR2(50 CHAR)
	BaseType string `json:"baseType"` // raw DATA_TYPE
	Nullable bool   `json:"nullable"`
	Default  string `json:"default,omitempty"`
	Comment  string `json:"comment,omitempty"`
	Identity bool   `json:"identity,omitempty"`
}

// Constraint covers primary keys, unique keys, foreign keys and check clauses.
type Constraint struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"` // PRIMARY KEY | UNIQUE | FOREIGN KEY | CHECK
	Columns    []string `json:"columns,omitempty"`
	RefOwner   string   `json:"refOwner,omitempty"`
	RefTable   string   `json:"refTable,omitempty"`
	RefColumns []string `json:"refColumns,omitempty"`
	DeleteRule string   `json:"deleteRule,omitempty"`
	Status     string   `json:"status,omitempty"`
	Condition  string   `json:"condition,omitempty"`
}

// Index is a schema index and its key columns.
type Index struct {
	Name    string   `json:"name"`
	Unique  bool     `json:"unique"`
	Type    string   `json:"indexType,omitempty"`
	Status  string   `json:"status,omitempty"`
	Columns []string `json:"columns"`
}

// Sequence is a number generator.
type Sequence struct {
	Name        string `json:"name"`
	MinValue    string `json:"minValue,omitempty"`
	MaxValue    string `json:"maxValue,omitempty"`
	IncrementBy string `json:"incrementBy,omitempty"`
	Cycle       bool   `json:"cycle,omitempty"`
	CacheSize   string `json:"cacheSize,omitempty"`
	LastNumber  string `json:"lastNumber,omitempty"`
}

// ---------------------------------------------------------------------------
// Introspection
// ---------------------------------------------------------------------------

// Inspector reads schema metadata out of the Oracle data dictionary.
type Inspector struct {
	DB *sql.DB
}

// queryFirst runs the given queries in order and returns the first one that
// succeeds. Dictionary views gained columns over time (IDENTITY_COLUMN in 12.1,
// SEARCH_CONDITION_VC in 12.1, ...), so the richest query is listed first and
// progressively simpler ones act as fallbacks on older releases.
func (in *Inspector) queryFirst(ctx context.Context, args []any, queries ...string) (*sql.Rows, error) {
	var err error
	for _, q := range queries {
		var rows *sql.Rows
		rows, err = in.DB.QueryContext(ctx, q, args...)
		if err == nil {
			return rows, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, err
}

// CurrentSchema returns the schema that unqualified names resolve to.
func (in *Inspector) CurrentSchema(ctx context.Context) (string, error) {
	var owner string
	err := in.DB.QueryRowContext(ctx,
		`SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM dual`).Scan(&owner)
	if err != nil {
		return "", fmt.Errorf("determining current schema: %w", err)
	}
	return owner, nil
}

// Inspect gathers the whole schema. Optional pieces (comments, version banner)
// are best-effort: a missing privilege degrades the report but does not fail it.
func (in *Inspector) Inspect(ctx context.Context, owner string) (*Schema, error) {
	s := &Schema{Owner: owner}

	s.Database = in.dbInfo(ctx)

	tables, err := in.tables(ctx, owner)
	if err != nil {
		return nil, err
	}
	views, err := in.views(ctx, owner)
	if err != nil {
		return nil, err
	}

	byTable := make(map[string]*Table, len(tables))
	for _, t := range tables {
		byTable[t.Name] = t
	}
	byView := make(map[string]*View, len(views))
	for _, v := range views {
		byView[v.Name] = v
	}

	if err := in.columns(ctx, owner, byTable, byView); err != nil {
		return nil, err
	}
	if err := in.constraints(ctx, owner, byTable); err != nil {
		return nil, err
	}
	if err := in.indexes(ctx, owner, byTable); err != nil {
		return nil, err
	}
	in.comments(ctx, owner, byTable, byView) // best-effort

	if seqs, err := in.sequences(ctx, owner); err == nil {
		s.Sequences = seqs
	}

	s.Tables = tables
	s.Views = views
	return s, nil
}

func (in *Inspector) dbInfo(ctx context.Context) DBInfo {
	var info DBInfo

	_ = in.DB.QueryRowContext(ctx, `
		SELECT SYS_CONTEXT('USERENV','SESSION_USER'),
		       SYS_CONTEXT('USERENV','CURRENT_SCHEMA'),
		       SYS_CONTEXT('USERENV','DB_NAME'),
		       SYS_CONTEXT('USERENV','INSTANCE_NAME')
		FROM dual`).Scan(&info.SessionUser, &info.CurrentSchema, &info.DatabaseName, &info.InstanceName)

	// product_component_version is readable by PUBLIC; v$version usually is not.
	_ = in.DB.QueryRowContext(ctx, `
		SELECT product || ' ' || version
		FROM product_component_version
		WHERE UPPER(product) LIKE 'ORACLE%'
		FETCH FIRST 1 ROWS ONLY`).Scan(&info.Banner)

	return info
}

func (in *Inspector) tables(ctx context.Context, owner string) ([]*Table, error) {
	const q = `
		SELECT table_name, num_rows, temporary, partitioned
		FROM all_tables
		WHERE owner = :1
		ORDER BY table_name`

	rows, err := in.DB.QueryContext(ctx, q, owner)
	if err != nil {
		return nil, fmt.Errorf("listing tables: %w", err)
	}
	defer rows.Close()

	var out []*Table
	for rows.Next() {
		var (
			t           Table
			numRows     sql.NullInt64
			temporary   sql.NullString
			partitioned sql.NullString
		)
		if err := rows.Scan(&t.Name, &numRows, &temporary, &partitioned); err != nil {
			return nil, fmt.Errorf("scanning table row: %w", err)
		}
		if numRows.Valid {
			n := numRows.Int64
			t.NumRows = &n
		}
		t.Temporary = strings.EqualFold(strings.TrimSpace(temporary.String), "Y")
		t.Partitioned = strings.EqualFold(strings.TrimSpace(partitioned.String), "YES")
		out = append(out, &t)
	}
	return out, rows.Err()
}

func (in *Inspector) views(ctx context.Context, owner string) ([]*View, error) {
	const q = `SELECT view_name FROM all_views WHERE owner = :1 ORDER BY view_name`

	rows, err := in.DB.QueryContext(ctx, q, owner)
	if err != nil {
		return nil, fmt.Errorf("listing views: %w", err)
	}
	defer rows.Close()

	var out []*View
	for rows.Next() {
		var v View
		if err := rows.Scan(&v.Name); err != nil {
			return nil, fmt.Errorf("scanning view row: %w", err)
		}
		out = append(out, &v)
	}
	return out, rows.Err()
}

// columns fills in the columns of every table and view.
//
// DATA_DEFAULT is a LONG column. Some drivers and some privilege setups choke
// on it, so it is selected last and the whole query is retried without it.
func (in *Inspector) columns(ctx context.Context, owner string, tables map[string]*Table, views map[string]*View) error {
	rows, err := in.queryFirst(ctx, []any{owner},
		// 12.1+ with identity columns and defaults.
		`SELECT table_name, column_name, column_id, data_type, data_length,
		        data_precision, data_scale, char_length, char_used, nullable,
		        identity_column, data_default
		 FROM all_tab_cols
		 WHERE owner = :1 AND hidden_column = 'NO'
		 ORDER BY table_name, column_id`,
		// Same, without the LONG default.
		`SELECT table_name, column_name, column_id, data_type, data_length,
		        data_precision, data_scale, char_length, char_used, nullable,
		        identity_column, NULL AS data_default
		 FROM all_tab_cols
		 WHERE owner = :1 AND hidden_column = 'NO'
		 ORDER BY table_name, column_id`,
		// 11g: no IDENTITY_COLUMN.
		`SELECT table_name, column_name, column_id, data_type, data_length,
		        data_precision, data_scale, char_length, char_used, nullable,
		        'NO' AS identity_column, NULL AS data_default
		 FROM all_tab_columns
		 WHERE owner = :1
		 ORDER BY table_name, column_id`,
	)
	if err != nil {
		return fmt.Errorf("listing columns: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			tableName string
			c         Column
			colID     sql.NullInt64
			length    sql.NullInt64
			precision sql.NullInt64
			scale     sql.NullInt64
			charLen   sql.NullInt64
			charUsed  sql.NullString
			nullable  sql.NullString
			identity  sql.NullString
			def       sql.NullString
		)
		if err := rows.Scan(&tableName, &c.Name, &colID, &c.BaseType, &length,
			&precision, &scale, &charLen, &charUsed, &nullable, &identity, &def); err != nil {
			return fmt.Errorf("scanning column row: %w", err)
		}

		c.Position = int(colID.Int64)
		c.Nullable = strings.EqualFold(strings.TrimSpace(nullable.String), "Y")
		c.Identity = strings.EqualFold(strings.TrimSpace(identity.String), "YES")
		c.Default = strings.TrimSpace(def.String)
		c.Type = renderType(c.BaseType, length, precision, scale, charLen, charUsed)

		if t, ok := tables[tableName]; ok {
			t.Columns = append(t.Columns, &c)
		} else if v, ok := views[tableName]; ok {
			v.Columns = append(v.Columns, &c)
		}
	}
	return rows.Err()
}

// renderType reassembles the DDL spelling of a column type.
func renderType(base string, length, precision, scale, charLen sql.NullInt64, charUsed sql.NullString) string {
	switch strings.ToUpper(base) {
	case "VARCHAR2", "NVARCHAR2", "CHAR", "NCHAR", "VARCHAR":
		n := length.Int64
		unit := ""
		if strings.EqualFold(strings.TrimSpace(charUsed.String), "C") {
			n = charLen.Int64
			unit = " CHAR"
		} else if charUsed.Valid {
			unit = " BYTE"
		}
		return fmt.Sprintf("%s(%d%s)", base, n, unit)

	case "NUMBER":
		switch {
		case !precision.Valid && (!scale.Valid || scale.Int64 == 0):
			return base
		case !precision.Valid:
			return fmt.Sprintf("NUMBER(*,%d)", scale.Int64)
		case scale.Valid && scale.Int64 != 0:
			return fmt.Sprintf("NUMBER(%d,%d)", precision.Int64, scale.Int64)
		default:
			return fmt.Sprintf("NUMBER(%d)", precision.Int64)
		}

	case "FLOAT":
		if precision.Valid {
			return fmt.Sprintf("FLOAT(%d)", precision.Int64)
		}
		return base

	case "RAW":
		return fmt.Sprintf("RAW(%d)", length.Int64)

	case "UROWID":
		if length.Valid {
			return fmt.Sprintf("UROWID(%d)", length.Int64)
		}
		return base

	default:
		// TIMESTAMP/INTERVAL types already carry their precision in DATA_TYPE.
		return base
	}
}

func (in *Inspector) constraints(ctx context.Context, owner string, tables map[string]*Table) error {
	cols, consTables, err := in.constraintColumns(ctx, owner)
	if err != nil {
		return err
	}

	// SEARCH_CONDITION is LONG; SEARCH_CONDITION_VC (12.1+) is not. Fall back
	// to a NULL literal on older releases.
	rows, err := in.queryFirst(ctx, []any{owner},
		`SELECT constraint_name, table_name, constraint_type, r_owner,
		        r_constraint_name, delete_rule, status, search_condition_vc
		 FROM all_constraints
		 WHERE owner = :1
		   AND constraint_type IN ('P', 'U', 'R', 'C')
		   AND NOT (constraint_type = 'C' AND generated = 'GENERATED NAME')
		 ORDER BY table_name, constraint_type, constraint_name`,
		`SELECT constraint_name, table_name, constraint_type, r_owner,
		        r_constraint_name, delete_rule, status, NULL AS search_condition_vc
		 FROM all_constraints
		 WHERE owner = :1
		   AND constraint_type IN ('P', 'U', 'R', 'C')
		   AND NOT (constraint_type = 'C' AND generated = 'GENERATED NAME')
		 ORDER BY table_name, constraint_type, constraint_name`,
	)
	if err != nil {
		return fmt.Errorf("listing constraints: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			c          Constraint
			tableName  string
			typ        string
			rOwner     sql.NullString
			rName      sql.NullString
			deleteRule sql.NullString
			status     sql.NullString
			condition  sql.NullString
		)
		if err := rows.Scan(&c.Name, &tableName, &typ, &rOwner, &rName,
			&deleteRule, &status, &condition); err != nil {
			return fmt.Errorf("scanning constraint row: %w", err)
		}

		c.Type = constraintTypeName(typ)
		c.Columns = cols[consKey{owner, c.Name}]
		c.Status = strings.TrimSpace(status.String)
		c.Condition = strings.TrimSpace(condition.String)
		if typ == "R" {
			c.RefOwner = strings.TrimSpace(rOwner.String)
			c.DeleteRule = strings.TrimSpace(deleteRule.String)
			ref := consKey{c.RefOwner, strings.TrimSpace(rName.String)}
			c.RefColumns = cols[ref]
			c.RefTable = consTables[ref]
		}

		if t, ok := tables[tableName]; ok {
			t.Constraints = append(t.Constraints, &c)
		}
	}
	return rows.Err()
}

type consKey struct{ Owner, Name string }

// constraintColumns returns, for every relevant constraint, its ordered column
// list and the table it belongs to.
func (in *Inspector) constraintColumns(ctx context.Context, owner string) (map[consKey][]string, map[consKey]string, error) {
	// Also pull the columns of constraints referenced by this schema's foreign
	// keys, which may live in another owner.
	const q = `
		SELECT owner, constraint_name, table_name, column_name, position
		FROM all_cons_columns
		WHERE owner = :1
		   OR (owner, constraint_name) IN (
		        SELECT r_owner, r_constraint_name
		        FROM all_constraints
		        WHERE owner = :2 AND constraint_type = 'R' AND r_constraint_name IS NOT NULL)
		ORDER BY owner, constraint_name, position`

	rows, err := in.DB.QueryContext(ctx, q, owner, owner)
	if err != nil {
		return nil, nil, fmt.Errorf("listing constraint columns: %w", err)
	}
	defer rows.Close()

	cols := map[consKey][]string{}
	onTable := map[consKey]string{}
	for rows.Next() {
		var (
			k        consKey
			table    string
			column   sql.NullString
			position sql.NullInt64
		)
		if err := rows.Scan(&k.Owner, &k.Name, &table, &column, &position); err != nil {
			return nil, nil, fmt.Errorf("scanning constraint column row: %w", err)
		}
		onTable[k] = table
		if column.Valid {
			cols[k] = append(cols[k], column.String)
		}
	}
	return cols, onTable, rows.Err()
}

func constraintTypeName(t string) string {
	switch t {
	case "P":
		return "PRIMARY KEY"
	case "U":
		return "UNIQUE"
	case "R":
		return "FOREIGN KEY"
	case "C":
		return "CHECK"
	default:
		return t
	}
}

func (in *Inspector) indexes(ctx context.Context, owner string, tables map[string]*Table) error {
	const idxQ = `
		SELECT index_name, table_name, uniqueness, index_type, status
		FROM all_indexes
		WHERE table_owner = :1
		ORDER BY table_name, index_name`

	const colQ = `
		SELECT ic.index_name, ic.column_name, ic.descend
		FROM all_ind_columns ic
		WHERE ic.table_owner = :1
		ORDER BY ic.index_name, ic.column_position`

	colRows, err := in.DB.QueryContext(ctx, colQ, owner)
	if err != nil {
		return fmt.Errorf("listing index columns: %w", err)
	}
	idxCols := map[string][]string{}
	for colRows.Next() {
		var name, column string
		var descend sql.NullString
		if err := colRows.Scan(&name, &column, &descend); err != nil {
			colRows.Close()
			return fmt.Errorf("scanning index column row: %w", err)
		}
		if strings.EqualFold(strings.TrimSpace(descend.String), "DESC") {
			column += " DESC"
		}
		idxCols[name] = append(idxCols[name], column)
	}
	colRows.Close()
	if err := colRows.Err(); err != nil {
		return err
	}

	rows, err := in.DB.QueryContext(ctx, idxQ, owner)
	if err != nil {
		return fmt.Errorf("listing indexes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			idx        Index
			tableName  string
			uniqueness sql.NullString
			indexType  sql.NullString
			status     sql.NullString
		)
		if err := rows.Scan(&idx.Name, &tableName, &uniqueness, &indexType, &status); err != nil {
			return fmt.Errorf("scanning index row: %w", err)
		}
		idx.Unique = strings.EqualFold(strings.TrimSpace(uniqueness.String), "UNIQUE")
		idx.Type = strings.TrimSpace(indexType.String)
		idx.Status = strings.TrimSpace(status.String)
		idx.Columns = idxCols[idx.Name]

		if t, ok := tables[tableName]; ok {
			t.Indexes = append(t.Indexes, &idx)
		}
	}
	return rows.Err()
}

// comments is best-effort; failures are silently ignored.
func (in *Inspector) comments(ctx context.Context, owner string, tables map[string]*Table, views map[string]*View) {
	if rows, err := in.DB.QueryContext(ctx,
		`SELECT table_name, comments FROM all_tab_comments WHERE owner = :1 AND comments IS NOT NULL`, owner); err == nil {
		for rows.Next() {
			var name, comment string
			if rows.Scan(&name, &comment) != nil {
				continue
			}
			if t, ok := tables[name]; ok {
				t.Comment = comment
			} else if v, ok := views[name]; ok {
				v.Comment = comment
			}
		}
		rows.Close()
	}

	if rows, err := in.DB.QueryContext(ctx,
		`SELECT table_name, column_name, comments FROM all_col_comments WHERE owner = :1 AND comments IS NOT NULL`, owner); err == nil {
		for rows.Next() {
			var table, column, comment string
			if rows.Scan(&table, &column, &comment) != nil {
				continue
			}
			var cols []*Column
			if t, ok := tables[table]; ok {
				cols = t.Columns
			} else if v, ok := views[table]; ok {
				cols = v.Columns
			}
			for _, c := range cols {
				if c.Name == column {
					c.Comment = comment
					break
				}
			}
		}
		rows.Close()
	}
}

func (in *Inspector) sequences(ctx context.Context, owner string) ([]Sequence, error) {
	// NUMBER values here can be 28 digits wide, so fetch them as text.
	const q = `
		SELECT sequence_name,
		       TO_CHAR(min_value), TO_CHAR(max_value), TO_CHAR(increment_by),
		       cycle_flag, TO_CHAR(cache_size), TO_CHAR(last_number)
		FROM all_sequences
		WHERE sequence_owner = :1
		ORDER BY sequence_name`

	rows, err := in.DB.QueryContext(ctx, q, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Sequence
	for rows.Next() {
		var (
			s                                       Sequence
			minV, maxV, incr, cycle, cache, lastNum sql.NullString
		)
		if err := rows.Scan(&s.Name, &minV, &maxV, &incr, &cycle, &cache, &lastNum); err != nil {
			return nil, err
		}
		s.MinValue, s.MaxValue, s.IncrementBy = minV.String, maxV.String, incr.String
		s.CacheSize, s.LastNumber = cache.String, lastNum.String
		s.Cycle = strings.EqualFold(strings.TrimSpace(cycle.String), "Y")
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
