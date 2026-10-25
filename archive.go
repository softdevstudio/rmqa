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
	Link      string // symlink target
	Data      []byte
	Unsafe    string // why extracting this entry would write outside the folder
}

// Limits for reading archives, so a damaged or hostile archive can't
// exhaust memory.
var (
	MaxArchiveBytes int64 = 4 << 30 // total uncompressed bytes per archive
	MaxEntries            = 200000
)

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
		b, err := readLimited(zr, MaxArchiveBytes)
		if err != nil {
			return nil, err
		}
		inner := strings.TrimSuffix(path.Base(name), ".gz")
		if zr.Name != "" {
			inner = zr.Name
		}
		return []Entry{{Name: inner, Data: b}}, nil
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
	var total int64
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
		case tar.TypeSymlink, tar.TypeLink:
			e.Link = h.Linkname
		case tar.TypeReg, tar.TypeRegA, tar.TypeGNUSparse:
			b, err := readLimited(tr, MaxArchiveBytes-total)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", h.Name, err)
			}
			total += int64(len(b))
			e.Data = b
		default:
			// Devices, fifos and the like have no place in a release.
			e.Unsafe = "it is a special file (device or pipe)"
		}
		if u := unsafePath(e.Name, e.Link); u != "" && e.Unsafe == "" {
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
	var total int64
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
			b, err := readLimited(rc, MaxArchiveBytes-total)
			rc.Close()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Name, err)
			}
			total += int64(len(b))
			e.Data = b
		}
		if u := unsafePath(e.Name, e.Link); u != "" {
			e.Unsafe = u
		}
		out = append(out, e)
	}
	return out, nil
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
