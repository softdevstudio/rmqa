package rmqa

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Manifest describes what a release must contain.
type Manifest struct {
	Project     string
	Version     string
	VersionFile string
	ExtraFiles  string   // "error", "warn" or "ignore": files nobody declared
	Forbid      []string // name patterns that must not appear inside archives
	Platforms   []Platform
	Artifacts   []ArtifactSpec

	dir string // folder the manifest came from, for version_file
}

// Platform is one operating system and processor the release supports.
// Every string key in a [[platform]] table becomes a template variable, so
// a platform can carry its own spelling of names, such as
// target = "x86_64-unknown-linux-gnu".
type Platform struct {
	OS   string
	Arch string
	Vars map[string]string
	line int
}

// ID is os/arch, for messages.
func (p Platform) ID() string { return p.OS + "/" + p.Arch }

// ArtifactSpec is one [[artifact]] entry: a file, or one file per platform,
// that must be in the release.
type ArtifactSpec struct {
	Name         string   // template, for example "ukue_{os}_{arch}.{archive}"
	Files        []string // paths that must be inside the archive (patterns allowed)
	Binaries     []string // executables inside the archive (templates allowed)
	Binary       bool     // the artifact itself is an executable
	Checksums    string   // "sha256" or "sha512": this file lists checksums of the others
	CheckVersion bool     // binaries must contain the version string
	Run          []string // arguments that make the binary print its version
	Root         string   // "auto", "none" or a template for the folder inside the archive
	MinSize      int64
	MaxSize      int64
	Optional     bool
	Only         []string // limit a per-platform artifact to these os/arch patterns
	line         int
}

// DefaultForbid lists files that should never ship inside a release archive.
var DefaultForbid = []string{".env", ".env.*", "*.pem", "*.key", "id_rsa", "id_ed25519", ".git/*", ".npmrc", ".pypirc"}

var knownTop = map[string]bool{"project": true, "version": true, "version_file": true, "extra_files": true, "forbid": true}
var knownArtifact = map[string]bool{"name": true, "files": true, "binaries": true, "binary": true, "checksums": true, "check_version": true, "run": true, "root": true, "min_size": true, "max_size": true, "optional": true, "only": true}

// ParseManifest reads a manifest from TOML text. dir is the folder it came
// from (used for version_file); it may be empty.
func ParseManifest(src, dir string) (*Manifest, error) {
	doc, err := parseTOML(src)
	if err != nil {
		return nil, err
	}
	m := &Manifest{ExtraFiles: "error", Forbid: DefaultForbid, dir: dir}
	for name, t := range doc.tables {
		return nil, fmt.Errorf("line %d: unknown section [%s] (rmqa knows [[platform]] and [[artifact]])", t.line, name)
	}
	for name, ts := range doc.arrays {
		if name != "platform" && name != "artifact" {
			return nil, fmt.Errorf("line %d: unknown section [[%s]] (rmqa knows [[platform]] and [[artifact]])", ts[0].line, name)
		}
	}
	r := doc.root
	for _, k := range r.keys {
		if !knownTop[k] {
			return nil, fmt.Errorf("line %d: unknown setting %q", r.values[k].line, k)
		}
	}
	if m.Project, err = getString(r, "project", ""); err != nil {
		return nil, err
	}
	if m.Version, err = getString(r, "version", ""); err != nil {
		return nil, err
	}
	if m.VersionFile, err = getString(r, "version_file", ""); err != nil {
		return nil, err
	}
	if m.ExtraFiles, err = getString(r, "extra_files", "error"); err != nil {
		return nil, err
	}
	switch m.ExtraFiles {
	case "error", "warn", "ignore":
	default:
		return nil, fmt.Errorf("line %d: extra_files must be \"error\", \"warn\" or \"ignore\"", r.values["extra_files"].line)
	}
	if _, ok := r.values["forbid"]; ok {
		if m.Forbid, err = getStrings(r, "forbid"); err != nil {
			return nil, err
		}
	}
	for _, t := range doc.arrays["platform"] {
		p := Platform{Vars: map[string]string{}, line: t.line}
		for _, k := range t.keys {
			s, ok := t.values[k].v.(string)
			if !ok {
				return nil, fmt.Errorf("line %d: platform setting %q must be a string", t.values[k].line, k)
			}
			p.Vars[k] = s
		}
		p.OS, p.Arch = p.Vars["os"], p.Vars["arch"]
		if p.OS == "" || p.Arch == "" {
			return nil, fmt.Errorf("line %d: a [[platform]] needs both os and arch", t.line)
		}
		if !knownOS[p.OS] {
			return nil, fmt.Errorf("line %d: unknown os %q (use one of %s)", t.values["os"].line, p.OS, strings.Join(sortedKeys(knownOS), ", "))
		}
		if !knownArch[p.Arch] {
			return nil, fmt.Errorf("line %d: unknown arch %q (use one of %s)", t.values["arch"].line, p.Arch, strings.Join(sortedKeys(knownArch), ", "))
		}
		if _, ok := p.Vars["archive"]; !ok {
			if p.OS == "windows" {
				p.Vars["archive"] = "zip"
			} else {
				p.Vars["archive"] = "tar.gz"
			}
		}
		if _, ok := p.Vars["exe"]; !ok {
			if p.OS == "windows" {
				p.Vars["exe"] = ".exe"
			} else {
				p.Vars["exe"] = ""
			}
		}
		for _, q := range m.Platforms {
			if q.ID() == p.ID() {
				return nil, fmt.Errorf("line %d: platform %s is listed twice", t.line, p.ID())
			}
		}
		m.Platforms = append(m.Platforms, p)
	}
	for _, t := range doc.arrays["artifact"] {
		a := ArtifactSpec{CheckVersion: true, Root: "auto", line: t.line}
		for _, k := range t.keys {
			if !knownArtifact[k] {
				return nil, fmt.Errorf("line %d: unknown artifact setting %q", t.values[k].line, k)
			}
		}
		if a.Name, err = getString(t, "name", ""); err != nil {
			return nil, err
		}
		if a.Name == "" {
			return nil, fmt.Errorf("line %d: an [[artifact]] needs a name", t.line)
		}
		if a.Files, err = getStrings(t, "files"); err != nil {
			return nil, err
		}
		if a.Binaries, err = getStrings(t, "binaries"); err != nil {
			return nil, err
		}
		if a.Run, err = getStrings(t, "run"); err != nil {
			return nil, err
		}
		if a.Only, err = getStrings(t, "only"); err != nil {
			return nil, err
		}
		if a.Binary, err = getBool(t, "binary", false); err != nil {
			return nil, err
		}
		if a.Optional, err = getBool(t, "optional", false); err != nil {
			return nil, err
		}
		if a.CheckVersion, err = getBool(t, "check_version", true); err != nil {
			return nil, err
		}
		if a.Checksums, err = getString(t, "checksums", ""); err != nil {
			return nil, err
		}
		switch a.Checksums {
		case "", "sha256", "sha512":
		default:
			return nil, fmt.Errorf("line %d: checksums must be \"sha256\" or \"sha512\"", t.values["checksums"].line)
		}
		if a.Root, err = getString(t, "root", "auto"); err != nil {
			return nil, err
		}
		if a.MinSize, err = getInt(t, "min_size"); err != nil {
			return nil, err
		}
		if a.MaxSize, err = getInt(t, "max_size"); err != nil {
			return nil, err
		}
		if a.Binary && (len(a.Files) > 0 || len(a.Binaries) > 0) {
			return nil, fmt.Errorf("line %d: an artifact with binary = true is itself the program, so it can't list files or binaries inside it", t.line)
		}
		if a.Checksums != "" && (a.Binary || len(a.Files) > 0 || len(a.Binaries) > 0) {
			return nil, fmt.Errorf("line %d: a checksums file can't also be an archive or a binary", t.line)
		}
		m.Artifacts = append(m.Artifacts, a)
	}
	if len(m.Artifacts) == 0 {
		return nil, fmt.Errorf("the manifest has no [[artifact]] entries, so there is nothing to check")
	}
	// Check every template now, so a typo fails before any release is read.
	for _, a := range m.Artifacts {
		if _, err := m.expand(a, "0.0.0"); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// LoadManifest reads a manifest file.
func LoadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m, err := ParseManifest(string(b), filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// ResolveVersion picks the version to check: an explicit value (from the
// command line or the environment) wins, then version_file, then version.
func (m *Manifest) ResolveVersion(explicit string) (string, error) {
	v := strings.TrimSpace(explicit)
	if v == "" && m.VersionFile != "" {
		b, err := os.ReadFile(filepath.Join(m.dir, m.VersionFile))
		if err != nil {
			return "", fmt.Errorf("version_file: %w", err)
		}
		v = strings.TrimSpace(string(b))
	}
	if v == "" {
		v = m.Version
	}
	v = strings.TrimPrefix(v, "v")
	return v, nil
}

// Expected is one concrete file the release must contain.
type Expected struct {
	Name     string
	Spec     *ArtifactSpec
	Platform *Platform // nil when the artifact is the same for every platform
	Files    []string
	Binaries []ExpectedBinary
	Root     string // "auto", "none" or a concrete folder name
}

// ExpectedBinary is an executable inside an archive, or the artifact itself.
type ExpectedBinary struct {
	Path     string
	Platform *Platform // nil: format checked, not the platform
}

var platformKeys = map[string]bool{"os": true, "arch": true, "archive": true, "exe": true}

// Expand lists every file the release must contain for the given version.
func (m *Manifest) Expand(version string) ([]Expected, error) {
	var out []Expected
	seen := map[string]int{}
	for i := range m.Artifacts {
		e, err := m.expand(m.Artifacts[i], version)
		if err != nil {
			return nil, err
		}
		for j := range e {
			e[j].Spec = &m.Artifacts[i]
			if line, dup := seen[e[j].Name]; dup {
				return nil, fmt.Errorf("line %d: artifact %q is also produced by the artifact on line %d", m.Artifacts[i].line, e[j].Name, line)
			}
			seen[e[j].Name] = m.Artifacts[i].line
		}
		out = append(out, e...)
	}
	return out, nil
}

func (m *Manifest) expand(a ArtifactSpec, version string) ([]Expected, error) {
	base := map[string]string{"version": version, "project": m.Project}
	perPlatform := usesPlatformVars(a.Name, m)
	if perPlatform && len(m.Platforms) == 0 {
		return nil, fmt.Errorf("line %d: %q uses platform variables, but the manifest lists no [[platform]]", a.line, a.Name)
	}
	if !perPlatform && len(a.Only) > 0 {
		return nil, fmt.Errorf("line %d: only = [...] needs a per-platform name such as \"x_{os}_{arch}\"", a.line)
	}
	var out []Expected
	if perPlatform {
		for i := range m.Platforms {
			p := &m.Platforms[i]
			if len(a.Only) > 0 && !matchAny(a.Only, p.ID()) {
				continue
			}
			vars := merge(base, p.Vars)
			e, err := m.one(a, vars, p)
			if err != nil {
				return nil, err
			}
			out = append(out, e)
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("line %d: only = [...] matches none of the platforms", a.line)
		}
		return out, nil
	}
	e, err := m.one(a, base, nil)
	if err != nil {
		return nil, err
	}
	return []Expected{e}, nil
}

func (m *Manifest) one(a ArtifactSpec, vars map[string]string, p *Platform) (Expected, error) {
	name, err := fill(a.Name, vars, a.line)
	if err != nil {
		return Expected{}, err
	}
	e := Expected{Name: name, Platform: p, Root: a.Root}
	if a.Root != "auto" && a.Root != "none" {
		if e.Root, err = fill(a.Root, vars, a.line); err != nil {
			return Expected{}, err
		}
	}
	for _, f := range a.Files {
		s, err := fill(f, vars, a.line)
		if err != nil {
			return Expected{}, err
		}
		e.Files = append(e.Files, s)
	}
	if a.Binary {
		e.Binaries = append(e.Binaries, ExpectedBinary{Path: "", Platform: p})
	}
	for _, b := range a.Binaries {
		if p == nil && usesPlatformVars(b, m) {
			// One shared archive holding a binary for every platform.
			for i := range m.Platforms {
				q := &m.Platforms[i]
				s, err := fill(b, merge(vars, q.Vars), a.line)
				if err != nil {
					return Expected{}, err
				}
				e.Binaries = append(e.Binaries, ExpectedBinary{Path: s, Platform: q})
			}
			continue
		}
		s, err := fill(b, vars, a.line)
		if err != nil {
			return Expected{}, err
		}
		e.Binaries = append(e.Binaries, ExpectedBinary{Path: s, Platform: p})
	}
	return e, nil
}

func usesPlatformVars(tmpl string, m *Manifest) bool {
	for _, v := range templateVars(tmpl) {
		if platformKeys[v] {
			return true
		}
		for _, p := range m.Platforms {
			if _, ok := p.Vars[v]; ok {
				return true
			}
		}
	}
	return false
}

func templateVars(t string) []string {
	var out []string
	for {
		i := strings.IndexByte(t, '{')
		if i < 0 {
			return out
		}
		j := strings.IndexByte(t[i:], '}')
		if j < 0 {
			return out
		}
		out = append(out, t[i+1:i+j])
		t = t[i+j+1:]
	}
}

func fill(t string, vars map[string]string, line int) (string, error) {
	var b strings.Builder
	for {
		i := strings.IndexByte(t, '{')
		if i < 0 {
			b.WriteString(t)
			return b.String(), nil
		}
		j := strings.IndexByte(t[i:], '}')
		if j < 0 {
			return "", fmt.Errorf("line %d: %q has a { without a closing }", line, t)
		}
		b.WriteString(t[:i])
		k := t[i+1 : i+j]
		v, ok := vars[k]
		if !ok {
			return "", fmt.Errorf("line %d: unknown variable {%s} (known: %s)", line, k, strings.Join(sortedKeys(vars), ", "))
		}
		if k == "version" && v == "" {
			return "", fmt.Errorf("line %d: {version} is used, but no version is known: pass --version, set RMQA_VERSION, or set version or version_file in the manifest", line)
		}
		b.WriteString(v)
		t = t[i+j+1:]
	}
}

func merge(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func getString(t *tomlTable, k, def string) (string, error) {
	v, ok := t.values[k]
	if !ok {
		return def, nil
	}
	s, ok := v.v.(string)
	if !ok {
		return "", fmt.Errorf("line %d: %s must be a string in quotes", v.line, k)
	}
	return s, nil
}

func getBool(t *tomlTable, k string, def bool) (bool, error) {
	v, ok := t.values[k]
	if !ok {
		return def, nil
	}
	b, ok := v.v.(bool)
	if !ok {
		return false, fmt.Errorf("line %d: %s must be true or false", v.line, k)
	}
	return b, nil
}

func getInt(t *tomlTable, k string) (int64, error) {
	v, ok := t.values[k]
	if !ok {
		return 0, nil
	}
	n, ok := v.v.(int64)
	if !ok || n < 0 {
		return 0, fmt.Errorf("line %d: %s must be a whole number of bytes", v.line, k)
	}
	return n, nil
}

func getStrings(t *tomlTable, k string) ([]string, error) {
	v, ok := t.values[k]
	if !ok {
		return nil, nil
	}
	if s, ok := v.v.(string); ok {
		return []string{s}, nil
	}
	arr, ok := v.v.([]any)
	if !ok {
		return nil, fmt.Errorf("line %d: %s must be a list of strings, like [\"a\", \"b\"]", v.line, k)
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		s, ok := x.(string)
		if !ok {
			return nil, fmt.Errorf("line %d: %s must be a list of strings, like [\"a\", \"b\"]", v.line, k)
		}
		out = append(out, s)
	}
	return out, nil
}
