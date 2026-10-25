package rmqa

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
)

// Entry is one file inside an archive.
type Entry struct {
	Name      string // slash-separated, as stored
	Mode      fs.FileMode
	ModeKnown bool // false for zip files made on Windows, which store no Unix mode
	IsDir     bool
	Link      string // symlink or hard link target
	HardLink  bool   // Link is a tar hard link, relative to the archive root
	Size      int64  // uncompressed size in bytes
	// Data holds the whole file for programs (ELF, Mach-O and PE files,
	// recognised by their first bytes) and only the first bytes of anything
	// else, so a huge file in an archive is counted, not held in memory.
	Data   []byte
	Unsafe string // why extracting this entry would write outside the folder
}

// Limits for reading archives, so a damaged or hostile archive can't
// exhaust memory.
var (
	MaxArchiveBytes int64 = 4 << 30   // total uncompressed bytes per archive
	MaxProgramBytes int64 = 512 << 20 // bytes held in memory for programs, per archive
	MaxEntries            = 200000
)

// headBytes is how much of a file that isn't a program is kept: enough to
// tell a script or a damaged program from a real one.
const headBytes = 4096

// isProgramMagic reports whether b starts like an ELF, Mach-O or PE file.
func isProgramMagic(b []byte) bool {
	if len(b) >= 2 && b[0] == 'M' && b[1] == 'Z' {
		return true
	}
	if len(b) < 4 {
		return false
	}
	switch string(b[:4]) {
	case "\x7fELF", "\xfe\xed\xfa\xce", "\xfe\xed\xfa\xcf", "\xce\xfa\xed\xfe", "\xcf\xfa\xed\xfe", "\xca\xfe\xba\xbe":
		return true
	}
	return false
}

// entryReader reads entries of one archive, holding programs in memory and
// only counting everything else.
type entryReader struct {
	total, held int64
}

func (er *entryReader) read(name string, r io.Reader) ([]byte, int64, error) {
	head := make([]byte, headBytes)
	n, err := io.ReadFull(r, head)
	head = head[:n]
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, 0, err
	}
	er.total += int64(n)
	if er.total > MaxArchiveBytes {
		return nil, 0, fmt.Errorf("expands to more than %d bytes, which rmqa treats as damaged or hostile", MaxArchiveBytes)
	}
	if n < headBytes {
		if isProgramMagic(head) {
			er.held += int64(n)
		}
		return head, int64(n), nil
	}
	if isProgramMagic(head) {
		rest, err := readLimited(r, MaxProgramBytes-er.held-int64(n))
		if err != nil {
			return nil, 0, fmt.Errorf("the programs in it add up to more than %d bytes, which rmqa treats as damaged or hostile", MaxProgramBytes)
		}
		b := append(head, rest...)
		er.held += int64(len(b))
		er.total += int64(len(rest))
		if er.total > MaxArchiveBytes {
			return nil, 0, fmt.Errorf("expands to more than %d bytes, which rmqa treats as damaged or hostile", MaxArchiveBytes)
		}
		return b, int64(len(b)), nil
	}
	m, err := io.Copy(io.Discard, io.LimitReader(r, MaxArchiveBytes-er.total+1))
	if err != nil {
		return nil, 0, err
	}
	er.total += m
	if er.total > MaxArchiveBytes {
		return nil, 0, fmt.Errorf("expands to more than %d bytes, which rmqa treats as damaged or hostile", MaxArchiveBytes)
	}
	return head, int64(n) + m, nil
}

var errNotArchive = errors.New("not an archive")

// archiveKind returns the archive type from a file name, or "".
func archiveKind(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".tar.gz"), strings.HasSuffix(n, ".tgz"):
		return "tar.gz"
	case strings.HasSuffix(n, ".tar.bz2"), strings.HasSuffix(n, ".tbz2"), strings.HasSuffix(n, ".tbz"):
		return "tar.bz2"
	case strings.HasSuffix(n, ".tar"):
		return "tar"
	case strings.HasSuffix(n, ".zip"):
		return "zip"
	case strings.HasSuffix(n, ".gz"):
		return "gz"
	case strings.HasSuffix(n, ".tar.xz"), strings.HasSuffix(n, ".txz"), strings.HasSuffix(n, ".tar.zst"),
		strings.HasSuffix(n, ".7z"), strings.HasSuffix(n, ".rar"), strings.HasSuffix(n, ".dmg"),
		strings.HasSuffix(n, ".deb"), strings.HasSuffix(n, ".rpm"), strings.HasSuffix(n, ".msi"):
		return "unsupported"
	}
	return ""
}

// OpenArchive reads every entry of an archive held in memory.
func OpenArchive(name string, data []byte) ([]Entry, error) {
	switch archiveKind(name) {
	case "tar.gz":
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		return readTar(zr)
	case "tar.bz2":
		return readTar(bzip2.NewReader(bytes.NewReader(data)))
	case "tar":
		return readTar(bytes.NewReader(data))
	case "zip":
		return readZip(data)
	case "gz":
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		var er entryReader
		b, size, err := er.read(name, zr)
		if err != nil {
			return nil, err
		}
		inner := strings.TrimSuffix(path.Base(name), ".gz")
		e := Entry{Name: inner, Data: b, Size: size}
		// The name stored inside the gzip header is what gunzip -N would
		// write, so it gets the same check as an archive entry.
		if zr.Name != "" {
			e.Name = zr.Name
			e.Unsafe = unsafePath(zr.Name, "")
		}
		return []Entry{e}, nil
	case "unsupported":
		return nil, fmt.Errorf("rmqa can't open this kind of archive yet (it reads .tar.gz, .tgz, .tar.bz2, .tar, .zip and .gz)")
	}
	return nil, errNotArchive
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("expands to more than %d bytes, which rmqa treats as damaged or hostile", limit)
	}
	return b, nil
}

func readTar(r io.Reader) ([]Entry, error) {
	tr := tar.NewReader(r)
	var out []Entry
	var er entryReader
	for {
		h, err := tr.Next()
		if err == io.EOF {
			if len(out) == 0 {
				return nil, fmt.Errorf("the archive is empty")
			}
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w", err)
		}
		if len(out) >= MaxEntries {
			return nil, fmt.Errorf("more than %d entries", MaxEntries)
		}
		e := Entry{Name: h.Name, Mode: h.FileInfo().Mode(), ModeKnown: true}
		switch h.Typeflag {
		case tar.TypeDir:
			e.IsDir = true
		case tar.TypeSymlink:
			e.Link = h.Linkname
		case tar.TypeLink:
			e.Link = h.Linkname
			e.HardLink = true
		case tar.TypeReg, tar.TypeRegA, tar.TypeGNUSparse:
			b, size, err := er.read(h.Name, tr)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", h.Name, err)
			}
			e.Data, e.Size = b, size
		default:
			// Devices, fifos and the like have no place in a release.
			e.Unsafe = "it is a special file (device or pipe)"
		}
		if u := unsafeEntry(e); u != "" && e.Unsafe == "" {
			e.Unsafe = u
		}
		out = append(out, e)
	}
}

func readZip(data []byte) ([]Entry, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("zip: %w", err)
	}
	if len(zr.File) == 0 {
		return nil, fmt.Errorf("the archive is empty")
	}
	if len(zr.File) > MaxEntries {
		return nil, fmt.Errorf("more than %d entries", MaxEntries)
	}
	var out []Entry
	var er entryReader
	for _, f := range zr.File {
		e := Entry{Name: f.Name, Mode: f.Mode()}
		// The upper byte of CreatorVersion says which system made the
		// entry; 3 is Unix, and only then is the stored mode real.
		e.ModeKnown = f.CreatorVersion>>8 == 3
		if f.FileInfo().IsDir() || strings.HasSuffix(f.Name, "/") {
			e.IsDir = true
		} else if e.Mode&fs.ModeSymlink != 0 {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Name, err)
			}
			b, err := readLimited(rc, 4096)
			rc.Close()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Name, err)
			}
			e.Link = string(b)
		} else {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Name, err)
			}
			b, size, err := er.read(f.Name, rc)
			rc.Close()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Name, err)
			}
			e.Data, e.Size = b, size
		}
		if u := unsafeEntry(e); u != "" {
			e.Unsafe = u
		}
		out = append(out, e)
	}
	return out, nil
}

// unsafeEntry is unsafePath for an entry. A tar hard link names its target
// from the top of the archive, not from the link's own folder.
func unsafeEntry(e Entry) string {
	if !e.HardLink {
		return unsafePath(e.Name, e.Link)
	}
	if u := unsafePath(e.Name, ""); u != "" {
		return u
	}
	if u := unsafePath(e.Link, ""); u != "" {
		return "it is a hard link to a path outside the folder (" + e.Link + ")"
	}
	return ""
}

// unsafePath explains why an entry would land outside the folder it is
// unpacked into ("zip slip"), or returns "".
func unsafePath(name, link string) string {
	n := strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(n, "/") || (len(n) > 1 && n[1] == ':') {
		return "its path is absolute"
	}
	for _, part := range strings.Split(n, "/") {
		if part == ".." {
			return "its path climbs out of the folder with .."
		}
	}
	if link != "" {
		l := strings.ReplaceAll(link, `\`, "/")
		if strings.HasPrefix(l, "/") {
			return "it is a link to an absolute path (" + link + ")"
		}
		target := path.Join(path.Dir(n), l)
		if target == ".." || strings.HasPrefix(target, "../") {
			return "it is a link that points outside the folder (" + link + ")"
		}
	}
	return ""
}

// stripRoot returns the entries with a single shared top folder removed
// (root "auto"), a named folder removed, or unchanged (root "none"). It
// reports the folder that was removed.
func stripRoot(entries []Entry, root string) ([]Entry, string, error) {
	clean := func(n string) string {
		n = strings.TrimPrefix(strings.ReplaceAll(n, `\`, "/"), "./")
		return strings.TrimSuffix(n, "/")
	}
	if root == "none" {
		out := make([]Entry, len(entries))
		for i, e := range entries {
			e.Name = clean(e.Name)
			out[i] = e
		}
		return out, "", nil
	}
	top := root
	if root == "auto" {
		top = ""
		files := 0
		for _, e := range entries {
			n := clean(e.Name)
			if n == "" || n == "." {
				continue
			}
			first, restOf, nested := strings.Cut(n, "/")
			if !nested && !e.IsDir {
				return stripRoot(entries, "none") // a file at the top: no wrapping folder
			}
			_ = restOf
			if top == "" {
				top = first
			} else if top != first {
				return stripRoot(entries, "none")
			}
			if !e.IsDir {
				files++
			}
		}
		if top == "" || files == 0 {
			return stripRoot(entries, "none")
		}
	}
	var out []Entry
	found := false
	for _, e := range entries {
		n := clean(e.Name)
		if n == "" || n == "." {
			continue // the "./" entry of an archive made with tar -C dir .
		}
		if n == top && e.IsDir {
			found = true
			continue
		}
		if !strings.HasPrefix(n, top+"/") {
			return nil, "", fmt.Errorf("expected everything inside the folder %q, but %q is outside it", top, e.Name)
		}
		found = true
		e.Name = strings.TrimPrefix(n, top+"/")
		out = append(out, e)
	}
	if !found {
		return nil, "", fmt.Errorf("expected the folder %q inside the archive", top)
	}
	return out, top, nil
}
