package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// isNumericType reports whether an Oracle column holds a number whose exact
// decimal text we want to preserve.
func isNumericType(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "NUMBER", "NUMERIC", "DECIMAL", "DEC", "INTEGER", "INT", "SMALLINT", "FLOAT", "REAL", "DOUBLE PRECISION":
		return true
	}
	return false
}

// scanDest returns a scan destination for one column.
//
// NUMBER is scanned as text so that values wider than a float64 (Oracle
// NUMBER carries up to 38 significant digits) survive intact. Everything else
// is scanned into an interface and normalised afterwards, which keeps the tool
// working for column types the driver maps in its own way.
func scanDest(ct *sql.ColumnType) any {
	if ct != nil && isNumericType(ct.DatabaseTypeName()) {
		return new(sql.NullString)
	}
	return new(any)
}

// normalize converts a scanned value into something directly renderable:
// nil, string, bool, or json.Number.
func normalize(dest any, timeFormat string) (any, error) {
	switch d := dest.(type) {
	case *sql.NullString:
		if !d.Valid {
			return nil, nil
		}
		return numberOrString(d.String), nil
	case *any:
		return normalizeAny(*d, timeFormat)
	default:
		return nil, fmt.Errorf("unexpected scan destination %T", dest)
	}
}

func normalizeAny(v any, timeFormat string) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		return x, nil
	case bool:
		return x, nil
	case []byte:
		return base64.StdEncoding.EncodeToString(x), nil
	case time.Time:
		return x.Format(timeFormat), nil
	case int64:
		return json.Number(fmt.Sprintf("%d", x)), nil
	case float64:
		return numberOrString(fmt.Sprintf("%v", x)), nil
	case float32:
		return numberOrString(fmt.Sprintf("%v", x)), nil
	case fmt.Stringer:
		return x.String(), nil
	default:
		return fmt.Sprintf("%v", x), nil
	}
}

// numberOrString returns a json.Number when the text is a valid JSON number,
// and a plain string otherwise. Oracle happily produces forms such as ".5" or
// "1.5E+21" that JSON does not accept verbatim, so a few are repaired first.
func numberOrString(s string) any {
	t := strings.TrimSpace(s)
	if t == "" {
		return s
	}

	neg := false
	body := t
	switch body[0] {
	case '+':
		body = body[1:]
	case '-':
		neg = true
		body = body[1:]
	}
	if body == "" {
		return s
	}
	if strings.HasPrefix(body, ".") {
		body = "0" + body
	}
	body = strings.TrimSuffix(body, ".")
	if neg {
		body = "-" + body
	}

	if !json.Valid([]byte(body)) {
		return s
	}
	var probe json.Number
	if err := json.Unmarshal([]byte(body), &probe); err != nil {
		return s
	}
	return json.Number(body)
}

// renderText flattens a normalised value into its text form, used by the
// delimited writer.
func renderText(v any, nullText string) string {
	switch x := v.(type) {
	case nil:
		return nullText
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", x)
	}
}
