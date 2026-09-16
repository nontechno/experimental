package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"dbnfs/internal/config"
	"dbnfs/internal/dbfs"
)

// Source exposes an Oracle data dictionary. Every statement runs inside a
// transaction that is rolled back, and every statement uses bind variables
// for names; the only identifiers placed in SQL text are the owner, table
// and column names read back from the dictionary for rows.csv, and those go
// through quoteIdent.
type Source struct {
	db    *sql.DB
	ex    config.Export
	log   *slog.Logger
	kinds []kindDef
	byDir map[string]*kindDef
}

type fileGen func(ctx context.Context, tx *sql.Tx, o dbfs.Object, limit int) ([]byte, error)

type file struct {
	name string
	gen  fileGen
	// ddl marks generators that call DBMS_METADATA, which may need to write
	// to session-private work tables; see objectFile.
	ddl bool
}

type kindDef struct {
	dir, description string
	listSQL          func(ownerFilter string) string // must select (owner, name)
	listArgs         []any                           // appended after the owner filter binds
	files            []file
}

var _ dbfs.Source = (*Source)(nil)

// New builds a Source. db should come from Open.
func New(db *sql.DB, ex config.Export, log *slog.Logger) *Source {
	s := &Source{db: db, ex: ex, log: log, byDir: map[string]*kindDef{}}

	allObjects := func(objectType string) (func(string) string, []any) {
		return func(f string) string {
			return `SELECT owner, object_name FROM all_objects WHERE ` + f +
				` AND object_type = :t ORDER BY owner, object_name`
		}, []any{objectType}
	}
	source := func(sourceType string) file {
		return file{name: "source.sql", gen: s.sourceGen(sourceType)}
	}

	tableList := func(f string) string {
		return `SELECT owner, table_name FROM all_tables WHERE ` + f + `
		   AND dropped = 'NO' AND nested = 'NO' AND secondary = 'N'
		   AND (iot_type IS NULL OR iot_type = 'IOT')
		 ORDER BY owner, table_name`
	}

	rows := []file{}
	if ex.MaxRows > 0 {
		rows = append(rows, file{name: "rows.csv", gen: s.rowsCSV})
	}

	viewList, viewArgs := allObjects("VIEW")
	procList, procArgs := allObjects("PROCEDURE")
	funcList, funcArgs := allObjects("FUNCTION")
	pkgList, pkgArgs := allObjects("PACKAGE")
	seqList, seqArgs := allObjects("SEQUENCE")
	trgList, trgArgs := allObjects("TRIGGER")
	synList, synArgs := allObjects("SYNONYM")

	s.kinds = []kindDef{
		{dir: "table", description: "tables", listSQL: tableList, files: append([]file{
			{name: "columns.tsv", gen: s.columnsTSV},
			{name: "constraints.tsv", gen: s.constraintsTSV},
			{name: "indexes.tsv", gen: s.indexesTSV},
			{name: "ddl.sql", gen: s.ddlGen("TABLE"), ddl: true},
		}, rows...)},
		{dir: "view", description: "views", listSQL: viewList, listArgs: viewArgs, files: append([]file{
			{name: "columns.tsv", gen: s.columnsTSV},
			{name: "ddl.sql", gen: s.ddlGen("VIEW"), ddl: true},
		}, rows...)},
		{dir: "sproc", description: "standalone stored procedures", listSQL: procList, listArgs: procArgs, files: []file{
			{name: "arguments.tsv", gen: s.argumentsTSV(false)},
			source("PROCEDURE"),
		}},
		{dir: "function", description: "standalone functions", listSQL: funcList, listArgs: funcArgs, files: []file{
			{name: "arguments.tsv", gen: s.argumentsTSV(false)},
			source("FUNCTION"),
		}},
		{dir: "package", description: "PL/SQL packages", listSQL: pkgList, listArgs: pkgArgs, files: []file{
			{name: "arguments.tsv", gen: s.argumentsTSV(true)},
			{name: "spec.sql", gen: s.sourceGen("PACKAGE")},
			{name: "body.sql", gen: s.sourceGen("PACKAGE BODY")},
		}},
		{dir: "sequence", description: "sequences", listSQL: seqList, listArgs: seqArgs, files: []file{
			{name: "info.txt", gen: s.sequenceInfo},
		}},
		{dir: "trigger", description: "triggers", listSQL: trgList, listArgs: trgArgs, files: []file{
			{name: "info.txt", gen: s.triggerInfo},
			source("TRIGGER"),
		}},
		{dir: "synonym", description: "private synonyms", listSQL: synList, listArgs: synArgs, files: []file{
			{name: "info.txt", gen: s.synonymInfo},
		}},
	}
	for i := range s.kinds {
		s.byDir[s.kinds[i].dir] = &s.kinds[i]
	}
	return s
}

// Kinds implements dbfs.Source.
func (s *Source) Kinds() []dbfs.Kind {
	out := make([]dbfs.Kind, len(s.kinds))
	for i, k := range s.kinds {
		names := make([]string, len(k.files))
		for j, f := range k.files {
			names[j] = f.name
		}
		out[i] = dbfs.Kind{Dir: k.dir, Description: k.description, Files: names}
	}
	return out
}

// SchemaFiles implements dbfs.Source.
func (s *Source) SchemaFiles() []string { return []string{"objects.tsv"} }

// ownerFilter returns a predicate on column restricting it to the exported
// schemas, plus its bind values. Configured schema names are always bound,
// never concatenated.
func (s *Source) ownerFilter(column string) (string, []any) {
	if len(s.ex.Schemas) == 0 {
		// ORACLE_MAINTAINED exists from 12.1; list schemas explicitly on older releases.
		return column + ` IN (SELECT username FROM all_users WHERE oracle_maintained = 'N')`, nil
	}
	binds := make([]string, len(s.ex.Schemas))
	args := make([]any, len(s.ex.Schemas))
	for i, name := range s.ex.Schemas {
		binds[i] = ":s" + strconv.Itoa(i)
		args[i] = name
	}
	return column + " IN (" + strings.Join(binds, ", ") + ")", args
}

// Schemas implements dbfs.Source.
func (s *Source) Schemas(ctx context.Context) ([]string, error) {
	filter, args := s.ownerFilter("username")
	var out []string
	err := inTx(ctx, s.db, true, func(tx *sql.Tx) error {
		return scanStrings(ctx, tx, nil, func(_ []string, v []sql.NullString) (bool, error) {
			out = append(out, v[0].String)
			return len(out) < s.ex.MaxObjects, nil
		}, `SELECT username FROM all_users WHERE `+filter+` ORDER BY username`, args...)
	})
	return out, err
}

// Objects implements dbfs.Source.
func (s *Source) Objects(ctx context.Context, kind string) ([]dbfs.Object, error) {
	k, ok := s.byDir[kind]
	if !ok {
		return nil, fmt.Errorf("unknown kind %q", kind)
	}
	filter, args := s.ownerFilter("owner")
	args = append(args, k.listArgs...)
	var out []dbfs.Object
	truncated := false
	err := inTx(ctx, s.db, true, func(tx *sql.Tx) error {
		return scanStrings(ctx, tx, nil, func(_ []string, v []sql.NullString) (bool, error) {
			if len(out) >= s.ex.MaxObjects {
				truncated = true
				return false, nil
			}
			out = append(out, dbfs.Object{Owner: v[0].String, Name: v[1].String})
			return true, nil
		}, k.listSQL(filter), args...)
	})
	if truncated {
		s.log.Warn("listing truncated; raise export.maxObjects or narrow export.schemas",
			"kind", kind, "maxObjects", s.ex.MaxObjects)
	}
	return out, err
}

// SchemaFile implements dbfs.Source.
func (s *Source) SchemaFile(ctx context.Context, owner, name string, limit int) ([]byte, error) {
	if name != "objects.tsv" {
		return nil, fmt.Errorf("unknown schema file %q", name)
	}
	var out []byte
	err := inTx(ctx, s.db, true, func(tx *sql.Tx) (err error) {
		out, err = queryTSV(ctx, tx, limit, s.ex.MaxObjects, `
			SELECT object_type, object_name, subobject_name, status,
			       TO_CHAR(created, 'YYYY-MM-DD"T"HH24:MI:SS') AS created,
			       TO_CHAR(last_ddl_time, 'YYYY-MM-DD"T"HH24:MI:SS') AS last_ddl_time
			  FROM all_objects
			 WHERE owner = :o
			 ORDER BY object_type, object_name, subobject_name`, owner)
		return err
	})
	return out, err
}

// ObjectFile implements dbfs.Source.
func (s *Source) ObjectFile(ctx context.Context, kind string, o dbfs.Object, name string, limit int) ([]byte, error) {
	k, ok := s.byDir[kind]
	if !ok {
		return nil, fmt.Errorf("unknown kind %q", kind)
	}
	for _, f := range k.files {
		if f.name != name {
			continue
		}
		var out []byte
		run := func(readOnly bool) error {
			return inTx(ctx, s.db, readOnly, func(tx *sql.Tx) (err error) {
				out, err = f.gen(ctx, tx, o, limit)
				return err
			})
		}
		err := run(true)
		if f.ddl && isReadOnlyViolation(err) {
			// DBMS_METADATA may use session-private work tables, which a
			// read-only transaction forbids. It is Oracle's own read-only
			// API and the transaction is still rolled back.
			s.log.Debug("retrying DBMS_METADATA outside a read-only transaction", "kind", kind, "object", o, "err", err)
			err = run(false)
		}
		return out, err
	}
	return nil, fmt.Errorf("unknown file %q for kind %q", name, kind)
}

// ---- generators ----

func (s *Source) columnsTSV(ctx context.Context, tx *sql.Tx, o dbfs.Object, limit int) ([]byte, error) {
	return queryTSV(ctx, tx, limit, 100_000, `
		SELECT TO_CHAR(column_id) AS column_id, column_name, data_type,
		       CASE WHEN char_used IS NOT NULL THEN TO_CHAR(char_length) || DECODE(char_used, 'C', ' CHAR', ' BYTE') END AS char_length,
		       TO_CHAR(data_precision) AS data_precision, TO_CHAR(data_scale) AS data_scale, nullable
		  FROM all_tab_columns
		 WHERE owner = :o AND table_name = :n
		 ORDER BY column_id`, o.Owner, o.Name)
}

func (s *Source) constraintsTSV(ctx context.Context, tx *sql.Tx, o dbfs.Object, limit int) ([]byte, error) {
	return queryTSV(ctx, tx, limit, 100_000, `
		SELECT c.constraint_name,
		       DECODE(c.constraint_type, 'P', 'PRIMARY KEY', 'U', 'UNIQUE', 'R', 'FOREIGN KEY',
		              'C', 'CHECK', 'V', 'VIEW CHECK OPTION', 'O', 'READ ONLY VIEW', c.constraint_type) AS constraint_type,
		       (SELECT LISTAGG(cc.column_name, ',') WITHIN GROUP (ORDER BY cc.position)
		          FROM all_cons_columns cc
		         WHERE cc.owner = c.owner AND cc.constraint_name = c.constraint_name
		           AND cc.table_name = c.table_name) AS columns,
		       c.r_owner, c.r_constraint_name, c.delete_rule, c.status, c.validated
		  FROM all_constraints c
		 WHERE c.owner = :o AND c.table_name = :n
		 ORDER BY c.constraint_type, c.constraint_name`, o.Owner, o.Name)
}

func (s *Source) indexesTSV(ctx context.Context, tx *sql.Tx, o dbfs.Object, limit int) ([]byte, error) {
	return queryTSV(ctx, tx, limit, 100_000, `
		SELECT i.owner, i.index_name, i.index_type, i.uniqueness, i.status,
		       (SELECT LISTAGG(ic.column_name || CASE WHEN ic.descend = 'DESC' THEN ' DESC' END, ',')
		               WITHIN GROUP (ORDER BY ic.column_position)
		          FROM all_ind_columns ic
		         WHERE ic.index_owner = i.owner AND ic.index_name = i.index_name) AS columns
		  FROM all_indexes i
		 WHERE i.table_owner = :o AND i.table_name = :n
		 ORDER BY i.owner, i.index_name`, o.Owner, o.Name)
}

func (s *Source) ddlGen(objectType string) fileGen {
	return func(ctx context.Context, tx *sql.Tx, o dbfs.Object, limit int) ([]byte, error) {
		var ddl sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT DBMS_METADATA.GET_DDL(:t, :n, :o) FROM dual`,
			objectType, o.Name, o.Owner).Scan(&ddl)
		if err != nil {
			return nil, err
		}
		text := strings.TrimSpace(ddl.String)
		if text == "" {
			return nil, errNoRows
		}
		return truncateText([]byte(text+";\n"), limit), nil
	}
}

func (s *Source) sourceGen(sourceType string) fileGen {
	return func(ctx context.Context, tx *sql.Tx, o dbfs.Object, limit int) ([]byte, error) {
		var b strings.Builder
		b.WriteString("CREATE OR REPLACE ")
		lines := 0
		err := scanStrings(ctx, tx, nil, func(_ []string, v []sql.NullString) (bool, error) {
			lines++
			b.WriteString(v[0].String)
			return b.Len() <= limit, nil
		}, `SELECT text FROM all_source WHERE owner = :o AND name = :n AND type = :t ORDER BY line`,
			o.Owner, o.Name, sourceType)
		if err != nil {
			return nil, err
		}
		if lines == 0 {
			return []byte(fmt.Sprintf("-- no %s source is visible to this user in ALL_SOURCE\n", strings.ToLower(sourceType))), nil
		}
		text := strings.TrimRight(b.String(), " \t\r\n")
		return truncateText([]byte(text+"\n/\n"), limit), nil
	}
}

func (s *Source) argumentsTSV(inPackage bool) fileGen {
	return func(ctx context.Context, tx *sql.Tx, o dbfs.Object, limit int) ([]byte, error) {
		if inPackage {
			return queryTSV(ctx, tx, limit, 100_000, `
				SELECT object_name, overload, TO_CHAR(position) AS position, argument_name,
				       data_type, in_out, type_owner, type_name, pls_type, defaulted
				  FROM all_arguments
				 WHERE owner = :o AND package_name = :n AND data_level = 0
				 ORDER BY object_name, overload NULLS FIRST, sequence`, o.Owner, o.Name)
		}
		return queryTSV(ctx, tx, limit, 100_000, `
			SELECT overload, TO_CHAR(position) AS position, argument_name,
			       data_type, in_out, type_owner, type_name, pls_type, defaulted
			  FROM all_arguments
			 WHERE owner = :o AND object_name = :n AND package_name IS NULL AND data_level = 0
			 ORDER BY overload NULLS FIRST, sequence`, o.Owner, o.Name)
	}
}

func (s *Source) sequenceInfo(ctx context.Context, tx *sql.Tx, o dbfs.Object, _ int) ([]byte, error) {
	return queryKV(ctx, tx, `
		SELECT TO_CHAR(min_value) AS min_value, TO_CHAR(max_value) AS max_value,
		       TO_CHAR(increment_by) AS increment_by, cycle_flag, order_flag,
		       TO_CHAR(cache_size) AS cache_size, TO_CHAR(last_number) AS last_number
		  FROM all_sequences
		 WHERE sequence_owner = :o AND sequence_name = :n`, o.Owner, o.Name)
}

func (s *Source) triggerInfo(ctx context.Context, tx *sql.Tx, o dbfs.Object, _ int) ([]byte, error) {
	return queryKV(ctx, tx, `
		SELECT trigger_type, triggering_event, base_object_type, table_owner, table_name,
		       column_name, referencing_names, status, action_type
		  FROM all_triggers
		 WHERE owner = :o AND trigger_name = :n`, o.Owner, o.Name)
}

func (s *Source) synonymInfo(ctx context.Context, tx *sql.Tx, o dbfs.Object, _ int) ([]byte, error) {
	return queryKV(ctx, tx, `
		SELECT table_owner, table_name, db_link
		  FROM all_synonyms
		 WHERE owner = :o AND synonym_name = :n`, o.Owner, o.Name)
}

// rowsCSV exports up to export.maxRows rows. Every column is converted to
// text in SQL so the output does not depend on driver type mapping or
// session NLS settings.
func (s *Source) rowsCSV(ctx context.Context, tx *sql.Tx, o dbfs.Object, limit int) ([]byte, error) {
	type column struct{ name, dataType string }
	var cols []column
	err := scanStrings(ctx, tx, nil, func(_ []string, v []sql.NullString) (bool, error) {
		cols = append(cols, column{v[0].String, v[1].String})
		return true, nil
	}, `SELECT column_name, data_type FROM all_tab_columns
	     WHERE owner = :o AND table_name = :n ORDER BY column_id`, o.Owner, o.Name)
	if err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, errNoRows
	}

	owner, err := quoteIdent(o.Owner)
	if err != nil {
		return nil, err
	}
	table, err := quoteIdent(o.Name)
	if err != nil {
		return nil, err
	}
	exprs := make([]string, len(cols))
	header := make([]string, len(cols))
	for i, c := range cols {
		qc, err := quoteIdent(c.name)
		if err != nil {
			return nil, err
		}
		exprs[i] = textExpr(qc, c.dataType, s.ex.LOBChars)
		header[i] = c.name
	}
	query := "SELECT " + strings.Join(exprs, ", ") + " FROM " + owner + "." + table + " WHERE ROWNUM <= :max_rows"

	out := newCSVBuffer(limit)
	if _, err := out.write(header); err != nil {
		return nil, err
	}
	record := make([]string, len(cols))
	err = scanStrings(ctx, tx, nil, func(_ []string, v []sql.NullString) (bool, error) {
		for i := range v {
			record[i] = v[i].String // NULL and '' are the same value in Oracle
		}
		return out.write(record)
	}, query, s.ex.MaxRows)
	if err != nil {
		return nil, err
	}
	return out.buf.Bytes(), nil
}

var fractionalPrecision = regexp.MustCompile(`^TIMESTAMP\((\d)\)`)

// textExpr returns a SQL expression that renders column col (already
// quoted) as VARCHAR2 in a stable, NLS-independent format.
func textExpr(col, dataType string, lobChars int) string {
	const iso = `YYYY-MM-DD"T"HH24:MI:SS`
	switch {
	case dataType == "VARCHAR2" || dataType == "NVARCHAR2" || dataType == "CHAR" || dataType == "NCHAR":
		return col
	case dataType == "NUMBER" || dataType == "FLOAT" || dataType == "BINARY_FLOAT" || dataType == "BINARY_DOUBLE":
		return "TO_CHAR(" + col + ", 'TM9', 'NLS_NUMERIC_CHARACTERS=''.,''')"
	case dataType == "DATE":
		return "TO_CHAR(" + col + ", '" + iso + "')"
	case strings.HasPrefix(dataType, "TIMESTAMP"):
		format := iso
		if m := fractionalPrecision.FindStringSubmatch(dataType); m == nil || m[1] != "0" {
			format += ".FF"
		}
		if strings.HasSuffix(dataType, " WITH TIME ZONE") {
			format += "TZH:TZM"
		}
		return "TO_CHAR(" + col + ", '" + format + "')"
	case strings.HasPrefix(dataType, "INTERVAL"):
		return "TO_CHAR(" + col + ")"
	case dataType == "RAW":
		return "RAWTOHEX(" + col + ")"
	case dataType == "ROWID":
		return "ROWIDTOCHAR(" + col + ")"
	case (dataType == "CLOB" || dataType == "NCLOB") && lobChars > 0:
		return "DBMS_LOB.SUBSTR(" + col + ", " + strconv.Itoa(lobChars) + ", 1)"
	case dataType == "CLOB" || dataType == "NCLOB":
		return "CASE WHEN " + col + " IS NULL THEN NULL ELSE '<" + dataType + ">' END"
	case dataType == "BLOB":
		return "CASE WHEN " + col + " IS NULL THEN NULL ELSE '<BLOB ' || TO_CHAR(DBMS_LOB.GETLENGTH(" + col + ")) || ' bytes>' END"
	default:
		// LONG, LONG RAW, BFILE, XMLTYPE, JSON, BOOLEAN, VECTOR, object types, ...
		// The column is not referenced at all: LONG cannot appear in expressions.
		return "'<" + sanitizeTypeName(dataType) + ">'"
	}
}

var unsafeTypeChars = regexp.MustCompile(`[^A-Za-z0-9_ ().,$#]`)

func sanitizeTypeName(t string) string {
	t = unsafeTypeChars.ReplaceAllString(t, "")
	if len(t) > 60 {
		t = t[:60]
	}
	return t
}

// truncateText cuts b to limit bytes, replacing the tail with a marker line.
func truncateText(b []byte, limit int) []byte {
	if len(b) <= limit {
		return b
	}
	const marker = "\n-- [dbnfs: truncated at export.maxFileBytes]\n"
	cut := limit - len(marker)
	if cut < 0 {
		cut = 0
	}
	return append(b[:cut:cut], marker...)
}
