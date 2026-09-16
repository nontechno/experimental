package config

import (
	"strings"
	"testing"
)

func str(t *testing.T, o hObject, path string) string {
	t.Helper()
	sc, ok := o.lookup(path).(scalar)
	if !ok {
		t.Fatalf("%s: not a scalar: %#v", path, o.lookup(path))
	}
	return sc.text
}

func TestHOCONSyntax(t *testing.T) {
	t.Setenv("DBNFS_HOCON_X", "envval")
	o, err := parseHOCON(`
# comment
// another comment
a.b.c = 1          # trailing comment
a { d : "two", e = three }
"quoted.key" = x
url = "jdbc:oracle:thin:@//h:1/s"   // comment after a URL containing //
arr = [ "x", y
  , 3, ${?DBNFS_HOCON_UNSET}, ${DBNFS_HOCON_X} ]
emptyarr = []
opt = ${?DBNFS_HOCON_UNSET}
env = ${DBNFS_HOCON_X}
obj = { k = v }
obj { k2 = v2 }
over = 1
over = 2
raw = """line1
"line2" \n"""
`)
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]string{
		"a.b.c": "1", "a.d": "two", "a.e": "three", "url": "jdbc:oracle:thin:@//h:1/s",
		"env": "envval", "obj.k": "v", "obj.k2": "v2", "over": "2", "raw": "line1\n\"line2\" \\n",
	}
	for p, want := range checks {
		if got := str(t, o, p); got != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}
	if _, ok := o["quoted.key"]; !ok {
		t.Error("quoted key was split")
	}
	if o.lookup("opt") != nil {
		t.Error("unset optional substitution should leave key unset")
	}
	arr := o.lookup("arr").(hArray)
	var got []string
	for _, s := range arr {
		got = append(got, s.text)
	}
	if strings.Join(got, "|") != "x|y|3|envval" {
		t.Errorf("arr = %v", got)
	}
	if len(o.lookup("emptyarr").(hArray)) != 0 {
		t.Error("emptyarr not empty")
	}
}

func TestHOCONRootBraces(t *testing.T) {
	o, err := parseHOCON(`{ database { url = "u" } }`)
	if err != nil {
		t.Fatal(err)
	}
	if str(t, o, "database.url") != "u" {
		t.Error("root braces")
	}
}

func TestHOCONScalarOverridesObjectAndBack(t *testing.T) {
	o, err := parseHOCON("a { b = 1 }\na = 5\n")
	if err != nil {
		t.Fatal(err)
	}
	if str(t, o, "a") != "5" {
		t.Error("scalar should override object")
	}
}

func TestHOCONErrors(t *testing.T) {
	bad := map[string]string{
		"unterminated string": "a = \"abc\n",
		"concatenation":       "a = foo bar\n",
		"include":             "include \"other.conf\"\n",
		"plus equals":         "a += 1\n",
		"missing brace":       "a { b = 1\n",
		"extra brace":         "a = 1 }\n",
		"bad escape":          `a = "\q"`,
		"bad unicode":         `a = "\u12"`,
		"lone surrogate":      `a = "\ud83d"`,
		"required env":        "a = ${DBNFS_HOCON_NOT_SET}\n",
		"nested array":        "a = [[1]]\n",
		"object in array":     "a = [{b = 1}]\n",
		"empty key segment":   "a..b = 1\n",
		"no separator":        "a 1\n",
		"unterminated raw":    `a = """abc`,
		"garbage after root":  "{ a = 1 } b = 2",
		"unquoted special":    "a = x?y\n",
		"path substitution":   "a = ${b c}\n",
		"invalid utf8":        "a = \"\xff\"\n",
	}
	for name, text := range bad {
		if _, err := parseHOCON(text); err == nil {
			t.Errorf("%s: expected error", name)
		} else if !strings.Contains(err.Error(), "line") && name != "invalid utf8" {
			t.Errorf("%s: error without line number: %v", name, err)
		}
	}
}
