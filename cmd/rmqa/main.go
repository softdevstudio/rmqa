// Command rmqa checks a software release against a manifest before it is
// published: every download present, the right program for each platform
// inside each archive, the version inside the program, licenses included,
// checksums correct.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/softdevstudio/rmqa"
)

// Version is the rmqa version. scripts/check-version.sh keeps it equal to
// the VERSION file.
const Version = "0.1.1"

const usage = `rmqa checks a software release before you publish it.

Usage:
  rmqa check [DIR]       check the release files in DIR (default: dist)
  rmqa init [DIR]        write a manifest from a release that is known to be good
  rmqa inspect FILE...   show what is inside archives and programs
  rmqa version           print the rmqa version

Check options:
  --manifest FILE        the manifest (default: rmqa.toml)
  --version V            the version the release must carry
                         (default: $RMQA_VERSION, then the manifest)
  --github OWNER/REPO[@TAG]
                         check a release on GitHub instead of a folder
  --format F             text (default), json, github or markdown
  --strict               fail on warnings too
  --verbose              show notes as well as problems
  --no-run               never run the programs, even when they fit this machine

Exit status: 0 the release is ready, 1 it has problems, 2 rmqa couldn't run.
More: https://rmqa.com/quick-start/
`

type exitError struct {
	code int
	msg  string
}

func (e exitError) Error() string { return e.msg }

func main() {
	code := run(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "check":
		err = cmdCheck(args[1:], stdout)
	case "init":
		err = cmdInit(args[1:], stdout)
	case "inspect":
		err = cmdInspect(args[1:], stdout)
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "rmqa "+Version)
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "rmqa: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	if err == nil {
		return 0
	}
	var ee exitError
	if errors.As(err, &ee) {
		if ee.msg != "" {
			fmt.Fprintln(stderr, "rmqa: "+ee.msg)
		}
		return ee.code
	}
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintln(stderr, "rmqa: "+err.Error())
	return 2
}

// parse lets flags come before or after the positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, exitError{2, err.Error() + " (see rmqa help)"}
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func cmdCheck(args []string, stdout io.Writer) (err error) {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	manifest := fs.String("manifest", "rmqa.toml", "")
	version := fs.String("version", "", "")
	gh := fs.String("github", "", "")
	format := fs.String("format", "text", "")
	strict := fs.Bool("strict", false, "")
	verbose := fs.Bool("verbose", false, "")
	noRun := fs.Bool("no-run", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return exitError{2, "check takes one folder"}
	}
	switch *format {
	case "text", "json", "github", "markdown":
	default:
		return exitError{2, "--format must be text, json, github or markdown"}
	}
	if *format == "github" {
		// In GitHub Actions only annotations reach the run's summary, so a
		// problem that stops the check is repeated as one.
		defer func() {
			var ee exitError
			if errors.As(err, &ee) && ee.code == 2 && ee.msg != "" {
				fmt.Fprintf(stdout, "::error title=rmqa could not check the release::%s\n", strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(ee.msg))
			}
		}()
	}
	m, err := rmqa.LoadManifest(*manifest)
	if err != nil {
		return exitError{2, err.Error()}
	}
	v := *version
	if v == "" {
		v = os.Getenv("RMQA_VERSION")
	}
	var rel rmqa.Release
	if *gh != "" {
		if len(pos) > 0 {
			return exitError{2, "give a folder or --github, not both"}
		}
		g, err := rmqa.FetchGitHub(*gh)
		if err != nil {
			return exitError{2, err.Error()}
		}
		if v == "" && m.Version == "" && m.VersionFile == "" {
			v = g.Tag
		}
		rel = g
	} else {
		dir := "dist"
		if len(pos) == 1 {
			dir = pos[0]
		}
		d, err := rmqa.OpenDir(dir)
		if err != nil {
			return exitError{2, err.Error()}
		}
		rel = d
	}
	res, err := rmqa.Check(m, rel, rmqa.Options{Version: v, NoRun: *noRun})
	if err != nil {
		return exitError{2, err.Error()}
	}
	switch *format {
	case "json":
		if err := rmqa.WriteJSON(stdout, res, *strict); err != nil {
			return err
		}
	case "github":
		rmqa.WriteGitHub(stdout, res, *strict)
		if p := os.Getenv("GITHUB_STEP_SUMMARY"); p != "" {
			if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				rmqa.WriteMarkdown(f, res, *strict)
				f.Close()
			}
		}
	case "markdown":
		rmqa.WriteMarkdown(stdout, res, *strict)
	default:
		rmqa.WriteText(stdout, res, *verbose, *strict)
	}
	if !res.Ready(*strict) {
		return exitError{1, ""}
	}
	return nil
}

func cmdInit(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	project := fs.String("project", "", "")
	version := fs.String("version", "", "")
	out := fs.String("o", "", "")
	gh := fs.String("github", "", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	var rel rmqa.Release
	if *gh != "" {
		g, err := rmqa.FetchGitHub(*gh)
		if err != nil {
			return exitError{2, err.Error()}
		}
		if *version == "" {
			*version = g.Tag
		}
		rel = g
	} else {
		dir := "dist"
		if len(pos) == 1 {
			dir = pos[0]
		} else if len(pos) > 1 {
			return exitError{2, "init takes one folder"}
		}
		d, err := rmqa.OpenDir(dir)
		if err != nil {
			return exitError{2, err.Error()}
		}
		rel = d
	}
	text, err := rmqa.Init(rel, rmqa.InitOptions{Project: *project, Version: *version})
	if err != nil {
		return exitError{2, err.Error()}
	}
	// Make sure what was written reads back and passes on the release it
	// came from; a manifest that fails its own release is no use.
	m, err := rmqa.ParseManifest(text, ".")
	if err != nil {
		return exitError{2, "internal error, the generated manifest doesn't parse: " + err.Error()}
	}
	res, err := rmqa.Check(m, rel, rmqa.Options{NoRun: true})
	if err != nil {
		return exitError{2, "internal error: " + err.Error()}
	}
	if res.Errors > 0 {
		var b strings.Builder
		rmqa.WriteText(&b, res, false, false)
		text += "# Warning: this release doesn't pass its own manifest yet. Edit the\n# manifest or fix the release:\n#\n#   " + strings.ReplaceAll(strings.TrimSpace(b.String()), "\n", "\n#   ") + "\n"
	}
	if *out == "" {
		fmt.Fprint(stdout, text)
		return nil
	}
	if _, err := os.Stat(*out); err == nil {
		return exitError{2, *out + " already exists; remove it or pick another name"}
	}
	return os.WriteFile(*out, []byte(text), 0o644)
}

func cmdInspect(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return exitError{2, "inspect needs at least one file"}
	}
	for i, p := range pos {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return exitError{2, err.Error()}
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(stdout, "%s  %d bytes  sha256 %s\n", filepath.Base(p), len(data), hex.EncodeToString(sum[:]))
		if info, ok := rmqa.Identify(data); ok {
			fmt.Fprintf(stdout, "  program: %s for %s\n", info.Format, info.String())
			continue
		}
		entries, err := rmqa.OpenArchive(p, data)
		if err != nil {
			fmt.Fprintf(stdout, "  not a program, and not an archive rmqa can open (%v)\n", err)
			continue
		}
		for _, e := range entries {
			switch {
			case e.IsDir:
				fmt.Fprintf(stdout, "  %-11s %s/\n", modeString(e), strings.TrimSuffix(e.Name, "/"))
			case e.Link != "":
				fmt.Fprintf(stdout, "  %-11s %s -> %s\n", modeString(e), e.Name, e.Link)
			default:
				what := ""
				if info, ok := rmqa.Identify(e.Data); ok {
					what = "  " + info.Format + " " + info.String()
				}
				fmt.Fprintf(stdout, "  %-11s %s  %d bytes%s\n", modeString(e), e.Name, e.Size, what)
			}
			if e.Unsafe != "" {
				fmt.Fprintf(stdout, "              unsafe: %s\n", e.Unsafe)
			}
		}
	}
	return nil
}

func modeString(e rmqa.Entry) string {
	if !e.ModeKnown {
		return "(no mode)"
	}
	return e.Mode.String()
}
