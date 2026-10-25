#!/usr/bin/env bash
# Runs every test and measurement and records the output in test/results.
# Every number on rmqa.com comes from these files.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
res="$root/test/results"
mkdir -p "$res"
cd "$root"
fuzztime="${FUZZTIME:-60s}"

{
  echo "recorded: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "machine: $(uname -srm)"
  echo "cpus: $(getconf _NPROCESSORS_ONLN)"
  echo "go: $(go version)"
  echo "version: $(cat VERSION)"
} > "$res/environment.txt"

echo "== tests"
go test -count=1 -race -v -timeout 60m ./... 2>&1 | tee "$res/tests.txt" | grep -E "^(--- |ok|FAIL)" || true
grep -qE "^(--- FAIL|FAIL)" "$res/tests.txt" && { echo "::error::tests failed"; exit 1; }
echo "tests passed: $(grep -c '^--- PASS' "$res/tests.txt") top-level and sub-tests"

echo "== random combinations of problems"
RMQA_ITERATIONS="${RMQA_ITERATIONS:-300}" go test -count=1 -v -run '^TestCombinedProblems$' . 2>&1 | tee "$res/combinations.txt" | grep -E "combinations:|^(--- |ok|FAIL)"
grep -qE "^(--- FAIL|FAIL)" "$res/combinations.txt" && { echo "::error::a combination of problems was not fully reported"; exit 1; }

echo "== broken releases"
RMQA_RECORD_DIR="$res" go test -count=1 -run TestRecordScenarios . >/dev/null
cat "$res/broken-releases.txt"

echo "== fuzz ($fuzztime each)"
: > "$res/fuzz.txt"
for f in FuzzTOML FuzzOpenArchive; do
  echo "--- $f" >> "$res/fuzz.txt"
  go test -run '^$' -fuzz "^$f\$" -fuzztime "$fuzztime" . 2>&1 | grep -E "elapsed|PASS|FAIL|panic" | tail -3 >> "$res/fuzz.txt"
done
cat "$res/fuzz.txt"
grep -qE "FAIL|panic" "$res/fuzz.txt" && { echo "::error::fuzzing found a crash"; exit 1; }

echo "== a real release: ukue 0.1.0 from GitHub"
bin="$(mktemp -d)/rmqa"
go build -o "$bin" ./cmd/rmqa
rel="$(mktemp -d)"
for f in ukue_linux_amd64.tar.gz ukue_linux_arm64.tar.gz ukue_darwin_arm64.tar.gz ukue_darwin_amd64.tar.gz ukue_windows_amd64.zip SHA256SUMS; do
  curl -sSfL -o "$rel/$f" "https://github.com/ukue-queue/ukue/releases/download/v0.1.0/$f"
done
{
  echo "\$ rmqa check ukue-0.1.0 --manifest examples/ukue.toml --version 0.1.0"
  "$bin" check "$rel" --manifest examples/ukue.toml --version 0.1.0 | sed "s|$rel|ukue-0.1.0|"
  echo
  echo "\$ rmqa check ukue-0.1.0 --manifest examples/ukue.toml --version 0.1.1"
  "$bin" check "$rel" --manifest examples/ukue.toml --version 0.1.1 | sed "s|$rel|ukue-0.1.0|" || true
} > "$res/real-release.txt"
cat "$res/real-release.txt"

echo "== timing"
python3 - "$bin" "$rel" "$res/bench.json" <<'PY'
import json, os, subprocess, sys, time
bin, rel, out = sys.argv[1:]
total = sum(os.path.getsize(os.path.join(rel, f)) for f in os.listdir(rel))
runs = []
for _ in range(7):
    t = time.perf_counter()
    subprocess.run([bin, "check", rel, "--manifest", "examples/ukue.toml", "--version", "0.1.0", "--no-run"], check=True, capture_output=True)
    runs.append(time.perf_counter() - t)
runs.sort()
r = {"release": "ukue 0.1.0, 5 archives and SHA256SUMS", "download_bytes": total,
     "runs": len(runs), "median_seconds": round(runs[len(runs)//2], 3), "fastest_seconds": round(runs[0], 3)}
json.dump(r, open(out, "w"), indent=2)
print(json.dumps(r, indent=2))
PY
echo "== memory on hostile archives"
python3 - "$bin" "$res/memory.txt" <<'PY'
import io, os, resource, subprocess, sys, tarfile, tempfile
bin, out = sys.argv[1:]
d = tempfile.mkdtemp()

class Zeros(io.RawIOBase):
    def __init__(self, n): self.left = n
    def readable(self): return True
    def readinto(self, b):
        n = min(len(b), self.left)
        b[:n] = bytes(n)
        self.left -= n
        return n

def peak_mb(path):
    # Each measurement runs in its own child, so the peak is that run's.
    code = ("import resource,subprocess,sys;"
            "subprocess.run(sys.argv[1:],capture_output=True);"
            "print(resource.getrusage(resource.RUSAGE_CHILDREN).ru_maxrss//1024)")
    r = subprocess.run([sys.executable, "-c", code, bin, "inspect", path], capture_output=True, text=True, check=True)
    return int(r.stdout.strip())

lines = []
big = os.path.join(d, "one-big-file.tar.gz")
with tarfile.open(big, "w:gz") as t:
    ti = tarfile.TarInfo("pkg/big"); ti.size = 1 << 30
    t.addfile(ti, io.BufferedReader(Zeros(ti.size)))
lines.append("one file of 1 GiB of zeros: archive %d bytes, rmqa inspect peak memory %d MB" % (os.path.getsize(big), peak_mb(big)))
small = os.path.join(d, "many-small-files.tar.gz")
with tarfile.open(small, "w:gz") as t:
    for i in range(150000):
        ti = tarfile.TarInfo("f%d" % i); ti.size = 1
        t.addfile(ti, io.BytesIO(b"x"))
lines.append("150,000 files of 1 byte: archive %d bytes, rmqa inspect peak memory %d MB" % (os.path.getsize(small), peak_mb(small)))
open(out, "w").write("\n".join(lines) + "\n")
print("\n".join(lines))
PY
echo "recorded in $res"
