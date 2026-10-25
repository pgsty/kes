# Building and releasing PGSTY KES

The pipeline follows `pgsty/mc`: validate on pull requests, build a Draft
GitHub Release from a signed tag, sign RPMs on a maintainer machine, review
the Draft, and publish Docker Hub images separately. Pushing a branch never
publishes a release or an image.

## Continuous integration

All four workflows run on pull requests and pushes to `master`:

- **Go**: branding and attribution checks, build, vet, lint, and unit tests
  on Linux, macOS and Windows.
- **VulnCheck**: reachable Go vulnerabilities; also runs weekly.
- **KMS Compatibility**: OpenBao KV v2 and released SILO 20261018, 20260916
  and 20260903 servers, including reading objects written through the last
  PGSTY KES release after an upgrade. The baseline archive and checksum manifest
  are verified against their GitHub build attestations before use.
  A separate Moto Secrets Manager job checks AWS SDK v1/v2 upgrades and rollback.
  Both fixtures restart SILO between KES versions so cached keys cannot hide
  backend failures. Moto is local protocol coverage, not live AWS certification.
  Upgrade from the last upstream KES release remains a separate best-effort
  advisory job; only the maintained PGSTY stack gates this release.
- **Test Release Pipeline**: six cross-platform archives, six Linux packages,
  metadata and binary digest checks, provenance stamps, release guard tests,
  and a container build/run without pushing.

The release gate requires successful push runs for the exact tip of `master`.
A PR merge-ref run or a manual dispatch does not satisfy that gate.

To run the AWS fixture locally with Docker (Linux host networking or macOS
forwarded ports):

```sh
KEYSTORE=aws KES_BASELINE=/absolute/path/to/kes-with-sdk-v1 ./testing/kms-compat.sh
```

The fixture creates a disposable Moto container with test credentials and never
uses a real AWS account. It checks old and newly created encryption keys across
upgrade, rollback and restart, then runs `TestAWS` against that same store and
verifies that test cleanup preserved both keys. The CI pins the SDK v1 baseline
commit; `KES_BASELINE` must point to a binary built before the SDK migration.

## Local build rehearsal

Use the Go toolchain in `go.mod`, GoReleaser 2.18.3 and nFPM 2.48.0. Package
verification in the workflow additionally uses `rpm`, `dpkg-deb` and binutils
on Linux. The provenance check requires a committed, clean checkout.

```sh
go test ./...
go vet ./...
buildscripts/verify-branding.sh
export RELEASE_TAG=RELEASE.2026-02-14T12-00-00Z
export PKG_VERSION=20260214120000.0.0
export LDFLAGS="$(go run buildscripts/gen-ldflags.go 2026-02-14T12:00:00Z)"
goreleaser check --config .github/goreleaser.yml
goreleaser release --snapshot --clean --skip=publish --config .github/goreleaser.yml
buildscripts/verify-build-provenance.sh
buildscripts/package-release.sh
```

The date above is a fixture for local testing, not a release to publish.
Linux and macOS archives use `kes_<package-version>_<os>_<arch>.tar.gz`;
Windows archives use `kes_<package-version>_windows_<arch>.zip` for native
PowerShell `Expand-Archive` support. This applies to releases built from this
configuration; the published 20261018 Windows archives remain `.tar.gz`.
The binary is `kes`
(`kes.exe` on Windows). Linux packages install `/usr/local/bin/kes`; service
configuration, certificates and a persistent keystore must be provisioned
separately. Installing the package does not start an unconfigured service.

Package and image metadata uses `AGPL-3.0-only`, matching the upstream source
headers that specify AGPLv3. LICENSE and all inherited copyright and license
notices remain unchanged; this fork does not infer a grant for later license
versions from the example notice at the end of LICENSE.

## Repository settings

Configure these before the first real release:

| Type | Name | Purpose |
| --- | --- | --- |
| Variable | `RELEASE_SIGNING_FINGERPRINT` | Expected primary OpenPGP key fingerprint for release tags |
| Variable | `RELEASE_SIGNING_KEY_URL` | HTTPS URL of the corresponding public key |
| Secret | `DOCKERHUB_USERNAME` | Account allowed to publish `pgsty/kes` |
| Secret | `DOCKERHUB_TOKEN` | Docker Hub access token for that account |

GitHub provides `GITHUB_TOKEN` and OIDC automatically. No signing private key
is stored in GitHub Actions. RPM signing uses the same local `dnfupdate`
container and Pigsty signing key as mcli; inspect
`buildscripts/sign-release-rpms.sh` before using it. Set `SIGN_WORKDIR` to a
private directory outside the checkout when retaining signing evidence.

## Prepare and publish a release

1. Merge the reviewed change into `master` and wait for all four workflows.
2. Choose a UTC `RELEASE.YYYY-MM-DDTHH-MM-SSZ` tag for that exact commit.
   Create an annotated, signed tag with the configured signing key, then push
   it only when release preparation is authorized.
3. The **Release** workflow verifies tag signature, source commit and green CI,
   then creates a **Draft**. GoReleaser builds archives; nFPM packages the same
   Linux binaries. GitHub attestations cover archives, their checksum manifest,
   DEBs and APKs. RPMs are excluded because signing changes their bytes.
4. Run `buildscripts/sign-release-rpms.sh TAG` to download, validate and sign
   RPMs locally. Review the result; `--upload` replaces the RPMs and their
   checksums in the Draft. The script refuses an already published release.
5. Review the actual artifacts, signatures and integration results. Publish
   the GitHub Draft only after explicit release approval. CI never does this.
6. With separate image publication approval, dispatch **Publish Docker Image**
   with the published tag. It accepts only the latest stable GitHub release,
   re-verifies the signed tag and archive attestations, checks ELF architecture,
   builds and runs both Linux images, then promotes Docker Hub manifests.

An absent release or one unfinalized Draft can be rebuilt by rerunning its tag
workflow. A rebuild replaces the Draft, so repeat RPM signing and review after
any rerun. Published releases are immutable to the build workflow. Do not
publish a Draft while its build or RPM upload is running.

The Go module path remains `github.com/minio/kes` for source compatibility;
build this checkout rather than installing the upstream module by mistake.
The release version is injected from the chosen tag, while the commit ID and
clean-tree stamp come from Go's VCS metadata. `kes update` intentionally exits
without downloading anything; use verified PGSTY artifacts or package/image
updates instead.
