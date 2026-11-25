package main

import (
	"fmt"
	"io"
	"strings"
)

type Status string

const (
	Pass Status = "PASS"
	Fail Status = "FAIL"
	Warn Status = "WARN"
	Skip Status = "SKIP"
	Info Status = "INFO"
)

type Result struct {
	Group, Name string
	Status      Status
	Detail      string
}

type table struct {
	Title  string
	Header []string
	Rows   [][]string
}

type Report struct {
	out     io.Writer
	color   bool
	group   string
	Results []Result
	Tables  []table
	Meta    [][]string
}

func (r *Report) Section(name string) {
	r.group = name
	fmt.Fprintf(r.out, "\n== %s ==\n", name)
}

func (r *Report) Doing(format string, a ...any) {
	fmt.Fprintf(r.out, "  ... %s\n", fmt.Sprintf(format, a...))
}

func (r *Report) Add(name string, s Status, format string, a ...any) {
	d := fmt.Sprintf(format, a...)
	r.Results = append(r.Results, Result{r.group, name, s, d})
	tag := fmt.Sprintf("[%s]", s)
	if r.color {
		c := map[Status]string{Pass: "32", Fail: "31", Warn: "33", Skip: "90", Info: "36"}[s]
		tag = "\x1b[" + c + "m" + tag + "\x1b[0m"
	}
	fmt.Fprintf(r.out, "  %s %s: %s\n", tag, name, clean(d))
}

// Check records PASS when err is nil, FAIL otherwise.
func (r *Report) Check(name string, err error, ok string) bool {
	if err != nil {
		r.Add(name, Fail, "%v", err)
		return false
	}
	r.Add(name, Pass, "%s", ok)
	return true
}

func (r *Report) Table(title string, header []string, rows [][]string) {
	r.Tables = append(r.Tables, table{title, header, rows})
	fmt.Fprintf(r.out, "  collected: %s (%d rows)\n", title, len(rows))
}

func (r *Report) Count(s Status) (n int) {
	for _, x := range r.Results {
		if x.Status == s {
			n++
		}
	}
	return
}

// clean strips control characters so firmware-supplied strings cannot inject
// terminal escapes or break the report layout.
func clean(s string) string {
	return strings.Map(func(c rune) rune {
		if c == '\n' || c == '\t' {
			return ' '
		}
		if c < 0x20 || (c >= 0x7f && c < 0xa0) {
			return -1
		}
		return c
	}, s)
}

func cell(s string) string {
	return strings.NewReplacer("|", "\\|", "<", "&lt;", ">", "&gt;", "`", "'").Replace(clean(s))
}

func writeTable(w *strings.Builder, header []string, rows [][]string) {
	line := func(c []string) {
		w.WriteString("|")
		for _, v := range c {
			w.WriteString(" " + cell(v) + " |")
		}
		w.WriteString("\n")
	}
	line(header)
	w.WriteString("|" + strings.Repeat("---|", len(header)) + "\n")
	for _, row := range rows {
		line(row)
	}
	w.WriteString("\n")
}

func (r *Report) Markdown() string {
	var w strings.Builder
	w.WriteString("# TPM verification report\n\n")
	writeTable(&w, []string{"Item", "Value"}, r.Meta)

	verdict := "All executed checks passed."
	if r.Count(Fail) > 0 {
		verdict = "**One or more checks FAILED.**"
	} else if r.Count(Warn) > 0 {
		verdict = "No failures, but there are warnings to review."
	}
	if r.Count(Skip) > 0 {
		verdict += " Skipped checks were not verified."
	}
	w.WriteString("## Summary\n\n" + verdict + "\n\n")
	writeTable(&w, []string{"PASS", "FAIL", "WARN", "SKIP", "INFO"}, [][]string{{
		fmt.Sprint(r.Count(Pass)), fmt.Sprint(r.Count(Fail)), fmt.Sprint(r.Count(Warn)),
		fmt.Sprint(r.Count(Skip)), fmt.Sprint(r.Count(Info))}})

	var bad [][]string
	for _, x := range r.Results {
		if x.Status == Fail || x.Status == Warn {
			bad = append(bad, []string{string(x.Status), x.Group, x.Name, x.Detail})
		}
	}
	if len(bad) > 0 {
		w.WriteString("## Needs attention\n\n")
		writeTable(&w, []string{"Status", "Section", "Check", "Detail"}, bad)
	}

	w.WriteString("## Results\n\n")
	group := ""
	var rows [][]string
	flush := func() {
		if len(rows) > 0 {
			w.WriteString("### " + group + "\n\n")
			writeTable(&w, []string{"Status", "Check", "Detail"}, rows)
			rows = nil
		}
	}
	for _, x := range r.Results {
		if x.Group != group {
			flush()
			group = x.Group
		}
		rows = append(rows, []string{string(x.Status), x.Name, x.Detail})
	}
	flush()

	w.WriteString("## Inventory\n\n")
	for _, t := range r.Tables {
		w.WriteString("### " + t.Title + "\n\n")
		writeTable(&w, t.Header, t.Rows)
	}

	w.WriteString(`## What this report does not prove

- It runs on the machine under test. A compromised OS or kernel can falsify every result. For real attestation, a remote verifier must choose the nonce and check the quote and EK chain off the host.
- The kernel resource manager (/dev/tpmrm0) hides other processes' transient objects and sessions, so the handle list is partial.
- Without ` + "`-ek-ca`" + `, the TPM's manufacturer identity is not verified.
- Event log replay shows the log is consistent with the PCRs, not that the measured components are ones you trust. Compare against known-good values.
- It does not test physical tamper resistance, TPM bus interposition, or certification claims.
`)
	return w.String()
}
