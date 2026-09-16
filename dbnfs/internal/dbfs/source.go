// Package dbfs presents database catalog information as a read-only
// go-billy filesystem that go-nfs can serve.
//
// Layout:
//
//	/README.txt
//	/<kind>/                       one directory per Kind (table, view, sproc, ...)
//	/<kind>/<OWNER>.<NAME>/        one directory per object
//	/<kind>/<OWNER>.<NAME>/<file>  generated files, e.g. ddl.sql, rows.csv
//	/schema/<OWNER>/<file>         per-schema files
//
// The package knows nothing about SQL; a Source supplies names and bytes.
package dbfs

import (
	"context"
	"strings"
)

// Kind describes one top-level meta folder.
type Kind struct {
	Dir         string   // directory name at the root, e.g. "table"
	Description string   // shown in README.txt
	Files       []string // file names inside each object directory
}

// Object identifies a catalog object.
type Object struct {
	Owner string
	Name  string
}

// Source provides catalog data. Implementations must be safe for concurrent
// use and must not modify the database.
type Source interface {
	// Kinds returns the object kinds; it must return the same value every time.
	Kinds() []Kind
	// SchemaFiles lists the file names inside /schema/<OWNER>/.
	SchemaFiles() []string

	// Schemas lists visible schema names, sorted.
	Schemas(ctx context.Context) ([]string, error)
	// Objects lists objects of a kind, sorted by owner then name.
	Objects(ctx context.Context, kind string) ([]Object, error)

	// SchemaFile and ObjectFile generate file content. limit is the maximum
	// number of bytes the result may contain; implementations should stop
	// early rather than exceed it.
	SchemaFile(ctx context.Context, owner, file string, limit int) ([]byte, error)
	ObjectFile(ctx context.Context, kind string, obj Object, file string, limit int) ([]byte, error)
}

// SchemaDir is the name of the per-schema meta folder.
const SchemaDir = "schema"

// encodeComponent makes an identifier safe to use as (part of) a file name.
// Oracle quoted identifiers may contain '/' and '.', which would break paths
// or make OWNER.NAME ambiguous; '%' is encoded so decoding is unambiguous.
func encodeComponent(s string) string {
	if !strings.ContainsAny(s, "%/.\x00") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '%':
			b.WriteString("%25")
		case '/':
			b.WriteString("%2F")
		case '.':
			b.WriteString("%2E")
		case 0:
			b.WriteString("%00")
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// decodeComponent reverses encodeComponent; it rejects anything
// encodeComponent could not have produced, so each object has exactly one name.
func decodeComponent(s string) (string, bool) {
	if s == "" || strings.ContainsAny(s, "/.\x00") {
		return "", false
	}
	if !strings.Contains(s, "%") {
		return s, true
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			b.WriteByte(s[i])
			continue
		}
		if i+2 >= len(s) {
			return "", false
		}
		switch s[i+1 : i+3] {
		case "25":
			b.WriteByte('%')
		case "2F":
			b.WriteByte('/')
		case "2E":
			b.WriteByte('.')
		case "00":
			b.WriteByte(0)
		default:
			return "", false
		}
		i += 2
	}
	return b.String(), true
}

// objectEntryName is the directory name for an object.
func objectEntryName(o Object) string {
	return encodeComponent(o.Owner) + "." + encodeComponent(o.Name)
}

// parseObjectEntryName reverses objectEntryName.
func parseObjectEntryName(s string) (Object, bool) {
	owner, name, ok := strings.Cut(s, ".")
	if !ok {
		return Object{}, false
	}
	o, ok1 := decodeComponent(owner)
	n, ok2 := decodeComponent(name)
	if !ok1 || !ok2 {
		return Object{}, false
	}
	return Object{Owner: o, Name: n}, true
}
