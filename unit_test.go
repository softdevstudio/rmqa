package rmqa

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTOMLGood(t *testing.T) {
	src := "\ufeff# comment\nproject = \"x\" # trailing\nversion = 'lit\\eral'\nn = 1_000\nneg = -5\nb = true\n" +
		"list = [\n  \"a\", # one\n  \"b\",\n]\nempty = []\n\"quoted key\" = \"\\u00e9\\t\"\n\n[[artifact]]\nname = \"a\"\n[[artifact]]\nname = \"b\"\n"
	doc, err := parseTOML(src)
	if err != nil {
		t.Fatal(err)
	}
	r := doc.root
	want := map[string]any{"project": "x", "version": `lit\eral`, "n": int64(1000), "neg": int64(-5), "b": true, "quoted key": "é\t"}
	for k, v := range want {
		if fmt.Sprint(r.values[k].v) != fmt.Sprint(v) {
			t.Errorf("%s = %#v, want %#v", k, r.values[k].v, v)
		}
	}
	if l := r.values["list"].v.([]any); len(l) != 2 || l[1] != "b" {
		t.Errorf("list = %v", l)
	}
	if len(doc.arrays["artifact"]) != 2 || doc.arrays["artifact"][1].values["name"].v != "b" {
		t.Errorf("arrays of tables not read")
	}
	if r.values["b"].line != 6 {
		t.Errorf("line numbers are off: b on line %d", r.values["b"].line)
	}
}

func TestTOMLErrors(t *testing.T) {
	cases := map[string]string{
		"a = 1\na = 2":             "set twice",
		"a = \"open":               "never closed",
		"a = {x = 1}":              "inline tables",
		"a.b = 1":                  "dotted keys",
		"[a.b]":                    "dotted table",
		"a = 1.5":                  "whole numbers",
		"a = hello":                "need quotes",
		"a = [1, 2":                "never closed",
		"a = 1 b = 2":              "unexpected",
		"a = \"\"\"x\"\"\"":        "multi-line",
		"a = \"\\q\"":              "unknown escape",
		"[x]\n[x]":                 "appears twice",
		"= 1":                      "expected a key",
		"a 1":                      "expected =",
		"a = [\"x\" \"y\"]":        "expected , or ]",
		"a = 99999999999999999999": "not a valid number",
	}
	for src, want := range cases {
		_, err := parseTOML(src)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want an error with %q", src, err, want)
		}
	}
}

func TestManifestErrors(t *testing.T) {
	base := "[[platform]]\nos = \"linux\"\narch = \"amd64\"\n"
	cases := map[string]string{
		"": "no [[artifact]]",
		"projekt = \"x\"\n[[artifact]]\nname = \"a\"":                                 "unknown setting \"projekt\"",
		"[[artifact]]\nnmae = \"a\"":                                                  "unknown artifact setting \"nmae\"",
		"[[artifact]]\nfiles = [\"x\"]":                                               "needs a name",
		"[[artifact]]\nname = \"a_{os}\"":                                             "lists no [[platform]]",
		base + "[[artifact]]\nname = \"a_{oss}\"":                                     "unknown variable {oss}",
		"[[platform]]\nos = \"linux\"\n[[artifact]]\nname = \"a\"":                    "needs both os and arch",
		"[[platform]]\nos = \"macos\"\narch = \"arm64\"\n[[artifact]]\nname = \"a\"":  "unknown os \"macos\"",
		"[[platform]]\nos = \"linux\"\narch = \"x86_64\"\n[[artifact]]\nname = \"a\"": "unknown arch \"x86_64\"",
		base + base + "[[artifact]]\nname = \"a\"":                                    "listed twice",
		"[[artifact]]\nname = \"a\"\nchecksums = \"md5\"":                             "sha256",
		"[[artifact]]\nname = \"a\"\nbinary = true\nfiles = [\"x\"]":                  "itself the program",
		"[[artifact]]\nname = \"a\"\n[[artifact]]\nname = \"a\"":                      "also produced",
		"[[artifact]]\nname = \"a\"\nonly = [\"linux/*\"]":                            "per-platform name",
		base + "[[artifact]]\nname = \"a_{os}\"\nonly = [\"darwin/*\"]":               "matches none",
		"extra_files = \"maybe\"\n[[artifact]]\nname = \"a\"":                         "extra_files must be",
		"[release]\n[[artifact]]\nname = \"a\"":                                       "unknown section [release]",
		"[[artifact]]\nname = \"a\"\nmin_size = \"big\"":                              "whole number",
	}
	for src, want := range cases {
		m, err := ParseManifest(src, "")
		if err == nil && m != nil {
			_, err = m.Expand("1.0.0")
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q:\n got %v\nwant an error with %q", src, err, want)
		}
	}
}

func TestVersionResolution(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "VERSION"), []byte("v2.0.1\n"), 0o644)
	m, err := ParseManifest("version = \"1.0.0\"\nversion_file = \"VERSION\"\n[[artifact]]\nname = \"x-{version}.tar.gz\"", dir)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := m.ResolveVersion(""); v != "2.0.1" {
		t.Errorf("version_file should win over version, got %q", v)
	}
	if v, _ := m.ResolveVersion("v3.0.0"); v != "3.0.0" {
		t.Errorf("explicit version should win, got %q", v)
	}
	m2, _ := ParseManifest("[[artifact]]\nname = \"x-{version}.tar.gz\"", dir)
	if _, err := m2.Expand(""); err == nil || !strings.Contains(err.Error(), "no version is known") {
		t.Errorf("want a clear error when {version} is used without a version, got %v", err)
	}
}

func TestContainsVersion(t *testing.T) {
	cases := []struct {
		data string
		want bool
	}{
		{"hello 1.4.2\n", true},
		{"v1.4.2", true},
		{"1.4.2", true},
		{"go1.4.2", true},
		{"x11.4.2", false},
		{"1.4.21", true}, // packed strings: see containsVersion
		{"1.4.2.1", false},
		{"21.4.2 and later 1.4.2.", true},
		{"0.1.4.2", false},
		{"", false},
	}
	for _, c := range cases {
		if got := containsVersion([]byte(c.data), "1.4.2"); got != c.want {
			t.Errorf("%q: got %v want %v", c.data, got, c.want)
		}
	}
}

func TestParseSumLine(t *testing.T) {
	h := strings.Repeat("a", 64)
	cases := map[string]string{
		h + "  file.tar.gz":           "file.tar.gz",
		h + " *file.zip":              "file.zip",
		h + "  ./dist.tgz":            "dist.tgz",
		"SHA256 (my file.zip) = " + h: "my file.zip",
		h + "  name with spaces.zip":  "name with spaces.zip",
	}
	for l, want := range cases {
		sum, name, ok := parseSumLine(l)
		if !ok || name != want || sum != h {
			t.Errorf("%q: got %q %q %v", l, sum, name, ok)
		}
	}
	for _, bad := range []string{"justone", "zzzz  file", ""} {
		if _, _, ok := parseSumLine(bad); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func TestMatchAny(t *testing.T) {
	cases := []struct {
		pats []string
		name string
		want bool
	}{
		{[]string{".DS_Store"}, "docs/.DS_Store", true},
		{[]string{"__MACOSX/*"}, "__MACOSX/._x", true},
		{[]string{"__MACOSX/*"}, "a/__MACOSX/b", true},
		{[]string{"*.pem"}, "certs/key.pem", true},
		{[]string{"*.pem"}, "pem.txt", false},
		{[]string{"linux/*"}, "linux/arm64", true},
		{[]string{".git/*"}, "src/.git/config", true},
		{[]string{".env.*"}, ".env.production", true},
		{[]string{".env"}, "my.env", false},
	}
	for _, c := range cases {
		if got := matchAny(c.pats, c.name); got != c.want {
			t.Errorf("%v %q: got %v", c.pats, c.name, got)
		}
	}
}

func TestUnsafePath(t *testing.T) {
	for name, bad := range map[string]bool{
		"a/b":         false,
		"../a":        true,
		"a/../../b":   true,
		"/etc/passwd": true,
		`C:\x`:        true,
		`..\x`:        true,
		"a..b":        false,
	} {
		if got := unsafePath(name, "") != ""; got != bad {
			t.Errorf("%q: got %v", name, got)
		}
	}
	if unsafePath("bin/x", "../lib/y") != "" {
		t.Error("a link inside the folder is fine")
	}
	if unsafePath("x", "../../y") == "" {
		t.Error("a link that climbs out must be caught")
	}
}

func TestZipBombIsRefused(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("big")
	chunk := make([]byte, 1<<20)
	for i := 0; i < 64; i++ {
		w.Write(chunk)
	}
	zw.Close()
	old := MaxArchiveBytes
	MaxArchiveBytes = 16 << 20
	defer func() { MaxArchiveBytes = old }()
	_, err := OpenArchive("bomb.zip", buf.Bytes())
	if err == nil || !strings.Contains(err.Error(), "damaged or hostile") {
		t.Fatalf("want a refusal, got %v (the zip is %d bytes and expands to 64 MB)", err, buf.Len())
	}
}

func TestIdentifyRejectsNonPrograms(t *testing.T) {
	for _, d := range [][]byte{nil, []byte("hello"), []byte("\x7fELF garbage"), []byte("MZ but not PE"), {0xca, 0xfe, 0xba, 0xbe, 0, 0, 0, 52}} {
		if _, ok := Identify(d); ok {
			t.Errorf("%q identified as a program", d)
		}
	}
}

func TestIdentifyRealPrograms(t *testing.T) {
	for _, p := range append(platforms, struct{ goos, goarch string }{"freebsd", "amd64"}, struct{ goos, goarch string }{"linux", "riscv64"}, struct{ goos, goarch string }{"windows", "arm64"}) {
		info, ok := Identify(testBinary(t, p.goos, p.goarch, version))
		if !ok || info.OS != p.goos || info.Arches[0] != p.goarch {
			t.Errorf("%s/%s identified as %v (%v)", p.goos, p.goarch, info, ok)
		}
	}
}

func TestGitHubFetch(t *testing.T) {
	r := goodRelease(t)
	dir := r.write(t)
	rel, _ := OpenDir(dir)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no token", 401)
			return
		}
		switch {
		case req.URL.Path == "/repos/o/r/releases/latest" || req.URL.Path == "/repos/o/r/releases/tags/v1.4.2":
			var assets []map[string]any
			for _, n := range rel.Names() {
				assets = append(assets, map[string]any{"name": n, "size": rel.Size(n), "url": srv.URL + "/assets/" + n})
			}
			json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.4.2", "assets": assets})
		case strings.HasPrefix(req.URL.Path, "/assets/"):
			if req.Header.Get("Accept") != "application/octet-stream" {
				http.Error(w, "wrong accept", 400)
				return
			}
			b, _ := rel.Read(strings.TrimPrefix(req.URL.Path, "/assets/"))
			w.Write(b)
		default:
			http.NotFound(w, req)
		}
	}))
	defer srv.Close()
	t.Setenv("RMQA_GITHUB_API", srv.URL)
	t.Setenv("GITHUB_TOKEN", "tok")
	for _, spec := range []string{"o/r", "o/r@v1.4.2"} {
		g, err := FetchGitHub(spec)
		if err != nil {
			t.Fatal(err)
		}
		m, _ := ParseManifest(testManifest, "")
		res, err := Check(m, g, Options{Version: g.Tag})
		if err != nil {
			t.Fatal(err)
		}
		if res.Errors != 0 || !strings.Contains(res.Source, "o/r release v1.4.2") {
			t.Fatalf("%s:\n%s", spec, dump(res))
		}
	}
	if _, err := FetchGitHub("o/r@v9.9.9"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("a missing tag should report GitHub's 404, got %v", err)
	}
	if _, err := FetchGitHub("just-a-name"); err == nil {
		t.Error("a bad spec must fail")
	}
}

func TestReportFormats(t *testing.T) {
	r := goodRelease(t)
	r.archive("linux_arm64").file("hello").data = testBinary(t, "linux", "amd64", version)
	res := checkRelease(t, r, testManifest)
	var b bytes.Buffer
	if err := WriteJSON(&b, res, false); err != nil {
		t.Fatal(err)
	}
	var back struct {
		Ready    bool      `json:"ready"`
		Errors   int       `json:"errors"`
		Findings []Finding `json:"findings"`
	}
	if err := json.Unmarshal(b.Bytes(), &back); err != nil || back.Ready || back.Errors != res.Errors {
		t.Fatalf("json: %v %+v", err, back)
	}
	b.Reset()
	WriteGitHub(&b, res, false)
	if !strings.Contains(b.String(), "::error title=rmqa wrong-arch::hello_linux_arm64.tar.gz > hello: is built for linux/amd64") {
		t.Errorf("github format:\n%s", b.String())
	}
	b.Reset()
	WriteMarkdown(&b, res, false)
	if !strings.Contains(b.String(), "❌ Not ready") || !strings.Contains(b.String(), "| `hello_linux_arm64.tar.gz` | linux/arm64 |") {
		t.Errorf("markdown:\n%s", b.String())
	}
}

func FuzzTOML(f *testing.F) {
	f.Add(testManifest)
	f.Add("a = [\"x\", [\"y\"], 1, true]\n[[t]]\nb = 'c'")
	f.Add("a = \"\\u00e9\"")
	f.Fuzz(func(t *testing.T, src string) {
		m, err := ParseManifest(src, "")
		if err == nil {
			m.Expand("1.0.0")
		}
	})
}

func FuzzOpenArchive(f *testing.F) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("a/b.txt")
	w.Write([]byte("hello"))
	zw.Close()
	f.Add("x.zip", buf.Bytes())
	f.Add("x.tar.gz", pack(f, &tarchive{name: "x.tar.gz", files: []tfile{{name: "a", mode: 0o755, data: []byte("\x7fELF")}}}))
	f.Add("x.gz", []byte{0x1f, 0x8b})
	f.Fuzz(func(t *testing.T, name string, data []byte) {
		old := MaxArchiveBytes
		MaxArchiveBytes = 8 << 20
		defer func() { MaxArchiveBytes = old }()
		ents, err := OpenArchive(name, data)
		if err == nil {
			stripRoot(ents, "auto")
		}
		Identify(data)
	})
}
