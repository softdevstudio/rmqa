package rmqa

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
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

// Many small files can't add up in memory: each keeps a short copy that
// counts against the same budget as programs.
func TestManySmallFilesAreBounded(t *testing.T) {
	var es []tarEntry
	for i := 0; i < 2000; i++ {
		es = append(es, tarEntry{name: fmt.Sprintf("f%d", i), typ: tar.TypeReg, size: 1, fill: 'x'})
	}
	got, err := OpenArchive("x.tar.gz", makeTarGz(t, es))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		if cap(e.Data) > keepBytes {
			t.Fatalf("%s keeps %d bytes of capacity", e.Name, cap(e.Data))
		}
	}
	old := MaxProgramBytes
	MaxProgramBytes = 1000
	defer func() { MaxProgramBytes = old }()
	if _, err := OpenArchive("x.tar.gz", makeTarGz(t, es)); err == nil {
		t.Fatal("2000 kept heads over a 1000-byte budget should be refused")
	}
}

// A cut-off upload is reported as damage, not as a size problem, and a
// small cut-off file isn't taken as a short good one.
func TestTruncatedArchives(t *testing.T) {
	prog := append([]byte("\x7fELF"), bytes.Repeat([]byte{1, 2, 3, 4, 5, 6, 7}, 20000)...)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	tw.WriteHeader(&tar.Header{Name: "prog", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(prog))})
	tw.Write(prog)
	tw.Close()
	zw.Close()
	half := buf.Bytes()[:buf.Len()/2]
	_, err := OpenArchive("x.tar.gz", half)
	if err == nil || strings.Contains(err.Error(), "add up to") {
		t.Fatalf("cut-off archive: %v", err)
	}
	var small bytes.Buffer
	gw := gzip.NewWriter(&small)
	gw.Write(bytes.Repeat([]byte("abcdefghij"), 360))
	gw.Close()
	if _, err := OpenArchive("notes.gz", small.Bytes()[:small.Len()-8]); err == nil {
		t.Fatal("a .gz without its trailer was accepted")
	}
}

// A chain of links that each look harmless still leads outside.
func TestSymlinkChain(t *testing.T) {
	es, err := OpenArchive("x.tar.gz", makeTarGz(t, []tarEntry{
		{name: "d/", typ: tar.TypeDir},
		{name: "d/l", link: "..", typ: tar.TypeSymlink},
		{name: "d/l/m", link: "..", typ: tar.TypeSymlink},
		{name: "d/l/m/evil", typ: tar.TypeReg, size: 1, fill: 'x'},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if es[3].Unsafe == "" {
		t.Error("file under a chain of links not caught")
	}
}
