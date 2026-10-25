# Changelog

## 0.1.1 (2026-10-25)

Fixes from the independent review of 0.1.0.

- Archives are read as a stream: programs are kept in memory (at most
  512 MiB per archive) and everything else is only counted. A 1 MB archive
  that expands to 1 GiB used to take about 2.2 GB of memory; it now takes
  about 13 MB.
- A tar hard link is checked from the top of the archive, as tar resolves
  it, so a hard link to `../etc/passwd` inside a folder is now `unsafe-path`.
- An archive made with `tar -C dir .` around one top folder no longer fails
  with `archive-layout` because of its `./` entry.
- The file name stored inside a `.gz` file gets the `unsafe-path` check.
- A program run with `run` that leaves a child process holding its output
  can no longer keep rmqa waiting past the 15-second limit.
- With `--format github`, a problem that stops the check is also printed as
  an annotation.
- The GitHub Action uses the x86-64 build on Windows on ARM runners and says
  so plainly when the download fails. CI now runs the action on Linux (x86-64
  and ARM64), macOS and Windows runners.

## 0.1.0 (2026-10-25)

First release.

- `rmqa check` compares a release folder, or a release on GitHub, with a
  manifest: every download present, the right program for each operating
  system and processor inside each archive, the version inside the program,
  executable permissions, required files such as licenses, no secrets or
  clutter, no paths that escape the folder, and a checksums file that
  matches and covers everything.
- `rmqa init` writes the manifest from a release that is known to be good.
- `rmqa inspect` shows what is inside archives and programs.
- Text, JSON, GitHub Actions annotations and Markdown output. Exit status 0
  when the release is ready, 1 when it isn't, 2 when rmqa couldn't run.
- A GitHub Action (`uses: softdevstudio/rmqa@v0.1.0`).
- rmqa checks its own releases before they are published.
