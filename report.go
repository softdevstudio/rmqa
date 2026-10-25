package rmqa

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// WriteText prints a result for a person at a terminal. Notes (info
// findings) appear only when verbose is set.
func WriteText(w io.Writer, r *Result, verbose, strict bool) {
	head := "Checking " + r.Source
	if r.Project != "" || r.Version != "" {
		head += " for " + strings.TrimSpace(r.Project+" "+r.Version)
	}
	fmt.Fprintln(w, head)
	fmt.Fprintln(w)
	width := 0
	for _, f := range r.Files {
		if len(f.Name) > width {
			width = len(f.Name)
		}
	}
	for _, f := range r.Files {
		status := "ok  "
		for _, x := range f.Findings {
			if x.Severity == SevWarning && status == "ok  " {
				status = "warn"
			}
			if x.Severity == SevError {
				status = "FAIL"
			}
		}
		plat := f.Platform
		if !f.Expected {
			plat = "(not in manifest)"
		}
		fmt.Fprintln(w, strings.TrimRight(fmt.Sprintf("  %s  %-*s  %s", status, width, f.Name, plat), " "))
		for _, x := range f.Findings {
			if x.Severity == SevInfo && !verbose {
				continue
			}
			mark := ""
			switch x.Severity {
			case SevWarning:
				mark = "warning: "
			case SevInfo:
				mark = "note: "
			}
			where := ""
			if x.Path != "" {
				where = x.Path + ": "
			}
			fmt.Fprintf(w, "          %s%s%s [%s]\n", mark, where, x.Message, x.Code)
		}
	}
	fmt.Fprintln(w)
	verdict := "Ready to publish"
	if !r.Ready(strict) {
		verdict = "Not ready"
	}
	fmt.Fprintf(w, "%s: %s, %s in %d files.\n", verdict, plural(r.Errors, "error"), plural(r.Warnings, "warning"), len(r.Files))
}

func plural(n int, s string) string {
	if n == 1 {
		return "1 " + s
	}
	return fmt.Sprintf("%d %ss", n, s)
}

// WriteJSON prints the result as JSON.
func WriteJSON(w io.Writer, r *Result, strict bool) error {
	out := struct {
		*Result
		Ready bool `json:"ready"`
	}{r, r.Ready(strict)}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// WriteGitHub prints GitHub Actions workflow commands, so each problem
// shows as an annotation on the run, followed by the plain text report.
func WriteGitHub(w io.Writer, r *Result, strict bool) {
	for _, f := range r.Findings {
		cmd := ""
		switch f.Severity {
		case SevError:
			cmd = "error"
		case SevWarning:
			cmd = "warning"
		default:
			continue
		}
		where := f.File
		if f.Path != "" {
			where += " > " + f.Path
		}
		fmt.Fprintf(w, "::%s title=rmqa %s::%s\n", cmd, f.Code, escapeGH(where+": "+f.Message))
	}
	WriteText(w, r, false, strict)
}

func escapeGH(s string) string {
	s = strings.ReplaceAll(s, "%", "%25")
	s = strings.ReplaceAll(s, "\r", "%0D")
	return strings.ReplaceAll(s, "\n", "%0A")
}

// WriteMarkdown prints a short summary for $GITHUB_STEP_SUMMARY or a pull
// request comment.
func WriteMarkdown(w io.Writer, r *Result, strict bool) {
	verdict := "✅ Ready to publish"
	if !r.Ready(strict) {
		verdict = "❌ Not ready"
	}
	fmt.Fprintf(w, "### rmqa: %s %s\n\n%s: %s, %s in %d files.\n\n", r.Project, r.Version, verdict, plural(r.Errors, "error"), plural(r.Warnings, "warning"), len(r.Files))
	fmt.Fprintln(w, "| File | Platform | Result |")
	fmt.Fprintln(w, "|---|---|---|")
	for _, f := range r.Files {
		var msgs []string
		for _, x := range f.Findings {
			if x.Severity == SevInfo {
				continue
			}
			p := ""
			if x.Path != "" {
				p = "`" + x.Path + "`: "
			}
			msgs = append(msgs, p+mdEscape(x.Message))
		}
		res := "ok"
		if len(msgs) > 0 {
			res = strings.Join(msgs, "<br>")
		}
		fmt.Fprintf(w, "| `%s` | %s | %s |\n", f.Name, f.Platform, res)
	}
}

func mdEscape(s string) string { return strings.ReplaceAll(s, "|", "\\|") }
