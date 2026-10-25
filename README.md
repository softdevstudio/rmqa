# rmqa

**Release manifest QA.** rmqa checks a software release before you publish
it. You describe once what every release must contain, and rmqa opens the
real archives and programs and tells you, file by file, what is missing or
wrong.

It catches the mistakes that slip past a green CI run:

- the arm64 archive holds the amd64 program, or the macOS download holds a
  Linux build
- one archive still has last week's build inside
- a download for one platform was never uploaded
- the checksums file doesn't match an archive that was rebuilt later, or
  doesn't list every file
- the program lost its executable permission, so users get "permission denied"
- LICENSE or NOTICE is missing or empty
- a `.env` file or a private key was packed by accident
- an archive entry would unpack outside the folder (`../../`)

rmqa is one small program with nothing to install beside it. It runs on
your machine before you publish, and in CI as a gate. Open source under the
Apache License 2.0. Website: [rmqa.com](https://rmqa.com).

```
$ rmqa check dist
Checking dist for rmqa 0.1.0

  FAIL  rmqa_linux_amd64.tar.gz   linux/amd64
          rmqa: is byte-for-byte the same program as rmqa_linux_arm64.tar.gz:rmqa, which is meant for a different platform [duplicate-binary]
  FAIL  rmqa_linux_arm64.tar.gz   linux/arm64
          rmqa: is built for linux/amd64, but this download is for linux/arm64 [wrong-arch]
          rmqa: is byte-for-byte the same program as rmqa_linux_amd64.tar.gz:rmqa, which is meant for a different platform [duplicate-binary]
  ok    rmqa_darwin_arm64.tar.gz  darwin/arm64
  FAIL  rmqa_darwin_amd64.tar.gz  darwin/amd64
          missing from the release (the darwin/amd64 download) [missing-artifact]
  ok    rmqa_windows_amd64.zip    windows/amd64
  FAIL  SHA256SUMS
          rmqa_darwin_amd64.tar.gz: is listed, but there is no such file in the release [checksum-stale-entry]
          rmqa_linux_arm64.tar.gz: listed as ce197bcf55a1… but the file's sha256 is 3bee3a27e9f5…; the file changed after the checksums were made, or the download is damaged [checksum-mismatch]

Not ready: 6 errors, 0 warnings in 6 files.
```

## Install

Download the archive for your system and put `rmqa` on your PATH. The links
always point to the latest release.

| System | Download |
|---|---|
| Linux, x86-64 | [rmqa_linux_amd64.tar.gz](https://github.com/softdevstudio/rmqa/releases/latest/download/rmqa_linux_amd64.tar.gz) |
| Linux, ARM64 | [rmqa_linux_arm64.tar.gz](https://github.com/softdevstudio/rmqa/releases/latest/download/rmqa_linux_arm64.tar.gz) |
| macOS, Apple silicon | [rmqa_darwin_arm64.tar.gz](https://github.com/softdevstudio/rmqa/releases/latest/download/rmqa_darwin_arm64.tar.gz) |
| macOS, Intel | [rmqa_darwin_amd64.tar.gz](https://github.com/softdevstudio/rmqa/releases/latest/download/rmqa_darwin_amd64.tar.gz) |
| Windows, x86-64 | [rmqa_windows_amd64.zip](https://github.com/softdevstudio/rmqa/releases/latest/download/rmqa_windows_amd64.zip) |

Checksums are in [SHA256SUMS](https://github.com/softdevstudio/rmqa/releases/latest/download/SHA256SUMS).
Every rmqa release is checked by rmqa before it is published.

With Go 1.24 or later:

```sh
go install github.com/softdevstudio/rmqa/cmd/rmqa@latest
```

## Quick start

**1. Describe your release.** If you have a release that you know is good,
let rmqa write the manifest from it:

```sh
rmqa init dist --version 1.4.2 -o rmqa.toml
```

`rmqa init` finds the programs inside each archive, works out which
operating system and processor each one is for, spots the version and the
platform names in the file names, and turns them into a template. Look the
result over and commit it next to your code.

**2. Check every new release** before it goes out:

```sh
rmqa check dist --version 1.4.3
```

The exit status is 0 when the release is ready, 1 when it has problems and
2 when rmqa couldn't run (a missing manifest, a typo in it). That makes it a
gate in any CI system.

**3. Or check a release that is already on GitHub**, a draft included:

```sh
GITHUB_TOKEN=... rmqa check --github owner/repo@v1.4.3
```

## The manifest

The manifest is a small TOML file, `rmqa.toml` by default. This is the one
rmqa uses for its own releases:

```toml
project = "rmqa"
version_file = "VERSION"     # or version = "1.4.2", or --version on the command line

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
arch = "amd64"               # archive = "zip" and exe = ".exe" are the Windows defaults

[[artifact]]
name = "rmqa_{os}_{arch}.{archive}"   # one archive per platform
binaries = ["rmqa{exe}"]              # the program inside, checked against the platform
files = ["LICENSE", "NOTICE", "THIRD_PARTY_LICENSES.md", "README.md", "CHANGELOG.md"]
run = ["version"]                     # run it where it can run, and look for the version
min_size = 500000

[[artifact]]
name = "SHA256SUMS"
checksums = "sha256"                  # must list every other file, with the right hash
```

**Platforms.** Each `[[platform]]` needs `os` and `arch` in Go's spelling
(`linux`, `darwin`, `windows`, `freebsd`; `amd64`, `arm64`, `386`, `arm`,
`riscv64`, and `universal` for a macOS universal binary). Any other string
in a platform becomes a template variable, so names that spell platforms
their own way still fit:

```toml
[[platform]]
os = "darwin"
arch = "amd64"
target = "x86_64-apple-darwin"     # use as {target}

[[artifact]]
name = "tool-{version}-{target}.tar.gz"
```

**Template variables** in names, file lists and binaries: `{version}`,
`{project}`, `{os}`, `{arch}`, `{archive}` (`tar.gz`, or `zip` on Windows),
`{exe}` (`.exe` on Windows, empty elsewhere) and any variables your
platforms define. An artifact whose name uses a platform variable is
expected once for every platform.

**Artifact settings:**

| Setting | Meaning |
|---|---|
| `name` | File name, with template variables. Required. |
| `files` | Files that must be inside the archive and not empty. Patterns such as `licenses/*` work. |
| `binaries` | Programs inside the archive. rmqa checks that each is a real executable for the artifact's platform, that it's marked executable, and that it contains the version. |
| `binary = true` | The file itself is the program, for releases that ship bare executables. |
| `run` | Arguments that make the program print its version, such as `["--version"]`. rmqa runs it when it fits the machine rmqa runs on, and skips it otherwise. |
| `check_version = false` | Don't look for the version string inside the programs. |
| `root` | `"auto"` (default) removes one folder that wraps everything; `"none"` keeps paths as they are; or a template such as `"tool-{version}-{os}"` that must match exactly. |
| `checksums` | `"sha256"` or `"sha512"`: this file lists checksums of the other files. |
| `min_size`, `max_size` | Size limits in bytes. |
| `optional = true` | Missing is fine. |
| `only` | For per-platform artifacts that exist for some platforms only, for example `["linux/*"]`. |

**Top-level settings:** `project`, `version`, `version_file` (read relative
to the manifest), `extra_files` (`"error"` by default, or `"warn"` or
`"ignore"`) for files in the release that the manifest doesn't mention, and
`forbid`, a list of patterns that must never be inside an archive. The
default `forbid` list catches `.env`, `*.pem`, `*.key`, `id_rsa`,
`id_ed25519`, `.git/*`, `.npmrc` and `.pypirc`.

The version comes from, in order: `--version`, the `RMQA_VERSION`
environment variable, `version_file`, `version`, and for `--github` the
release tag. A leading `v` is dropped.

rmqa reads the parts of TOML a manifest needs: tables, arrays of tables,
strings, whole numbers, booleans and lists. Anything else, and any unknown
setting, is an error with its line number, so a typo can't quietly switch a
check off.

## Commands

```
rmqa check [DIR]       check the release files in DIR (default: dist)
rmqa init [DIR]        write a manifest from a release that is known to be good
rmqa inspect FILE...   show what is inside archives and programs
rmqa version           print the rmqa version
```

`check` options: `--manifest FILE`, `--version V`, `--github OWNER/REPO[@TAG]`,
`--format text|json|github|markdown`, `--strict` (fail on warnings too),
`--verbose` (show notes), `--no-run` (never run the programs).

`init` options: `--project NAME`, `--version V`, `--github OWNER/REPO[@TAG]`,
`-o FILE` (it won't overwrite an existing file). The manifest it writes is
checked against the release it came from before it's printed.

`--format github` prints each problem as a GitHub Actions annotation, and
writes a summary table to the job summary when `$GITHUB_STEP_SUMMARY` is set.
`--format json` gives the full result for your own scripts.

rmqa reads `.tar.gz`, `.tgz`, `.tar.bz2`, `.tar`, `.zip` and `.gz` archives.
Other files, such as `.deb`, `.msi` or `.dmg`, can be listed in the manifest
and are checked for presence, size and checksums, but not opened.

## In GitHub Actions

Check the release files your workflow built, before the step that publishes
them:

```yaml
- uses: softdevstudio/rmqa@v0.1.0
  with:
    dir: dist
    version: ${{ github.ref_name }}
```

The action downloads the rmqa binary for the runner and runs
`rmqa check --format github`. Inputs: `dir`, `manifest`, `version`,
`github`, `strict` and `rmqa-version`.

Any other CI system can run the binary directly and use the exit status.

## What each finding means

Every finding has a stable code, so scripts can match on it.

| Code | Severity | What happened |
|---|---|---|
| `missing-artifact` | error | A file the manifest expects is not in the release. |
| `extra-file` | error (configurable) | A file in the release that the manifest doesn't mention. |
| `empty-file` | error | A release file, a required file or a program is 0 bytes. |
| `size-out-of-range` | error | Outside `min_size` or `max_size`. |
| `archive-corrupt` | error | The archive can't be read to the end: cut off, damaged, or too large to be real. |
| `unsupported-archive` | error | Files were listed for an archive type rmqa can't open. |
| `archive-layout` | error | The archive isn't laid out as `root` says. |
| `unsafe-path` | error | An entry would unpack outside the target folder: `..`, an absolute path, or a link out. |
| `forbidden-file` | error | A file matching `forbid`, such as `.env` or a private key. |
| `junk-file` | warning | Clutter such as `.DS_Store` or `__MACOSX/`. |
| `missing-file` | error | A file listed in `files` is not in the archive. |
| `missing-binary` | error | A program listed in `binaries` is not in the archive, or is only a link. |
| `not-executable` | error | The program isn't an ELF, Mach-O or PE executable (a script, a text file, a damaged build). |
| `wrong-os` | error | The program was built for another operating system. |
| `wrong-arch` | error | The program was built for another processor. |
| `duplicate-binary` | error | Two platforms' downloads hold the same program, byte for byte. |
| `no-exec-bit` | error | The program isn't marked executable in a tar archive. |
| `exec-bit-unknown` | warning | A zip made on Windows stores no Unix permissions, so the program won't be executable after unpacking on Linux or macOS. |
| `version-missing` | error | The program doesn't contain the version string. |
| `version-run-mismatch` | error | Running the program failed, or what it printed doesn't contain the version. |
| `checksum-mismatch` | error | A checksum doesn't match its file. |
| `checksum-missing-entry` | error | A release file has no checksum. Signature files (`.sig`, `.asc`, `.pem`, sigstore bundles) are exempt. |
| `checksum-stale-entry` | error | A checksum for a file that isn't in the release. |
| `checksum-format` | error | A line of the checksums file can't be read, or a hash has the wrong length. |
| `run-skipped` | note | The program wasn't run because it's for another platform. |
| `optional-missing` | note | An optional file isn't in this release. |
| `version-unknown` | note | No version was given, so programs weren't checked for one. |

**On the version check.** rmqa looks for the version string inside each
program. It won't accept `1.4.2` inside `11.4.2` or `1.4.2.1`, but it will
accept it when a digit follows, because compilers pack strings end to end
with nothing between them. So a build of `1.4.20` would pass a check for
`1.4.2`. Running the program with `run` closes that gap on the platforms
your machine can run.

## Use it as a Go library

```go
m, err := rmqa.LoadManifest("rmqa.toml")
rel, err := rmqa.OpenDir("dist")
res, err := rmqa.Check(m, rel, rmqa.Options{Version: "1.4.2"})
if !res.Ready(false) {
	for _, f := range res.Findings {
		fmt.Println(f.File, f.Path, f.Code, f.Message)
	}
}
```

## How it was tested

The tests build a small Go program for five platforms and pack it the way
real projects do. A good release must pass with no errors and no warnings.
Then the tests break it in 24 different ways, the ones that happen in real
life, and each must be caught with the right code. Random combinations of
up to four problems at once must all be reported together, so one problem
never hides another. Fuzzers feed damaged manifests and archives to the
parsers. rmqa also checks a real published release, ukue 0.1.0, downloaded
from GitHub.

The recorded results are in [test/results](test/results). To record them
again: `scripts/record-results.sh`.

## License

Apache License 2.0. See [LICENSE](LICENSE), [NOTICE](NOTICE) and
[THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).
