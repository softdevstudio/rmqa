package rmqa

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Severity of a finding.
type Severity string

const (
	SevError   Severity = "error"
	SevWarning Severity = "warning"
	SevInfo    Severity = "info"
)

// Finding codes. They are stable: scripts and CI rules may match on them.
const (
	CodeMissingArtifact   = "missing-artifact"
	CodeExtraFile         = "extra-file"
	CodeEmptyFile         = "empty-file"
	CodeSize              = "size-out-of-range"
	CodeArchiveCorrupt    = "archive-corrupt"
	CodeArchiveLayout     = "archive-layout"
	CodeUnsafePath        = "unsafe-path"
	CodeForbiddenFile     = "forbidden-file"
	CodeJunkFile          = "junk-file"
	CodeMissingFile       = "missing-file"
	CodeMissingBinary     = "missing-binary"
	CodeNotExecutable     = "not-executable"
	CodeWrongOS           = "wrong-os"
	CodeWrongArch         = "wrong-arch"
	CodeNoExecBit         = "no-exec-bit"
	CodeExecBitUnknown    = "exec-bit-unknown"
	CodeVersionMissing    = "version-missing"
	CodeVersionRun        = "version-run-mismatch"
	CodeRunSkipped        = "run-skipped"
	CodeDuplicateBinary   = "duplicate-binary"
	CodeChecksumMismatch  = "checksum-mismatch"
	CodeChecksumMissing   = "checksum-missing-entry"
	CodeChecksumStale     = "checksum-stale-entry"
	CodeChecksumFormat    = "checksum-format"
	CodeOptionalMissing   = "optional-missing"
	CodeVersionUnknown    = "version-unknown"
	CodeUnsupportedFormat = "unsupported-archive"
)

// Finding is one problem, or one note, about the release.
type Finding struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	File     string   `json:"file,omitempty"` // the release file
	Path     string   `json:"path,omitempty"` // a path inside it
	Message  string   `json:"message"`
}

// FileResult is the outcome for one file of the release.
type FileResult struct {
	Name     string    `json:"name"`
	Platform string    `json:"platform,omitempty"`
	Present  bool      `json:"present"`
	Expected bool      `json:"expected"`
	Size     int64     `json:"size"`
	SHA256   string    `json:"sha256,omitempty"`
	Findings []Finding `json:"findings"`
}

// Result of checking a release.
type Result struct {
	Project  string       `json:"project,omitempty"`
	Version  string       `json:"version"`
	Source   string       `json:"source"`
	Files    []FileResult `json:"files"`
	Findings []Finding    `json:"findings"` // every finding, in file order
	Errors   int          `json:"errors"`
	Warnings int          `json:"warnings"`
}

// Ready reports whether the release passed: no errors, and no warnings
// either when strict is set.
func (r *Result) Ready(strict bool) bool {
	return r.Errors == 0 && (!strict || r.Warnings == 0)
}

// Release is a set of files to check.
type Release interface {
	Names() []string
	Size(name string) int64
	Read(name string) ([]byte, error)
	Describe() string
}

// DirRelease is a release in a local folder.
type DirRelease struct {
	Dir   string
	names []string
	sizes map[string]int64
}

// OpenDir lists the regular files at the top of dir.
func OpenDir(dir string) (*DirRelease, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	r := &DirRelease{Dir: dir, sizes: map[string]int64{}}
	for _, e := range ents {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		r.names = append(r.names, e.Name())
		r.sizes[e.Name()] = info.Size()
	}
	sort.Strings(r.names)
	return r, nil
}

func (r *DirRelease) Names() []string        { return r.names }
func (r *DirRelease) Size(name string) int64 { return r.sizes[name] }
func (r *DirRelease) Read(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(r.Dir, name))
}
func (r *DirRelease) Describe() string { return r.Dir }

// Options change how a check runs.
type Options struct {
	Version string // the version to expect; empty means the manifest decides
	NoRun   bool   // never run binaries, even when they fit this machine
}

// junk is clutter that sometimes lands in archives; it's harmless but
// shows the archive was packed by hand.
var junk = []string{".DS_Store", "__MACOSX/*", "Thumbs.db", "desktop.ini", "*~", "*.swp"}

type binRecord struct {
	file, path, platform string
}

// Check compares a release with a manifest.
func Check(m *Manifest, rel Release, opt Options) (*Result, error) {
	version, err := m.ResolveVersion(opt.Version)
	if err != nil {
		return nil, err
	}
	expected, err := m.Expand(version)
	if err != nil {
		return nil, err
	}
	res := &Result{Project: m.Project, Version: version, Source: rel.Describe()}
	present := map[string]bool{}
	for _, n := range rel.Names() {
		present[n] = true
	}
	claimed := map[string]bool{}
	results := map[string]*FileResult{}
	var order []string
	add := func(name string, f Finding) {
		f.File = name
		results[name].Findings = append(results[name].Findings, f)
	}
	binaries := map[string][]binRecord{}
	var checksumFiles []Expected

	for _, e := range expected {
		claimed[e.Name] = true
		fr := &FileResult{Name: e.Name, Expected: true, Present: present[e.Name], Findings: []Finding{}}
		if e.Platform != nil {
			fr.Platform = e.Platform.ID()
		}
		results[e.Name] = fr
		order = append(order, e.Name)
		if !fr.Present {
			if e.Spec.Optional {
				add(e.Name, Finding{Code: CodeOptionalMissing, Severity: SevInfo, Message: "optional file not in this release"})
			} else {
				msg := "missing from the release"
				if e.Platform != nil {
					msg = "missing from the release (the " + e.Platform.ID() + " download)"
				}
				add(e.Name, Finding{Code: CodeMissingArtifact, Severity: SevError, Message: msg})
			}
			continue
		}
		fr.Size = rel.Size(e.Name)
		if fr.Size == 0 {
			add(e.Name, Finding{Code: CodeEmptyFile, Severity: SevError, Message: "the file is empty (0 bytes)"})
			continue
		}
		if e.Spec.MinSize > 0 && fr.Size < e.Spec.MinSize {
			add(e.Name, Finding{Code: CodeSize, Severity: SevError, Message: fmt.Sprintf("%s, smaller than the minimum of %s", human(fr.Size), human(e.Spec.MinSize))})
		}
		if e.Spec.MaxSize > 0 && fr.Size > e.Spec.MaxSize {
			add(e.Name, Finding{Code: CodeSize, Severity: SevError, Message: fmt.Sprintf("%s, larger than the maximum of %s", human(fr.Size), human(e.Spec.MaxSize))})
		}
		data, err := rel.Read(e.Name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name, err)
		}
		sum := sha256.Sum256(data)
		fr.SHA256 = hex.EncodeToString(sum[:])
		if e.Spec.Checksums != "" {
			checksumFiles = append(checksumFiles, e)
			continue
		}
		if e.Spec.Binary {
			checkBinaryEntry(add, e.Name, "", data, e.Binaries[0].Platform, false, false, version, e.Spec, opt, binaries)
			continue
		}
		kind := archiveKind(e.Name)
		if kind == "" {
			if len(e.Files) > 0 || len(e.Binaries) > 0 {
				add(e.Name, Finding{Code: CodeUnsupportedFormat, Severity: SevError, Message: "lists files to look for inside, but the name doesn't end in a known archive type (.tar.gz, .tgz, .tar.bz2, .tar, .zip, .gz)"})
			}
			continue
		}
		entries, err := OpenArchive(e.Name, data)
		if err != nil {
			code := CodeArchiveCorrupt
			if kind == "unsupported" {
				code = CodeUnsupportedFormat
				if len(e.Files) == 0 && len(e.Binaries) == 0 {
					continue // nothing was asked of it, so not being able to open it is fine
				}
			}
			add(e.Name, Finding{Code: code, Severity: SevError, Message: "can't be read: " + err.Error()})
			continue
		}
		checkArchive(add, e, entries, version, opt, m, binaries)
	}

	// Binaries that are byte-for-byte the same in two platforms' downloads
	// usually mean one build was copied into the wrong archive.
	for _, recs := range binaries {
		plats := map[string]bool{}
		for _, r := range recs {
			plats[r.platform] = true
		}
		if len(recs) < 2 || len(plats) < 2 {
			continue
		}
		var where []string
		for _, r := range recs {
			where = append(where, r.file+":"+r.path)
		}
		for _, r := range recs {
			add(r.file, Finding{Code: CodeDuplicateBinary, Severity: SevError, Path: r.path, Message: "is byte-for-byte the same program as " + strings.Join(others(where, r.file+":"+r.path), ", ") + ", which is meant for a different platform"})
		}
	}

	// Files nobody declared.
	for _, n := range rel.Names() {
		if claimed[n] {
			continue
		}
		fr := &FileResult{Name: n, Present: true, Size: rel.Size(n), Findings: []Finding{}}
		results[n] = fr
		order = append(order, n)
		if m.ExtraFiles == "ignore" {
			continue
		}
		sev := SevError
		if m.ExtraFiles == "warn" {
			sev = SevWarning
		}
		add(n, Finding{Code: CodeExtraFile, Severity: sev, Message: "is in the release but not in the manifest"})
	}

	for _, e := range checksumFiles {
		data, err := rel.Read(e.Name)
		if err != nil {
			return nil, err
		}
		checkChecksums(add, e, data, rel, checksumFiles)
	}

	for _, n := range order {
		fr := results[n]
		sortFindings(fr.Findings)
		for _, f := range fr.Findings {
			res.Findings = append(res.Findings, f)
			switch f.Severity {
			case SevError:
				res.Errors++
			case SevWarning:
				res.Warnings++
			}
		}
		res.Files = append(res.Files, *fr)
	}
	return res, nil
}

func others(all []string, me string) []string {
	var out []string
	for _, s := range all {
		if s != me {
			out = append(out, s)
		}
	}
	return out
}

func sortFindings(fs []Finding) {
	rank := map[Severity]int{SevError: 0, SevWarning: 1, SevInfo: 2}
	sort.SliceStable(fs, func(i, j int) bool { return rank[fs[i].Severity] < rank[fs[j].Severity] })
}

type adder func(name string, f Finding)

func checkArchive(add adder, e Expected, entries []Entry, version string, opt Options, m *Manifest, binaries map[string][]binRecord) {
	for _, en := range entries {
		if en.Unsafe != "" {
			add(e.Name, Finding{Code: CodeUnsafePath, Severity: SevError, Path: en.Name, Message: "would be unpacked outside the target folder: " + en.Unsafe})
		}
	}
	stripped, top, err := stripRoot(entries, e.Root)
	if err != nil {
		add(e.Name, Finding{Code: CodeArchiveLayout, Severity: SevError, Message: err.Error()})
		return
	}
	byName := map[string]*Entry{}
	for i := range stripped {
		en := &stripped[i]
		byName[en.Name] = en
		if en.IsDir {
			continue
		}
		if matchAny(m.Forbid, en.Name) {
			add(e.Name, Finding{Code: CodeForbiddenFile, Severity: SevError, Path: en.Name, Message: "should never ship in a release (it looks like a secret, a key or private config)"})
		} else if matchAny(junk, en.Name) {
			add(e.Name, Finding{Code: CodeJunkFile, Severity: SevWarning, Path: en.Name, Message: "is clutter from the machine that packed the archive"})
		}
	}
	_ = top
	for _, pat := range e.Files {
		var hits []*Entry
		for i := range stripped {
			en := &stripped[i]
			if en.IsDir {
				continue
			}
			if ok, _ := path.Match(pat, en.Name); ok || en.Name == pat {
				hits = append(hits, en)
			}
		}
		if len(hits) == 0 {
			add(e.Name, Finding{Code: CodeMissingFile, Severity: SevError, Path: pat, Message: "is not in the archive" + nearMiss(pat, stripped)})
			continue
		}
		for _, h := range hits {
			if h.Link == "" && len(h.Data) == 0 {
				add(e.Name, Finding{Code: CodeEmptyFile, Severity: SevError, Path: h.Name, Message: "is in the archive but empty (0 bytes)"})
			}
		}
	}
	for _, b := range e.Binaries {
		en, ok := byName[b.Path]
		if !ok || en.IsDir {
			add(e.Name, Finding{Code: CodeMissingBinary, Severity: SevError, Path: b.Path, Message: "the program is not in the archive" + nearMiss(b.Path, stripped)})
			continue
		}
		if en.Link != "" {
			add(e.Name, Finding{Code: CodeMissingBinary, Severity: SevError, Path: b.Path, Message: "is a link to " + en.Link + ", not the program itself"})
			continue
		}
		modeKnown := en.ModeKnown
		execBit := en.Mode&0o111 != 0
		checkBinaryEntry(add, e.Name, b.Path, en.Data, b.Platform, modeKnown, execBit, version, e.Spec, opt, binaries)
	}
}

// nearMiss suggests a file with the same base name at another path, or the
// same name in different case, which is the usual reason a file "is missing".
func nearMiss(want string, entries []Entry) string {
	base := strings.ToLower(path.Base(want))
	if strings.ContainsAny(base, "*?[") {
		return ""
	}
	for _, en := range entries {
		if !en.IsDir && strings.ToLower(path.Base(en.Name)) == base && en.Name != want {
			return " (there is a " + en.Name + ")"
		}
	}
	return ""
}

func checkBinaryEntry(add adder, file, p string, data []byte, plat *Platform, modeKnown, execBit bool, version string, spec *ArtifactSpec, opt Options, binaries map[string][]binRecord) {
	label := p
	if len(data) == 0 {
		add(file, Finding{Code: CodeEmptyFile, Severity: SevError, Path: p, Message: "the program is empty (0 bytes)"})
		return
	}
	info, ok := Identify(data)
	if !ok {
		what := "is not a program rmqa recognises (ELF, Mach-O or PE)"
		if strings.HasPrefix(string(data), "#!") {
			what = "is a script, not a compiled program"
		} else if info.Format != "" {
			what = "looks like a damaged " + strings.TrimSuffix(info.Format, " (damaged)") + " file"
		}
		add(file, Finding{Code: CodeNotExecutable, Severity: SevError, Path: label, Message: what})
		return
	}
	platID := "-"
	if plat != nil {
		platID = plat.ID()
		osOK, archOK := matchesPlatform(info, plat)
		if !osOK {
			add(file, Finding{Code: CodeWrongOS, Severity: SevError, Path: label, Message: fmt.Sprintf("is %s %s program built for %s, but this download is for %s", article(info.Format), info.Format, info.String(), plat.ID())})
		} else if !archOK {
			add(file, Finding{Code: CodeWrongArch, Severity: SevError, Path: label, Message: fmt.Sprintf("is built for %s, but this download is for %s", info.String(), plat.ID())})
		}
		if plat.OS != "windows" && plat.OS != "any" && p != "" {
			if modeKnown && !execBit {
				add(file, Finding{Code: CodeNoExecBit, Severity: SevError, Path: label, Message: "isn't marked executable, so users will get \"permission denied\" until they chmod +x it"})
			} else if !modeKnown {
				add(file, Finding{Code: CodeExecBitUnknown, Severity: SevWarning, Path: label, Message: "the archive stores no Unix permissions (it was probably zipped on Windows), so the program won't be executable after unpacking"})
			}
		}
	}
	sum := sha256.Sum256(data)
	key := hex.EncodeToString(sum[:])
	binaries[key] = append(binaries[key], binRecord{file: file, path: firstNonEmpty(p, "(the file itself)"), platform: platID})
	if version == "" {
		if spec.CheckVersion {
			add(file, Finding{Code: CodeVersionUnknown, Severity: SevInfo, Path: label, Message: "version not checked: no version was given"})
		}
		return
	}
	if spec.CheckVersion && !containsVersion(data, version) {
		add(file, Finding{Code: CodeVersionMissing, Severity: SevError, Path: label, Message: "doesn't contain the version string " + version + "; it may be an old build"})
	}
	if len(spec.Run) > 0 && !opt.NoRun {
		if plat == nil || !(plat.OS == runtime.GOOS && (plat.Arch == runtime.GOARCH || (plat.Arch == "universal" && contains(info.Arches, runtime.GOARCH)))) {
			add(file, Finding{Code: CodeRunSkipped, Severity: SevInfo, Path: label, Message: "not run: it's for " + platID + " and this machine is " + runtime.GOOS + "/" + runtime.GOARCH})
			return
		}
		out, err := runBinary(data, spec.Run, plat.Vars["exe"])
		args := strings.Join(spec.Run, " ")
		switch {
		case err != nil:
			add(file, Finding{Code: CodeVersionRun, Severity: SevError, Path: label, Message: fmt.Sprintf("running it with %q failed: %v", args, err)})
		case !containsVersion(out, version):
			add(file, Finding{Code: CodeVersionRun, Severity: SevError, Path: label, Message: fmt.Sprintf("running it with %q printed %q, which doesn't contain %s", args, firstLine(out), version)})
		}
	}
}

func article(word string) string {
	if strings.ContainsRune("AEIOUaeiou", rune(word[0])) {
		return "an"
	}
	return "a"
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "..."
	}
	return s
}

func runBinary(data []byte, args []string, exe string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "rmqa-run-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "program"+exe)
	if err := os.WriteFile(p, data, 0o755); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "NO_COLOR=1"}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return out, fmt.Errorf("it didn't finish within 15 seconds")
	}
	if err != nil {
		return out, fmt.Errorf("%v (output: %q)", err, firstLine(out))
	}
	return out, nil
}

// checkChecksums verifies a checksums file such as SHA256SUMS against the
// release: every line must match, and every other file must be listed.
func checkChecksums(add adder, e Expected, data []byte, rel Release, sumFiles []Expected) {
	alg := e.Spec.Checksums
	hexLen := map[string]int{"sha256": 64, "sha512": 128}[alg]
	listed := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		sum, name, ok := parseSumLine(l)
		if !ok {
			add(e.Name, Finding{Code: CodeChecksumFormat, Severity: SevError, Path: fmt.Sprintf("line %d", line), Message: fmt.Sprintf("can't read %q (expected \"<hash>  <file name>\")", firstLine([]byte(l)))})
			continue
		}
		if len(sum) != hexLen {
			add(e.Name, Finding{Code: CodeChecksumFormat, Severity: SevError, Path: name, Message: fmt.Sprintf("the hash has %d characters, but a %s hash has %d", len(sum), alg, hexLen)})
			continue
		}
		listed[name] = strings.ToLower(sum)
	}
	if len(listed) == 0 {
		add(e.Name, Finding{Code: CodeChecksumFormat, Severity: SevError, Message: "lists no checksums"})
		return
	}
	present := map[string]bool{}
	for _, n := range rel.Names() {
		present[n] = true
	}
	isSum := map[string]bool{}
	for _, s := range sumFiles {
		isSum[s.Name] = true
	}
	for _, name := range sortedKeys(listed) {
		if !present[name] {
			add(e.Name, Finding{Code: CodeChecksumStale, Severity: SevError, Path: name, Message: "is listed, but there is no such file in the release"})
			continue
		}
		b, err := rel.Read(name)
		if err != nil {
			add(e.Name, Finding{Code: CodeChecksumFormat, Severity: SevError, Path: name, Message: "can't be read: " + err.Error()})
			continue
		}
		var h hash.Hash = sha256.New()
		if alg == "sha512" {
			h = sha512.New()
		}
		h.Write(b)
		got := hex.EncodeToString(h.Sum(nil))
		if got != listed[name] {
			add(e.Name, Finding{Code: CodeChecksumMismatch, Severity: SevError, Path: name, Message: fmt.Sprintf("listed as %s… but the file's %s is %s…; the file changed after the checksums were made, or the download is damaged", listed[name][:12], alg, got[:12])})
		}
	}
	for _, n := range rel.Names() {
		if isSum[n] || isSignature(n) {
			continue
		}
		if _, ok := listed[n]; !ok {
			add(e.Name, Finding{Code: CodeChecksumMissing, Severity: SevError, Path: n, Message: "is in the release but has no checksum here"})
		}
	}
}

func isSignature(n string) bool {
	for _, s := range []string{".sig", ".asc", ".pem", ".sigstore", ".sigstore.json", ".bundle", ".intoto.jsonl"} {
		if strings.HasSuffix(n, s) {
			return true
		}
	}
	return false
}

// parseSumLine reads "hash  name", "hash *name" (binary mode) or the BSD
// form "SHA256 (name) = hash".
func parseSumLine(l string) (sum, name string, ok bool) {
	if i := strings.Index(l, " ("); i > 0 && strings.Contains(l, ") = ") {
		j := strings.LastIndex(l, ") = ")
		return strings.TrimSpace(l[j+4:]), l[i+2 : j], true
	}
	f := strings.Fields(l)
	if len(f) < 2 {
		return "", "", false
	}
	sum = f[0]
	for _, c := range sum {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return "", "", false
		}
	}
	name = strings.TrimSpace(l[len(sum):])
	name = strings.TrimPrefix(name, "*")
	name = strings.TrimPrefix(name, "./")
	return sum, name, name != ""
}

func human(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}
