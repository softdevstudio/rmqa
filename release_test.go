package rmqa

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The tests build a small Go program for five platforms and pack it the way
// real projects do. A "good" release must pass with no errors and no
// warnings. Each kind of broken release must be caught with the right code.

var platforms = []struct{ goos, goarch string }{
	{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}, {"darwin", "amd64"}, {"windows", "amd64"},
}

var (
	binOnce  sync.Mutex
	binCache = map[string][]byte{}
	binDir   string
)

func testBinary(t testing.TB, goos, goarch, version string) []byte {
	t.Helper()
	binOnce.Lock()
	defer binOnce.Unlock()
	key := goos + "/" + goarch + "/" + version
	if b, ok := binCache[key]; ok {
		return b
	}
	if binDir == "" {
		d, err := os.MkdirTemp("", "rmqa-bins-")
		if err != nil {
			t.Fatal(err)
		}
		binDir = d
	}
	out := filepath.Join(binDir, strings.ReplaceAll(key, "/", "_"))
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w -X main.version="+version, "-o", out, "./testdata/hello")
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the test program for %s: %v\n%s", key, err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	binCache[key] = b
	return b
}

func TestMain(m *testing.M) {
	code := m.Run()
	if binDir != "" {
		os.RemoveAll(binDir)
	}
	os.Exit(code)
}

type tfile struct {
	name string
	mode int64
	data []byte
	link string
	dir  bool
}

type tarchive struct {
	name    string
	zip     bool
	winZip  bool // zip entries without Unix modes, as Windows tools write them
	files   []tfile
	raw     []byte // replaces the packed bytes (for damaged archives)
	packed  []byte
	mutated bool
}

type trelease struct {
	archives []*tarchive
	extra    map[string][]byte
	noSums   bool
	sums     func(lines []string) []string // edits SHA256SUMS lines
	after    func(dir string)              // runs after SHA256SUMS is written
}

const version = "1.4.2"

func goodRelease(t testing.TB) *trelease {
	r := &trelease{extra: map[string][]byte{}}
	for _, p := range platforms {
		exe := ""
		if p.goos == "windows" {
			exe = ".exe"
		}
		a := &tarchive{name: fmt.Sprintf("hello_%s_%s.tar.gz", p.goos, p.goarch), zip: p.goos == "windows"}
		if a.zip {
			a.name = strings.TrimSuffix(a.name, ".tar.gz") + ".zip"
		}
		a.files = []tfile{
			{name: "hello" + exe, mode: 0o755, data: testBinary(t, p.goos, p.goarch, version)},
			{name: "LICENSE", mode: 0o644, data: []byte("Apache License\nVersion 2.0\n")},
			{name: "NOTICE", mode: 0o644, data: []byte("hello\nCopyright 2026\n")},
			{name: "README.md", mode: 0o644, data: []byte("# hello\n")},
		}
		r.archives = append(r.archives, a)
	}
	return r
}

func (r *trelease) archive(name string) *tarchive {
	for _, a := range r.archives {
		if strings.Contains(a.name, name) {
			return a
		}
	}
	panic("no archive " + name)
}

func (a *tarchive) file(name string) *tfile {
	for i := range a.files {
		if a.files[i].name == name {
			return &a.files[i]
		}
	}
	panic("no file " + name)
}

func (a *tarchive) remove(name string) {
	var keep []tfile
	for _, f := range a.files {
		if f.name != name {
			keep = append(keep, f)
		}
	}
	a.files = keep
}

func pack(t testing.TB, a *tarchive) []byte {
	var buf bytes.Buffer
	if a.zip {
		zw := zip.NewWriter(&buf)
		for _, f := range a.files {
			h := &zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)}
			if f.dir {
				h.Name = strings.TrimSuffix(f.name, "/") + "/"
			}
			if !a.winZip {
				h.SetMode(os.FileMode(f.mode))
				if f.dir {
					h.SetMode(os.ModeDir | 0o755)
				}
			} else {
				h.CreatorVersion = 0 // made on DOS/Windows: no Unix mode stored
			}
			w, err := zw.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			w.Write(f.data)
		}
		zw.Close()
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range a.files {
		h := &tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.data)), Typeflag: tar.TypeReg, ModTime: time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)}
		if f.dir {
			h.Typeflag, h.Size, h.Mode = tar.TypeDir, 0, 0o755
		}
		if f.link != "" {
			h.Typeflag, h.Size, h.Linkname = tar.TypeSymlink, 0, f.link
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write(f.data)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// write puts the release in a new folder and returns its path.
func (r *trelease) write(t testing.TB) string {
	dir := t.TempDir()
	var lines []string
	for _, a := range r.archives {
		b := a.raw
		if b == nil {
			b = pack(t, a)
		}
		if err := os.WriteFile(filepath.Join(dir, a.name), b, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		lines = append(lines, hex.EncodeToString(sum[:])+"  "+a.name)
	}
	for n, b := range r.extra {
		os.WriteFile(filepath.Join(dir, n), b, 0o644)
		sum := sha256.Sum256(b)
		lines = append(lines, hex.EncodeToString(sum[:])+"  "+n)
	}
	sort.Strings(lines)
	if r.sums != nil {
		lines = r.sums(lines)
	}
	if !r.noSums {
		os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	}
	if r.after != nil {
		r.after(dir)
	}
	return dir
}

const testManifest = `
project = "hello"

[[platform]]
os = "linux"
arch = "amd64"

[[platform]]
os = "linux"
arch = "arm64"

[[platform]]
os = "darwin"
arch = "arm64"

[[platform]]
os = "darwin"
arch = "amd64"

[[platform]]
os = "windows"
arch = "amd64"

[[artifact]]
name = "hello_{os}_{arch}.{archive}"
binaries = ["hello{exe}"]
files = ["LICENSE", "NOTICE", "README.md"]
run = ["version"]

[[artifact]]
name = "SHA256SUMS"
checksums = "sha256"
`

func checkRelease(t testing.TB, r *trelease, manifest string) *Result {
	t.Helper()
	m, err := ParseManifest(manifest, "")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := OpenDir(r.write(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Check(m, rel, Options{Version: version})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func codes(res *Result, sev Severity) map[string]int {
	out := map[string]int{}
	for _, f := range res.Findings {
		if sev == "" || f.Severity == sev {
			out[f.Code]++
		}
	}
	return out
}

func dump(res *Result) string {
	var b bytes.Buffer
	WriteText(&b, res, true, false)
	return b.String()
}

func TestGoodReleasePasses(t *testing.T) {
	res := checkRelease(t, goodRelease(t), testManifest)
	if res.Errors != 0 || res.Warnings != 0 {
		t.Fatalf("a good release must pass cleanly:\n%s", dump(res))
	}
	if len(res.Files) != 6 {
		t.Fatalf("expected 6 files, got %d", len(res.Files))
	}
}

// mutation is one way a release goes wrong in real life.
type mutation struct {
	name   string
	target string // the archive it touches, so random combinations don't collide
	apply  func(t testing.TB, r *trelease)
	want   []string // codes that must appear as errors or warnings
	alone  []string // further codes that appear when it is the only problem
}

var mutations = []mutation{
	{"a platform's download was never uploaded", "darwin_amd64", func(t testing.TB, r *trelease) {
		var keep []*tarchive
		for _, a := range r.archives {
			if !strings.Contains(a.name, "darwin_amd64") {
				keep = append(keep, a)
			}
		}
		r.archives = keep
	}, []string{CodeMissingArtifact}, nil},
	{"the amd64 build was copied into the arm64 archive", "linux_arm64", func(t testing.TB, r *trelease) {
		r.archive("linux_arm64").file("hello").data = testBinary(t, "linux", "amd64", version)
	}, []string{CodeWrongArch}, []string{CodeDuplicateBinary}},
	{"a Linux build was packed as the macOS download", "darwin_arm64", func(t testing.TB, r *trelease) {
		r.archive("darwin_arm64").file("hello").data = testBinary(t, "linux", "arm64", version)
	}, []string{CodeWrongOS}, []string{CodeDuplicateBinary}},
	{"an old build shipped in one archive", "linux_amd64", func(t testing.TB, r *trelease) {
		r.archive("linux_amd64").file("hello").data = testBinary(t, "linux", "amd64", "1.4.1")
	}, []string{CodeVersionMissing}, nil},
	{"the license file was left out of the Windows zip", "windows_amd64", func(t testing.TB, r *trelease) {
		r.archive("windows_amd64").remove("LICENSE")
	}, []string{CodeMissingFile}, nil},
	{"the license file is empty", "darwin_arm64", func(t testing.TB, r *trelease) {
		r.archive("darwin_arm64").file("LICENSE").data = nil
	}, []string{CodeEmptyFile}, nil},
	{"an archive was rebuilt after the checksums were made", "linux_arm64", func(t testing.TB, r *trelease) {
		a := r.archive("linux_arm64")
		r.after = chain(r.after, func(dir string) {
			a.files = append(a.files, tfile{name: "CHANGELOG.md", mode: 0o644, data: []byte("late fix\n")})
			os.WriteFile(filepath.Join(dir, a.name), pack(t, a), 0o644)
		})
	}, []string{CodeChecksumMismatch}, nil},
	{"the checksums file misses one download", "sums", func(t testing.TB, r *trelease) {
		r.sums = func(l []string) []string { return l[1:] }
	}, []string{CodeChecksumMissing}, nil},
	{"the checksums file lists a download that isn't there", "sums", func(t testing.TB, r *trelease) {
		r.sums = func(l []string) []string {
			return append(l, strings.Repeat("ab", 32)+"  hello_freebsd_amd64.tar.gz")
		}
	}, []string{CodeChecksumStale}, nil},
	{"the Linux program lost its executable bit", "linux_amd64", func(t testing.TB, r *trelease) {
		r.archive("linux_amd64").file("hello").mode = 0o644
	}, []string{CodeNoExecBit}, nil},
	{"an upload was cut off halfway", "darwin_amd64", func(t testing.TB, r *trelease) {
		a := r.archive("darwin_amd64")
		b := pack(t, a)
		a.raw = b[:len(b)/2]
	}, []string{CodeArchiveCorrupt}, nil},
	{"an archive entry escapes the folder (zip slip)", "windows_amd64", func(t testing.TB, r *trelease) {
		a := r.archive("windows_amd64")
		a.files = append(a.files, tfile{name: "../../evil.bat", mode: 0o644, data: []byte("echo hi")})
	}, []string{CodeUnsafePath}, nil},
	{"a .env file with secrets was packed", "linux_arm64", func(t testing.TB, r *trelease) {
		a := r.archive("linux_arm64")
		a.files = append(a.files, tfile{name: ".env", mode: 0o600, data: []byte("API_KEY=secret\n")})
	}, []string{CodeForbiddenFile}, nil},
	{"a private key was packed", "darwin_arm64", func(t testing.TB, r *trelease) {
		a := r.archive("darwin_arm64")
		a.files = append(a.files, tfile{name: "certs/signing.pem", mode: 0o600, data: []byte("-----BEGIN PRIVATE KEY-----\n")})
	}, []string{CodeForbiddenFile}, nil},
	{"macOS clutter landed in the archive", "darwin_amd64", func(t testing.TB, r *trelease) {
		a := r.archive("darwin_amd64")
		a.files = append(a.files, tfile{name: ".DS_Store", mode: 0o644, data: []byte{0, 0, 0, 1}})
	}, []string{CodeJunkFile}, nil},
	{"a leftover debug build was uploaded too", "extra", func(t testing.TB, r *trelease) {
		r.extra["hello_linux_amd64_debug.tar.gz"] = []byte("not really")
	}, []string{CodeExtraFile}, nil},
	{"the program inside one archive is empty", "linux_amd64", func(t testing.TB, r *trelease) {
		r.archive("linux_amd64").file("hello").data = nil
	}, []string{CodeEmptyFile}, nil},
	{"a shell script was packed instead of the program", "linux_arm64", func(t testing.TB, r *trelease) {
		r.archive("linux_arm64").file("hello").data = []byte("#!/bin/sh\necho hello\n")
	}, []string{CodeNotExecutable}, nil},
	{"the Windows program is missing its .exe", "windows_amd64", func(t testing.TB, r *trelease) {
		a := r.archive("windows_amd64")
		a.file("hello.exe").name = "hello"
	}, []string{CodeMissingBinary}, nil},
	{"the Windows zip holds the Linux program", "windows_amd64", func(t testing.TB, r *trelease) {
		r.archive("windows_amd64").file("hello.exe").data = testBinary(t, "linux", "amd64", version)
	}, []string{CodeWrongOS}, []string{CodeDuplicateBinary}},
	{"a Unix archive was zipped on Windows, losing permissions", "darwin_arm64", func(t testing.TB, r *trelease) {
		a := r.archive("darwin_arm64")
		a.name = strings.TrimSuffix(a.name, ".tar.gz") + ".zip"
		a.zip, a.winZip = true, true
	}, []string{CodeMissingArtifact, CodeExtraFile}, nil},
	{"the archive was named with the version, unlike the manifest", "linux_amd64", func(t testing.TB, r *trelease) {
		a := r.archive("linux_amd64")
		a.name = "hello_" + version + "_linux_amd64.tar.gz"
	}, []string{CodeMissingArtifact, CodeExtraFile}, nil},
	{"the checksums file was never made", "sums", func(t testing.TB, r *trelease) {
		r.noSums = true
	}, []string{CodeMissingArtifact}, nil},
	{"a link points outside the archive", "darwin_amd64", func(t testing.TB, r *trelease) {
		a := r.archive("darwin_amd64")
		a.files = append(a.files, tfile{name: "lib", link: "/etc/passwd"})
	}, []string{CodeUnsafePath}, nil},
}

func chain(a, b func(string)) func(string) {
	return func(dir string) {
		if a != nil {
			a(dir)
		}
		b(dir)
	}
}

func TestEveryBrokenReleaseIsCaught(t *testing.T) {
	for _, mu := range mutations {
		t.Run(mu.name, func(t *testing.T) {
			r := goodRelease(t)
			mu.apply(t, r)
			res := checkRelease(t, r, testManifest)
			got := codes(res, "")
			for _, c := range append(append([]string{}, mu.want...), mu.alone...) {
				if got[c] == 0 {
					t.Errorf("expected %s:\n%s", c, dump(res))
				}
			}
			if res.Ready(true) {
				t.Errorf("a broken release was called ready:\n%s", dump(res))
			}
		})
	}
}

// Random combinations of problems, each on a different archive, must all
// be reported together: one problem must never hide another.
func TestCombinedProblems(t *testing.T) {
	rng := rand.New(rand.NewSource(20261025))
	// 60 by default keeps CI quick under -race; scripts/record-results.sh
	// sets RMQA_ITERATIONS=300 for the published run.
	iterations := 60
	if n, err := strconv.Atoi(os.Getenv("RMQA_ITERATIONS")); err == nil && n > 0 {
		iterations = n
	}
	if testing.Short() {
		iterations = 20
	}
	total := 0
	defer func() {
		if t.Failed() {
			return
		}
		t.Logf("combinations: %d releases with %d problems in all, every problem reported", iterations, total)
	}()
	for i := 0; i < iterations; i++ {
		r := goodRelease(t)
		used := map[string]bool{}
		var applied []mutation
		for _, j := range rng.Perm(len(mutations))[:1+rng.Intn(4)] {
			mu := mutations[j]
			if used[mu.target] {
				continue
			}
			used[mu.target] = true
			mu.apply(t, r)
			applied = append(applied, mu)
		}
		total += len(applied)
		res := checkRelease(t, r, testManifest)
		got := codes(res, "")
		for _, mu := range applied {
			for _, c := range mu.want {
				if got[c] == 0 {
					var names []string
					for _, a := range applied {
						names = append(names, a.name)
					}
					t.Fatalf("iteration %d (%s): %q was not reported as %s:\n%s", i, strings.Join(names, "; "), mu.name, c, dump(res))
				}
			}
		}
	}
}

func TestZipWithoutUnixModesWarns(t *testing.T) {
	r := goodRelease(t)
	a := r.archive("darwin_arm64")
	a.name = strings.TrimSuffix(a.name, ".tar.gz") + ".zip"
	a.zip, a.winZip = true, true
	man := strings.Replace(testManifest, `name = "hello_{os}_{arch}.{archive}"`, `name = "hello_{os}_{arch}.{archive}"`, 1)
	man = strings.Replace(man, "os = \"darwin\"\narch = \"arm64\"\n", "os = \"darwin\"\narch = \"arm64\"\narchive = \"zip\"\n", 1)
	res := checkRelease(t, r, man)
	if codes(res, SevWarning)[CodeExecBitUnknown] != 1 || res.Errors != 0 {
		t.Fatalf("want one exec-bit-unknown warning and no errors:\n%s", dump(res))
	}
}

func TestWrappedFolderAndRoot(t *testing.T) {
	r := goodRelease(t)
	for _, a := range r.archives {
		base := strings.TrimSuffix(strings.TrimSuffix(a.name, ".tar.gz"), ".zip")
		files := []tfile{{name: base + "/", dir: true}}
		for _, f := range a.files {
			f.name = base + "/" + f.name
			files = append(files, f)
		}
		a.files = files
	}
	// root = "auto" (the default) strips one shared top folder.
	if res := checkRelease(t, r, testManifest); res.Errors != 0 {
		t.Fatalf("auto root:\n%s", dump(res))
	}
	// A named root must match exactly.
	man := strings.Replace(testManifest, `run = ["version"]`, "run = [\"version\"]\nroot = \"hello_{os}_{arch}\"", 1)
	if res := checkRelease(t, r, man); res.Errors != 0 {
		t.Fatalf("named root:\n%s", dump(res))
	}
	man = strings.Replace(testManifest, `run = ["version"]`, "run = [\"version\"]\nroot = \"hello-{version}\"", 1)
	if res := checkRelease(t, r, man); codes(res, SevError)[CodeArchiveLayout] != 5 {
		t.Fatalf("wrong root must fail for all 5 archives:\n%s", dump(res))
	}
}

func TestRawBinaryArtifacts(t *testing.T) {
	dir := t.TempDir()
	for _, p := range platforms[:3] {
		os.WriteFile(filepath.Join(dir, "hello-"+p.goos+"-"+p.goarch), testBinary(t, p.goos, p.goarch, version), 0o755)
	}
	man := `
[[platform]]
os = "linux"
arch = "amd64"
[[platform]]
os = "linux"
arch = "arm64"
[[platform]]
os = "darwin"
arch = "arm64"
[[artifact]]
name = "hello-{os}-{arch}"
binary = true
`
	m, err := ParseManifest(man, "")
	if err != nil {
		t.Fatal(err)
	}
	rel, _ := OpenDir(dir)
	res, err := Check(m, rel, Options{Version: version})
	if err != nil {
		t.Fatal(err)
	}
	if res.Errors != 0 {
		t.Fatalf("raw binaries:\n%s", dump(res))
	}
	// Swap two of them.
	os.Rename(filepath.Join(dir, "hello-linux-amd64"), filepath.Join(dir, "tmp"))
	os.Rename(filepath.Join(dir, "hello-linux-arm64"), filepath.Join(dir, "hello-linux-amd64"))
	os.Rename(filepath.Join(dir, "tmp"), filepath.Join(dir, "hello-linux-arm64"))
	rel, _ = OpenDir(dir)
	res, _ = Check(m, rel, Options{Version: version})
	if codes(res, SevError)[CodeWrongArch] != 2 {
		t.Fatalf("swapped binaries must give two wrong-arch errors:\n%s", dump(res))
	}
}

func TestInitRoundTrip(t *testing.T) {
	r := goodRelease(t)
	dir := r.write(t)
	rel, _ := OpenDir(dir)
	text, err := Init(rel, InitOptions{Version: version})
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseManifest(text, "")
	if err != nil {
		t.Fatalf("generated manifest doesn't parse: %v\n%s", err, text)
	}
	res, err := Check(m, rel, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Errors != 0 || res.Warnings != 0 {
		t.Fatalf("a release must pass the manifest init wrote for it:\n%s\n%s", text, dump(res))
	}
	for _, want := range []string{`project = "hello"`, `name = "hello_{os}_{arch}.{archive}"`, `binaries = ["hello{exe}"]`, `checksums = "sha256"`} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %s in:\n%s", want, text)
		}
	}
	// And the generated manifest catches the problems a hand-written one does.
	for _, mu := range mutations {
		if mu.target == "sums" && mu.name == "the checksums file was never made" {
			continue
		}
		r := goodRelease(t)
		mu.apply(t, r)
		rel, _ := OpenDir(r.write(t))
		res, err := Check(m, rel, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Ready(true) {
			t.Errorf("init manifest missed %q:\n%s", mu.name, dump(res))
		}
	}
}

func TestInitOtherSpellings(t *testing.T) {
	// goreleaser-style names: Darwin_x86_64, with the version and a folder.
	dir := t.TempDir()
	spell := map[string]string{"linux/amd64": "Linux_x86_64", "linux/arm64": "Linux_arm64", "darwin/arm64": "Darwin_arm64", "darwin/amd64": "Darwin_x86_64", "windows/amd64": "Windows_x86_64"}
	var lines []string
	for _, p := range platforms {
		exe := ""
		if p.goos == "windows" {
			exe = ".exe"
		}
		a := &tarchive{name: "hello_" + version + "_" + spell[p.goos+"/"+p.goarch] + ".tar.gz", zip: p.goos == "windows"}
		if a.zip {
			a.name = strings.TrimSuffix(a.name, ".tar.gz") + ".zip"
		}
		a.files = []tfile{{name: "hello" + exe, mode: 0o755, data: testBinary(t, p.goos, p.goarch, version)}, {name: "LICENSE", mode: 0o644, data: []byte("license")}}
		b := pack(t, a)
		os.WriteFile(filepath.Join(dir, a.name), b, 0o644)
		sum := sha256.Sum256(b)
		lines = append(lines, hex.EncodeToString(sum[:])+"  "+a.name)
	}
	os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	rel, _ := OpenDir(dir)
	text, err := Init(rel, InitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`version = "1.4.2"`, `name = "hello_{version}_{os_name}_{arch_name}.{archive}"`, `os_name = "Darwin"`, `arch_name = "x86_64"`} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %s in:\n%s", want, text)
		}
	}
	m, err := ParseManifest(text, "")
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	res, _ := Check(m, rel, Options{})
	if res.Errors != 0 {
		t.Fatalf("%s\n%s", text, dump(res))
	}
	// The next version must be checked with the same manifest.
	res, _ = Check(m, rel, Options{Version: "1.5.0"})
	if codes(res, SevError)[CodeMissingArtifact] != 5 {
		t.Fatalf("checking 1.5.0 against the 1.4.2 files must find all 5 downloads missing:\n%s", dump(res))
	}
}

// TestRecordScenarios writes the outcome of every broken-release scenario
// to $RMQA_RECORD_DIR/broken-releases.txt for the published test results.
func TestRecordScenarios(t *testing.T) {
	dir := os.Getenv("RMQA_RECORD_DIR")
	if dir == "" {
		t.Skip("set RMQA_RECORD_DIR to record")
	}
	var b strings.Builder
	caught := 0
	good := checkRelease(t, goodRelease(t), testManifest)
	fmt.Fprintf(&b, "good release: %d errors, %d warnings (must be 0 and 0)\n\n", good.Errors, good.Warnings)
	for i, mu := range mutations {
		r := goodRelease(t)
		mu.apply(t, r)
		res := checkRelease(t, r, testManifest)
		got := codes(res, "")
		ok := !res.Ready(true)
		for _, c := range mu.want {
			if got[c] == 0 {
				ok = false
			}
		}
		if ok {
			caught++
		}
		var found []string
		for _, c := range sortedKeys(codes(res, SevError)) {
			found = append(found, c)
		}
		for _, c := range sortedKeys(codes(res, SevWarning)) {
			found = append(found, c+" (warning)")
		}
		status := "caught"
		if !ok {
			status = "MISSED"
		}
		fmt.Fprintf(&b, "%2d. %-62s %s: %s\n", i+1, mu.name, status, strings.Join(found, ", "))
	}
	fmt.Fprintf(&b, "\n%d of %d broken releases caught\n", caught, len(mutations))
	if err := os.WriteFile(filepath.Join(dir, "broken-releases.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}
