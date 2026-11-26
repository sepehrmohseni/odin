---
name: release
description: Prepare or cut an Odin release (binaries, multi-arch image, changelog). Use when asked to release, tag a version, or check that the release pipeline works.
---

# Release Odin

Tagging `vX.Y.Z` runs `.github/workflows/release.yml`:
- GoReleaser (`.goreleaser.yaml`) builds `odin` and `odin-migrate` for linux and darwin on amd64
  and arm64, with archives, checksums, SBOMs and a changelog.
- The `image` job pushes a multi-arch image to `ghcr.io/<owner>/odin` with SBOM and provenance,
  signed with keyless cosign.

1. **Ask first.** The first release tag and the first public image need the owner's explicit
   go-ahead. So does any later tag unless they asked for it.
2. Gate: `scripts/docker-check.sh` green, and CI green on `main`.
3. Dry run, without publishing:
   ```bash
   docker run --rm -v "$PWD:/repo:ro" -w /tmp --entrypoint sh goreleaser/goreleaser:v2.20.3 -c \
     'git config --global --add safe.directory "*"; git clone -q /repo rel && cd rel &&
      git remote set-url origin https://github.com/sepehrmohseni/odin.git &&
      goreleaser check && goreleaser release --snapshot --clean --skip=sbom,publish && ls dist'
   docker buildx build --platform linux/amd64,linux/arm64 .
   ```
4. Move the `Unreleased` section of `CHANGELOG.md` under the new version and date, with a
   "Changed (check these when upgrading)" list; bump `appVersion` (and the chart `version`) in
   `deployments/helm/odin/Chart.yaml` and the versions in `docs/deployment.md`; commit
   `docs: changelog for vX.Y.Z`; push, and wait for CI on that commit.
5. Once approved: `git tag -a vX.Y.Z -m "vX.Y.Z" && git push origin vX.Y.Z`. Watch the Release
   workflow, then check the release page and `cosign verify ghcr.io/<owner>/odin:X.Y.Z
   --certificate-identity-regexp 'github.com/<owner>/odin' --certificate-oidc-issuer
   https://token.actions.githubusercontent.com`.
6. Afterwards: `curl -sI https://github.com/<owner>/odin/releases/latest` redirects to the new
   tag, the assets are listed, and `docker run --rm ghcr.io/<owner>/odin:X.Y.Z -version` prints it.
