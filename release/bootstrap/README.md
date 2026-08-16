# Bootstrap release procedure

This directory documents the manual first materialization of an anoikis-tools release,
performed locally (no push, no publish) so a plugin-foundation download-script and the runtime
post-merge-compile edge can be exercised against real per-OS/arch archives before this
task's own work lands and `release.yml` starts cutting releases from a real tag push.

Unlike a CLI whose own release orchestration runs through itself, anoikis-tools has no
chicken-and-egg constraint — `go build` never needs anoikis-tools to build anoikis-tools.
This bootstrap exists only because the current working tree is not yet committed, so no
commit exists yet for a release tag to name.

## Procedure

1. **Ready check** — gofmt, go vet, go build, go test, the distribution guard:
   ```sh
   gofmt -l .
   go vet ./...
   go build ./...
   go test ./...
   bash release/guard/no-committed-binaries.sh .
   ```

2. **Version conforms to SC-VERSIONING** — bare `[v]X.Y.Z`, no path prefix (this repo's one
   Go module lives at the repo root):
   ```sh
   bash release/guard/tag-prefix.sh v0.1.0
   ```

3. **Cross-compile per-OS/arch archives + checksums** — the same recipe `release.yml` runs,
   run by hand against the working tree:
   ```sh
   mkdir -p dist
   for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
     os="${target%/*}"; arch="${target#*/}"
     workdir="$(mktemp -d)"
     GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "${workdir}/anoikis" .
     tar -czf "dist/anoikis_0.1.0_${os}_${arch}.tar.gz" -C "${workdir}" anoikis
     rm -rf "${workdir}"
   done
   ( cd dist && sha256sum ./*.tar.gz | sed 's#\./##' | sort -k2 > checksums.txt )
   ```

4. **Verify** — checksums match, an archive extracts to a runnable binary:
   ```sh
   ( cd dist && sha256sum -c checksums.txt )
   tar -xzf dist/anoikis_0.1.0_linux_amd64.tar.gz -O anoikis > /dev/null  # sanity: extracts cleanly
   ```

5. **Exercise a plugin-foundation provisioner against the local archives** (`file://`, no
   network). anoikis-tools carries no plugin of its own, so this borrows ai-shared-lib's
   shared `download-script.sh`, which resolves `PF_RELEASE_BASE_URL/v<version>/<archive>`;
   stage the `dist/` output under a `v<version>/` subdir of a scratch mirror first:
   ```sh
   mirror="$(mktemp -d)/mirror"; mkdir -p "$mirror/v0.1.0"
   cp dist/*.tar.gz dist/checksums.txt "$mirror/v0.1.0/"
   PF_CLI_NAME=anoikis PF_PLUGIN_DATA="$(mktemp -d)" PF_VERSION=0.1.0 \
   PF_ARCH_OVERRIDE=linux/amd64 PF_RELEASE_BASE_URL="file://$mirror" \
     sh ../ai-shared-lib/plugin-foundation/download-script.sh
   ```

## What this bootstrap does not do

- **No git tag.** The tag (`git tag -a v0.1.0 -m "anoikis-tools v0.1.0: initial release"`)
  names this task's own commit once it lands, created by whoever performs that commit —
  mirroring language-tools' own bootstrap, whose tag names the commit it was cut from.
- **No push, no `gh release create`, no GitHub Release.** `release.yml`'s
  `workflow_dispatch` dry-run path (or a real tag push, once one exists) performs that
  half; this bootstrap only proves the recipe and the archives it produces are correct.

## Artifacts

- `dist/anoikis_0.1.0_<os>_<arch>.tar.gz` — one archive per target, single executable
  named `anoikis` inside, matching `release.yml`'s own build step and the filename shape
  ai-shared-lib's `plugin-foundation/download-script.sh` parses.
- `dist/checksums.txt` — `sha256sum`-style manifest, one line per archive.

Both are git-ignored build output (SC-DISTRIBUTION: no built binary is ever committed);
`release/bootstrap/README.md` (this file) is the only tracked artifact of this procedure.
