# rmqa: where the next session starts

rmqa (release manifest QA) checks a software release against a manifest
before it is published. Go, standard library only, one static binary.

- `toml.go` reads the TOML subset the manifest needs (no third-party parser).
- `manifest.go` turns it into platforms and artifacts and expands templates.
- `archive.go` opens tar.gz/tgz/tar.bz2/tar/zip/gz with size limits and
  zip-slip detection. `binary.go` identifies ELF, Mach-O and PE programs.
- `check.go` runs every check; `report.go` prints text, JSON, GitHub
  annotations and Markdown; `init.go` writes a manifest from a good release;
  `github.go` fetches a release through the GitHub API.
- `cmd/rmqa` is the CLI. `action.yml` is the GitHub Action.
- `rmqa.toml` is rmqa's own manifest: the release workflow checks every rmqa
  release with rmqa before publishing it.

Site repo: github.com/photozeitgeist/rmqa.com (Hugo, Launch template,
content and config values only; never touch the theme).

## Workflow (low maintenance)

- Pull before working: ChatGPT/Codex also works on this repo (see AGENTS.md).
- The owner does not publish releases. Raise the version in `VERSION` and
  `const Version` in `cmd/rmqa/main.go`, add a `CHANGELOG.md` section, push to
  main. When CI is green, `release.yml` builds Linux/macOS/Windows archives,
  smoke-tests them, checks the release with rmqa, tags `vX.Y.Z` and
  publishes. Then watch the run, check the release files and links, and run
  a downloaded binary. `scripts/check-version.sh` keeps the places in step.
- Every number on the site comes from `test/results/`, written by
  `scripts/record-results.sh`. Re-record after any change that could move a
  number, and update the site from the new files.
- An independent review agent looks at code and site before a release is
  called done.
- main is the default branch. No side branches.
- GitHub job logs can't be downloaded from these sessions, but annotations
  can. Scripts and workflows print `::error::` lines for that reason. Read
  the real error before any rerun; at most two fix-and-retry rounds on one
  failure, then stop and report what's known and the options.
- Don't push to main while a release run is going. GitHub refuses the job's
  token when it tags a commit that's no longer main's newest one ("refusing
  to allow a GitHub App to create or update workflow"), so the release
  must tag main's head. If a push races a release, the next CI run on main
  releases instead.
- Finding codes in `check.go` are a public interface: never rename one.
  Add new codes instead, and document them in README.md.

## State

0.1.0 (2026-10-25, released): check, init, inspect; text/json/github/markdown output;
GitHub Action; 24 broken-release scenarios in the tests; checks the real
ukue 0.1.0 release in the recorded results.

## Gaps and to-do

Known gaps, not planned yet:

- No `.tar.xz`, `.tar.zst`, `.deb`, `.rpm`, `.msi` or `.dmg` inspection
  (the standard library has no xz or zstd reader). They are checked for
  presence, size and checksums only.
- Signatures (cosign, GPG, minisign) are not verified, only exempted from
  the checksum coverage rule.
- No SBOM or provenance checks.
- Per-file checksum files (`x.tar.gz.sha256`) are treated as ordinary
  artifacts; only one-file-lists-all checksum files are verified.
- The version check accepts a digit right after the version (see README).
- Container images and package registries (npm, PyPI, crates.io) are out of
  scope for now.

## Next

To be decided and approved with the owner before it goes here.
