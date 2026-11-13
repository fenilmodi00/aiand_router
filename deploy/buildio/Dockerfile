# Template: the real `deploy` branch Dockerfile is generated.
#
# This file documents the shape of the thin Dockerfile that
# `scripts/deploy/update_buildio_deploy_branch.sh` writes to the generated
# `deploy` branch: a single literal, digest-pinned `FROM` — no `ARG`, no
# variable indirection — so build.io's kaniko build is an unambiguous pull
# of one immutable image. The digest below is a placeholder; the real
# branch always pins the exact digest of the image that GitHub Actions
# just pushed. See docs/DEPLOY_BUILDIO.md ("Build once, deploy a prebuilt
# image").
FROM ghcr.io/fenilmodi00/aiand_router@sha256:0000000000000000000000000000000000000000000000000000000000000000
