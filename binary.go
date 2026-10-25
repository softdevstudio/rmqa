package rmqa

import (
	"bytes"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"fmt"
	"path"
	"sort"
	"strings"
)

var knownOS = map[string]bool{
	"linux": true, "darwin": true, "windows": true, "freebsd": true, "openbsd": true,
	"netbsd": true, "android": true, "illumos": true, "solaris": true, "any": true,
}

var knownArch = map[string]bool{
	"amd64": true, "arm64": true, "386": true, "arm": true, "riscv64": true,
	"ppc64le": true, "ppc64": true, "s390x": true, "loong64": true, "mips64": true,
	"mips64le": true, "universal": true, "any": true,
}

// ExecInfo describes an executable file.
type ExecInfo struct {
	Format string   // "ELF", "Mach-O", "Mach-O universal", "PE"
	OS     string   // linux, darwin, windows, freebsd, ...
	Arches []string // one, or several for a macOS universal binary
}

func (x ExecInfo) String() string {
	return x.OS + "/" + strings.Join(x.Arches, "+")
}

// Identify reports what kind of executable data is, or ok=false when it is
// not an ELF, Mach-O or PE program at all.
func Identify(data []byte) (ExecInfo, bool) {
	r := bytes.NewReader(data)
	switch {
	case len(data) >= 4 && bytes.Equal(data[:4], []byte("\x7fELF")):
		f, err := elf.NewFile(r)
		if err != nil {
			return ExecInfo{Format: "ELF (damaged)"}, false
		}
		defer f.Close()
		return ExecInfo{Format: "ELF", OS: elfOS(f), Arches: []string{elfArch(f)}}, true
	case len(data) >= 4 && (isMachoMagic(data[:4])):
		f, err := macho.NewFile(r)
		if err != nil {
			return ExecInfo{Format: "Mach-O (damaged)"}, false
		}
		defer f.Close()
		return ExecInfo{Format: "Mach-O", OS: "darwin", Arches: []string{machoArch(f.Cpu)}}, true
	case len(data) >= 4 && (bytes.Equal(data[:4], []byte{0xca, 0xfe, 0xba, 0xbe})):
		f, err := macho.NewFatFile(r)
		if err != nil {
			// 0xcafebabe is also the magic of Java class files.
			return ExecInfo{}, false
		}
		defer f.Close()
		var arches []string
		for _, a := range f.Arches {
			arches = append(arches, machoArch(a.Cpu))
		}
		sort.Strings(arches)
		return ExecInfo{Format: "Mach-O universal", OS: "darwin", Arches: arches}, true
	case len(data) >= 2 && data[0] == 'M' && data[1] == 'Z':
		f, err := pe.NewFile(r)
		if err != nil {
			return ExecInfo{Format: "PE (damaged)"}, false
		}
		defer f.Close()
		return ExecInfo{Format: "PE", OS: "windows", Arches: []string{peArch(f.Machine)}}, true
	}
	return ExecInfo{}, false
}

func isMachoMagic(b []byte) bool {
	for _, m := range [][]byte{{0xfe, 0xed, 0xfa, 0xce}, {0xce, 0xfa, 0xed, 0xfe}, {0xfe, 0xed, 0xfa, 0xcf}, {0xcf, 0xfa, 0xed, 0xfe}} {
		if bytes.Equal(b, m) {
			return true
		}
	}
	return false
}

func elfOS(f *elf.File) string {
	switch f.OSABI {
	case elf.ELFOSABI_FREEBSD:
		return "freebsd"
	case elf.ELFOSABI_NETBSD:
		return "netbsd"
	case elf.ELFOSABI_OPENBSD:
		return "openbsd"
	case elf.ELFOSABI_SOLARIS:
		return "solaris"
	}
	// Most systems leave OSABI at 0 and say who they are in a note section.
	for _, p := range f.Progs {
		if p.Type != elf.PT_NOTE {
			continue
		}
		b := make([]byte, p.Filesz)
		if _, err := p.ReadAt(b, 0); err != nil {
			continue
		}
		switch {
		case bytes.Contains(b, []byte("FreeBSD\x00")):
			return "freebsd"
		case bytes.Contains(b, []byte("OpenBSD\x00")):
			return "openbsd"
		case bytes.Contains(b, []byte("NetBSD\x00")):
			return "netbsd"
		case bytes.Contains(b, []byte("Android\x00")):
			return "android"
		}
	}
	return "linux"
}

func elfArch(f *elf.File) string {
	switch f.Machine {
	case elf.EM_X86_64:
		return "amd64"
	case elf.EM_AARCH64:
		return "arm64"
	case elf.EM_386:
		return "386"
	case elf.EM_ARM:
		return "arm"
	case elf.EM_RISCV:
		if f.Class == elf.ELFCLASS64 {
			return "riscv64"
		}
		return "riscv32"
	case elf.EM_PPC64:
		if f.Data == elf.ELFDATA2LSB {
			return "ppc64le"
		}
		return "ppc64"
	case elf.EM_S390:
		return "s390x"
	case elf.EM_LOONGARCH:
		return "loong64"
	case elf.EM_MIPS:
		if f.Class == elf.ELFCLASS64 {
			if f.Data == elf.ELFDATA2LSB {
				return "mips64le"
			}
			return "mips64"
		}
		return "mips"
	}
	return "unknown(" + f.Machine.String() + ")"
}

func machoArch(c macho.Cpu) string {
	switch c {
	case macho.CpuAmd64:
		return "amd64"
	case macho.CpuArm64:
		return "arm64"
	case macho.Cpu386:
		return "386"
	case macho.CpuArm:
		return "arm"
	case macho.CpuPpc64:
		return "ppc64"
	}
	return "unknown(" + c.String() + ")"
}

func peArch(m uint16) string {
	switch m {
	case pe.IMAGE_FILE_MACHINE_AMD64:
		return "amd64"
	case pe.IMAGE_FILE_MACHINE_ARM64:
		return "arm64"
	case pe.IMAGE_FILE_MACHINE_I386:
		return "386"
	case pe.IMAGE_FILE_MACHINE_ARMNT:
		return "arm"
	}
	return fmt.Sprintf("unknown(0x%x)", m)
}

// matchesPlatform reports whether an executable fits a platform.
func matchesPlatform(x ExecInfo, p *Platform) (osOK, archOK bool) {
	osOK = p.OS == "any" || x.OS == p.OS || (p.OS == "android" && x.OS == "linux")
	switch {
	case p.Arch == "any":
		archOK = true
	case p.Arch == "universal":
		archOK = len(x.Arches) >= 2
	default:
		for _, a := range x.Arches {
			if a == p.Arch {
				archOK = true
			}
		}
	}
	return
}

// containsVersion reports whether data holds the version string. The match
// must not continue a longer number on the left ("11.4.2" is not "1.4.2")
// and must not be followed by another dotted part ("1.4.2.1"). A digit
// straight after it is allowed, because compilers pack strings end to end:
// a Go binary can hold "rmqa 1.4.2" immediately followed by "1234".
func containsVersion(data []byte, version string) bool {
	if version == "" {
		return false
	}
	v := []byte(version)
	for off := 0; ; {
		i := bytes.Index(data[off:], v)
		if i < 0 {
			return false
		}
		i += off
		j := i + len(v)
		leftOK := i == 0 || (!isDigit(data[i-1]) && data[i-1] != '.')
		rightOK := !(j+1 < len(data) && data[j] == '.' && isDigit(data[j+1]))
		if leftOK && rightOK {
			return true
		}
		off = i + 1
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// matchAny reports whether name matches any of the patterns. Patterns use
// path.Match syntax; a pattern without a slash also matches the base name
// at any depth, so ".DS_Store" catches "docs/.DS_Store".
func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
		if !strings.Contains(p, "/") {
			if ok, _ := path.Match(p, path.Base(name)); ok {
				return true
			}
		}
		if strings.HasSuffix(p, "/*") {
			dir := strings.TrimSuffix(p, "/*")
			if name == dir || strings.HasPrefix(name, dir+"/") || strings.Contains(name, "/"+dir+"/") {
				return true
			}
		}
	}
	return false
}
