package rmqa

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

type tarEntry struct {
	name, link string
	typ        byte
	size       int64
	fill       byte
}

func makeTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Linkname: e.link, Typeflag: e.typ, Mode: 0o755, Size: e.size}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			chunk := bytes.Repeat([]byte{e.fill}, 1<<20)
			for left := e.size; left > 0; {
				n := min(left, int64(len(chunk)))
				tw.Write(chunk[:n])
				left -= n
			}
		}
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

// A large file that isn't a program is counted, not held in memory.
func TestLargeFileIsNotHeld(t *testing.T) {
	data := makeTarGz(t, []tarEntry{{name: "big.dat", typ: tar.TypeReg, size: 64 << 20, fill: 'a'}})
	es, err := OpenArchive("x.tar.gz", data)
	if err != nil {
		t.Fatal(err)
	}
	if es[0].Size != 64<<20 || len(es[0].Data) > headBytes {
		t.Fatalf("size %d, held %d bytes", es[0].Size, len(es[0].Data))
	}
}

// Programs are held in memory, so their total is capped.
func TestProgramBytesAreCapped(t *testing.T) {
	old := MaxProgramBytes
	MaxProgramBytes = 8 << 20
	defer func() { MaxProgramBytes = old }()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	body := append([]byte("\x7fELF"), make([]byte, 16<<20)...)
	tw.WriteHeader(&tar.Header{Name: "prog", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))})
	tw.Write(body)
	tw.Close()
	zw.Close()
	if _, err := OpenArchive("x.tar.gz", buf.Bytes()); err == nil || !strings.Contains(err.Error(), "damaged or hostile") {
		t.Fatalf("want a refusal, got %v", err)
	}
}

// A tar hard link names its target from the top of the archive.
func TestHardLinkOutside(t *testing.T) {
	data := makeTarGz(t, []tarEntry{
		{name: "pkg/a/b", link: "../etc/passwd", typ: tar.TypeLink},
		{name: "pkg/ok", link: "pkg/a/c", typ: tar.TypeLink},
	})
	es, err := OpenArchive("x.tar.gz", data)
	if err != nil {
		t.Fatal(err)
	}
	if es[0].Unsafe == "" {
		t.Error("hard link to ../etc/passwd not caught")
	}
	if es[1].Unsafe != "" {
		t.Errorf("hard link inside the archive flagged: %s", es[1].Unsafe)
	}
}

// tar -C stage . around a top folder writes a "./" entry first.
func TestDotEntryWithTopFolder(t *testing.T) {
	data := makeTarGz(t, []tarEntry{
		{name: "./", typ: tar.TypeDir},
		{name: "./pkg/", typ: tar.TypeDir},
		{name: "./pkg/bin", typ: tar.TypeReg, size: 10, fill: 'x'},
	})
	es, err := OpenArchive("x.tar.gz", data)
	if err != nil {
		t.Fatal(err)
	}
	out, top, err := stripRoot(es, "auto")
	if err != nil || top != "pkg" || len(out) != 1 || out[0].Name != "bin" {
		t.Fatalf("top %q, %d entries, err %v", top, len(out), err)
	}
	if _, _, err := stripRoot(es, "pkg"); err != nil {
		t.Fatalf("root = \"pkg\": %v", err)
	}
}

// The file name stored in a .gz header gets the same check as an entry.
func TestGzStoredNameIsChecked(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Name = "../../evil"
	zw.Write([]byte("x"))
	zw.Close()
	es, err := OpenArchive("tool.gz", buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if es[0].Unsafe == "" {
		t.Error("unsafe stored name not caught")
	}
}
