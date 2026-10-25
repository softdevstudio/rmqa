package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A one-platform release for this machine, so `run` really runs.
func release(t *testing.T, ver string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(t.TempDir(), "hello")
	cmd := exec.Command("go", "build", "-ldflags", "-X main.version="+ver, "-o", bin, "../../testdata/hello")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	data, _ := os.ReadFile(bin)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		mode int64
		data []byte
	}{{"hello", 0o755, data}, {"LICENSE", 0o644, []byte("license")}} {
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.data)), Typeflag: tar.TypeReg})
		tw.Write(f.data)
	}
	tw.Close()
	gz.Close()
	name := "hello_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644)
	sum := sha256.Sum256(buf.Bytes())
	os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(hex.EncodeToString(sum[:])+"  "+name+"\n"), 0o644)
	return dir
}

const manifest = `project = "hello"
version_file = "VERSION"

[[platform]]
os = "` + runtime.GOOS + `"
arch = "` + runtime.GOARCH + `"

[[artifact]]
name = "hello_{os}_{arch}.tar.gz"
binaries = ["hello{exe}"]
files = ["LICENSE"]
run = ["version"]

[[artifact]]
name = "SHA256SUMS"
checksums = "sha256"
`

func setup(t *testing.T) (dir, man string) {
	dir = release(t, "2.0.0")
	md := t.TempDir()
	man = filepath.Join(md, "rmqa.toml")
	os.WriteFile(man, []byte(manifest), 0o644)
	os.WriteFile(filepath.Join(md, "VERSION"), []byte("2.0.0\n"), 0o644)
	return
}

func runCLI(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestCheckPasses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test release is built for Unix")
	}
	dir, man := setup(t)
	code, out, errs := runCLI("check", dir, "--manifest", man)
	if code != 0 || !strings.Contains(out, "Ready to publish: 0 errors, 0 warnings in 2 files.") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errs)
	}
}

func TestCheckWrongVersionRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test release is built for Unix")
	}
	dir, man := setup(t)
	code, out, _ := runCLI("check", "--manifest", man, "--version", "2.0.1", dir)
	if code != 1 || !strings.Contains(out, "version-missing") || !strings.Contains(out, `printed "hello 2.0.0"`) {
		t.Fatalf("exit %d\n%s", code, out)
	}
	code, out, _ = runCLI("check", "--manifest", man, "--version", "2.0.1", "--no-run", dir)
	if code != 1 || strings.Contains(out, "printed") {
		t.Fatalf("--no-run must not run the program: exit %d\n%s", code, out)
	}
	t.Setenv("RMQA_VERSION", "2.0.1")
	if code, _, _ := runCLI("check", "--manifest", man, dir); code != 1 {
		t.Fatalf("RMQA_VERSION must be used, exit %d", code)
	}
}

func TestCheckJSONAndStrict(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test release is built for Unix")
	}
	dir, man := setup(t)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	md := filepath.Dir(man)
	os.WriteFile(man, []byte(strings.Replace(manifest, "version_file", "extra_files = \"warn\"\nversion_file", 1)), 0o644)
	_ = md
	code, out, _ := runCLI("check", dir, "--manifest", man, "--format", "json")
	var res struct {
		Ready    bool `json:"ready"`
		Warnings int  `json:"warnings"`
		Errors   int  `json:"errors"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// notes.txt is a warning (extra_files = "warn"), and SHA256SUMS doesn't
	// list it, which is an error.
	if code != 1 || res.Errors != 1 || res.Warnings != 1 {
		t.Fatalf("exit %d %+v\n%s", code, res, out)
	}
}

func TestUsageErrors(t *testing.T) {
	cases := [][]string{
		{},
		{"frobnicate"},
		{"check", "--manifest", "/does/not/exist.toml"},
		{"check", "--format", "xml"},
		{"check", "--bogus"},
		{"inspect"},
		{"init", "/does/not/exist"},
	}
	for _, c := range cases {
		if code, _, _ := runCLI(c...); code != 2 {
			t.Errorf("%v: exit %d, want 2", c, code)
		}
	}
	if code, out, _ := runCLI("version"); code != 0 || out != "rmqa "+Version+"\n" {
		t.Errorf("version: %d %q", code, out)
	}
	if code, out, _ := runCLI("help"); code != 0 || !strings.Contains(out, "rmqa check") {
		t.Errorf("help: %d", code)
	}
}

func TestInitAndInspect(t *testing.T) {
	dir := release(t, "3.1.4")
	code, out, errs := runCLI("init", dir, "--version", "3.1.4")
	if code != 0 || !strings.Contains(out, `binaries = ["hello{exe}"]`) || strings.Contains(out, "Warning") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errs)
	}
	target := filepath.Join(t.TempDir(), "rmqa.toml")
	if code, _, errs := runCLI("init", dir, "-o", target); code != 0 {
		t.Fatalf("init -o: %s", errs)
	}
	if code, _, _ := runCLI("init", dir, "-o", target); code != 2 {
		t.Fatal("init must not overwrite an existing manifest")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.tar.gz"))
	code, out, _ = runCLI("inspect", matches[0])
	if code != 0 || !strings.Contains(out, "-rwxr-xr-x  hello") || !strings.Contains(out, runtime.GOOS+"/"+runtime.GOARCH) {
		t.Fatalf("inspect: %d\n%s", code, out)
	}
}
