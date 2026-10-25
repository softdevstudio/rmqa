package rmqa

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// rmqa init looks at a release that is known to be good and writes the
// manifest that describes it, so later releases can be checked against it.

var osWords = map[string][]string{
	"linux":   {"linux"},
	"darwin":  {"apple-darwin", "darwin", "macos", "osx", "mac"},
	"windows": {"windows", "win64", "win32", "win"},
	"freebsd": {"freebsd"},
	"openbsd": {"openbsd"},
	"netbsd":  {"netbsd"},
	"android": {"android"},
}

var archWords = map[string][]string{
	"amd64":     {"x86_64", "x86-64", "amd64", "x64", "64bit"},
	"arm64":     {"aarch64", "arm64"},
	"386":       {"i386", "i686", "386", "x86", "32bit"},
	"arm":       {"armv7l", "armv7", "armv6", "armhf", "arm"},
	"riscv64":   {"riscv64"},
	"ppc64le":   {"ppc64le"},
	"s390x":     {"s390x"},
	"loong64":   {"loong64"},
	"universal": {"universal", "all"},
}

var semverRE = regexp.MustCompile(`v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)`)

// findToken finds a whole word in name (case-insensitive) and returns it as
// spelled in name. Words are separated by anything but letters and digits.
func findToken(name string, words []string) (string, int) {
	lower := strings.ToLower(name)
	sorted := append([]string(nil), words...)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	for _, w := range sorted {
		for off := 0; ; {
			i := strings.Index(lower[off:], w)
			if i < 0 {
				break
			}
			i += off
			j := i + len(w)
			if (i == 0 || !isAlnum(lower[i-1])) && (j == len(lower) || !isAlnum(lower[j])) {
				return name[i:j], i
			}
			off = i + 1
		}
	}
	return "", -1
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

type initArtifact struct {
	name     string
	plat     *ExecInfo
	files    []string
	binaries []string
	binary   bool
	root     string
	sumAlg   string
}

// InitOptions guide manifest generation.
type InitOptions struct {
	Project string
	Version string
}

// Init writes a manifest for a release that is known to be good.
func Init(rel Release, opt InitOptions) (string, error) {
	version := strings.TrimPrefix(opt.Version, "v")
	if version == "" {
		version = guessVersion(rel.Names())
	}
	var arts []initArtifact
	var notes []string
	for _, n := range rel.Names() {
		data, err := rel.Read(n)
		if err != nil {
			return "", err
		}
		a := initArtifact{name: n}
		if alg := sumAlg(n, data); alg != "" {
			a.sumAlg = alg
			arts = append(arts, a)
			continue
		}
		if info, ok := Identify(data); ok {
			a.binary = true
			a.plat = &info
			arts = append(arts, a)
			continue
		}
		kind := archiveKind(n)
		if kind == "" || kind == "unsupported" {
			arts = append(arts, a)
			continue
		}
		entries, err := OpenArchive(n, data)
		if err != nil {
			return "", fmt.Errorf("%s can't be read (%v); rmqa init needs a release that is known to be good", n, err)
		}
		stripped, top, err := stripRoot(entries, "auto")
		if err != nil {
			return "", err
		}
		a.root = top
		var plats []ExecInfo
		for _, e := range stripped {
			if e.IsDir || e.Link != "" || matchAny(junk, e.Name) {
				continue
			}
			if info, ok := Identify(e.Data); ok {
				a.binaries = append(a.binaries, e.Name)
				plats = append(plats, info)
				continue
			}
			a.files = append(a.files, e.Name)
		}
		if len(plats) > 0 && samePlatform(plats) {
			a.plat = &plats[0]
		} else if len(plats) > 1 {
			notes = append(notes, n+" holds programs for several platforms, so its programs are listed without a platform")
		}
		arts = append(arts, a)
	}
	if len(arts) == 0 {
		return "", fmt.Errorf("the release is empty")
	}
	project := opt.Project
	if project == "" {
		project = guessProject(arts)
	}
	return renderInit(arts, project, version, notes), nil
}

func samePlatform(ps []ExecInfo) bool {
	for _, p := range ps[1:] {
		if p.String() != ps[0].String() {
			return false
		}
	}
	return true
}

func guessVersion(names []string) string {
	count := map[string]int{}
	for _, n := range names {
		for _, m := range semverRE.FindAllStringSubmatch(n, -1) {
			count[m[1]]++
		}
	}
	best, bestN := "", 0
	for v, c := range count {
		if c > bestN || (c == bestN && v > best) {
			best, bestN = v, c
		}
	}
	return best
}

func guessProject(arts []initArtifact) string {
	for _, a := range arts {
		for _, b := range a.binaries {
			base := b[strings.LastIndex(b, "/")+1:]
			return strings.TrimSuffix(base, ".exe")
		}
		if a.binary {
			n := a.name
			if i := strings.IndexAny(n, "_-."); i > 0 {
				return n[:i]
			}
			return n
		}
	}
	return ""
}

func sumAlg(name string, data []byte) string {
	n := strings.ToLower(name)
	if !(strings.Contains(n, "sum") || strings.Contains(n, "sha") || strings.Contains(n, "checksum")) {
		return ""
	}
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		sum, _, ok := parseSumLine(l)
		if !ok {
			return ""
		}
		switch len(sum) {
		case 64:
			return "sha256"
		case 128:
			return "sha512"
		}
		return ""
	}
	return ""
}

type platTokens struct {
	os, arch         string // canonical
	osWord, archWord string // as spelled in names
}

func renderInit(arts []initArtifact, project, version string, notes []string) string {
	// Work out the platforms and how each one is spelled in the file names.
	plats := map[string]*platTokens{}
	var platOrder []string
	ok := map[int]bool{}
	for i, a := range arts {
		if a.plat == nil {
			continue
		}
		osName := a.plat.OS
		arch := strings.Join(a.plat.Arches, "+")
		if len(a.plat.Arches) > 1 {
			arch = "universal"
		}
		ow, _ := findToken(a.name, osWords[osName])
		aw, _ := findToken(a.name, archWords[arch])
		if ow == "" || aw == "" {
			continue // the name doesn't say its platform; keep the name as it is
		}
		id := osName + "/" + arch
		pt, seen := plats[id]
		if !seen {
			pt = &platTokens{os: osName, arch: arch, osWord: ow, archWord: aw}
			plats[id] = pt
			platOrder = append(platOrder, id)
		}
		ok[i] = true
	}
	// Use {os} and {arch} when the names spell them the Go way, or
	// {os_name} and {arch_name} when they use other words.
	osVar, archVar := "{os}", "{arch}"
	for _, id := range platOrder {
		p := plats[id]
		if p.osWord != p.os {
			osVar = "{os_name}"
		}
		if p.archWord != p.arch {
			archVar = "{arch_name}"
		}
	}
	templateName := func(name string, p *platTokens) string {
		if version != "" {
			name = strings.ReplaceAll(name, "v"+version, "v{version}")
			name = strings.ReplaceAll(name, version, "{version}")
			name = strings.ReplaceAll(name, "v{version}", "v{version}")
		}
		if p == nil {
			return name
		}
		if w, i := findToken(name, []string{strings.ToLower(p.osWord)}); i >= 0 {
			name = name[:i] + osVar + name[i+len(w):]
		}
		if w, i := findToken(name, []string{strings.ToLower(p.archWord)}); i >= 0 {
			name = name[:i] + archVar + name[i+len(w):]
		}
		def := "tar.gz"
		if p.os == "windows" {
			def = "zip"
		}
		if strings.HasSuffix(name, "."+def) {
			name = strings.TrimSuffix(name, "."+def) + ".{archive}"
		}
		return name
	}
	type group struct {
		name, root   string
		files, bins  []string
		binary       bool
		sumAlg       string
		platforms    []string
		perPlatform  bool
		platformFree bool
	}
	var groups []*group
	byKey := map[string]*group{}
	for i, a := range arts {
		var p *platTokens
		if ok[i] {
			arch := strings.Join(a.plat.Arches, "+")
			if len(a.plat.Arches) > 1 {
				arch = "universal"
			}
			p = plats[a.plat.OS+"/"+arch]
		}
		g := &group{name: templateName(a.name, p), binary: a.binary, sumAlg: a.sumAlg, perPlatform: p != nil}
		if a.root != "" {
			g.root = templateName(a.root, p)
		}
		for _, b := range a.binaries {
			if p != nil {
				// {exe} is ".exe" on Windows and empty elsewhere, so one
				// template fits every platform.
				if p.os == "windows" {
					b = strings.TrimSuffix(b, ".exe")
				}
				b += "{exe}"
			}
			g.bins = append(g.bins, b)
		}
		g.files = append(g.files, a.files...)
		key := g.name + "|" + g.root + "|" + strings.Join(g.bins, ",") + fmt.Sprint(g.binary)
		if prev, dup := byKey[key]; dup && g.perPlatform {
			prev.files = intersect(prev.files, g.files)
			prev.platforms = append(prev.platforms, p.os+"/"+p.arch)
			continue
		}
		if p != nil {
			g.platforms = []string{p.os + "/" + p.arch}
		}
		byKey[key] = g
		groups = append(groups, g)
	}
	// A per-platform artifact must exist for every platform unless it says
	// which ones it is for.
	var b strings.Builder
	b.WriteString("# rmqa manifest, written by `rmqa init` from a release that was known to be good.\n")
	b.WriteString("# Check a release with: rmqa check DIR --manifest rmqa.toml\n\n")
	if project != "" {
		fmt.Fprintf(&b, "project = %q\n", project)
	}
	if version != "" {
		fmt.Fprintf(&b, "# The version can also come from --version, RMQA_VERSION or version_file = \"VERSION\".\nversion = %q\n", version)
	}
	b.WriteString("\n")
	for _, n := range notes {
		fmt.Fprintf(&b, "# Note: %s.\n", n)
	}
	sort.SliceStable(platOrder, func(i, j int) bool { return platOrder[i] < platOrder[j] })
	for _, id := range platOrder {
		p := plats[id]
		b.WriteString("[[platform]]\n")
		fmt.Fprintf(&b, "os = %q\narch = %q\n", p.os, p.arch)
		if osVar == "{os_name}" {
			fmt.Fprintf(&b, "os_name = %q\n", p.osWord)
		}
		if archVar == "{arch_name}" {
			fmt.Fprintf(&b, "arch_name = %q\n", p.archWord)
		}
		b.WriteString("\n")
	}
	// Checksum files go last: they describe everything above them.
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].sumAlg == "" && groups[j].sumAlg != "" })
	for _, g := range groups {
		b.WriteString("[[artifact]]\n")
		fmt.Fprintf(&b, "name = %q\n", g.name)
		if g.perPlatform && len(g.platforms) < len(platOrder) {
			sort.Strings(g.platforms)
			fmt.Fprintf(&b, "only = %s\n", tomlList(g.platforms))
		}
		if g.root != "" {
			fmt.Fprintf(&b, "root = %q\n", g.root)
		}
		if len(g.bins) > 0 {
			fmt.Fprintf(&b, "binaries = %s\n", tomlList(g.bins))
		}
		if len(g.files) > 0 {
			sort.Strings(g.files)
			fmt.Fprintf(&b, "files = %s\n", tomlList(g.files))
		}
		if g.binary {
			b.WriteString("binary = true\n")
		}
		if g.sumAlg != "" {
			fmt.Fprintf(&b, "checksums = %q\n", g.sumAlg)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func intersect(a, b []string) []string {
	in := map[string]bool{}
	for _, x := range b {
		in[x] = true
	}
	var out []string
	for _, x := range a {
		if in[x] {
			out = append(out, x)
		}
	}
	return out
}

func tomlList(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = fmt.Sprintf("%q", x)
	}
	s := "[" + strings.Join(q, ", ") + "]"
	if len(s) <= 90 {
		return s
	}
	return "[\n  " + strings.Join(q, ",\n  ") + ",\n]"
}
