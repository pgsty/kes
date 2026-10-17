# Maintaining PGSTY KES

The build, signing and publication procedure is maintained in
[RELEASING.md](RELEASING.md). Use the reviewed workflows described there.

Release tags use `RELEASE.YYYY-MM-DDTHH-MM-SSZ` in UTC. The workflows build a
Draft release from a signed tag and require separate approval to publish
artifacts or container images.

Product wording and links identify PGSTY KES. Preserve upstream copyright
headers, LICENSE, CREDITS and the attribution in NOTICE. Keep compatibility
identifiers documented in [README.md](README.md#compatibility-and-attribution).
Run `buildscripts/verify-branding.sh` when changing these surfaces.
