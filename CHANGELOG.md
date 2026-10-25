# Changelog

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
