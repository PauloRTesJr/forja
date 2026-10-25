# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e
#
# forja-engine, one image per CUDA line, with no weights inside. Build from the repository root:
#
#   docker build -f docker/engine/Dockerfile --build-arg VERSION=0.1.0 --build-arg CUDA=cu124 \
#     -t forja-engine:0.1.0-cu124 .
#
# CUDA is a torch lane from the families' `torch_backend` pins (cu124 today), or `cpu` for an
# image that runs only the stub models and the built-in stages.
#
# Family environments, weights and artifacts live on the volume at /data (FORJA_DATA_DIR), so
# the image stays small and a volume keeps what a first run downloads. The default command
# serves /v1 to cores on other machines, which off loopback needs FORJA_ENGINE_TOKEN and TLS
# (see entrypoint.sh). The RunPod image and the Modal app start from this one.

ARG CUDA=cu124

# Base images are pinned by digest (the multi-arch index), so a retagged image cannot change
# what is built; the tag beside each says what it is. To move one, take the new tag's digest
# from `docker buildx imagetools inspect <image:tag>`.

# ubuntu:22.04
FROM ubuntu@sha256:0a9887931b0e38af2e3487ed39e29436b0535c4f3f52e57745baaf79d1898956 AS base-cpu

# Families compile CUDA extensions against torch when they install (custom_rasterizer, diso),
# which needs nvcc: the devel image, not the runtime one.
# nvidia/cuda:12.4.1-devel-ubuntu22.04
FROM nvidia/cuda@sha256:da6791294b0b04d7e65d87b7451d6f2390b4d36225ab0701ee7dfec5769829f5 AS base-cu124

# ghcr.io/astral-sh/uv:0.12.22
FROM ghcr.io/astral-sh/uv@sha256:f513a91fc62fe7c17567eee97230dd198e43edb8a9fbecca843714a4358fe1bc AS uv

FROM base-${CUDA}
ARG VERSION=0.0.0-dev
ARG CUDA
LABEL org.opencontainers.image.title="forja-engine" \
      org.opencontainers.image.version="${VERSION}-${CUDA}" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.description="Forja inference engine (no weights inside)"

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates openssl build-essential libgl1 libglib2.0-0 \
 && rm -rf /var/lib/apt/lists/*

COPY --from=uv /uv /uvx /usr/local/bin/
# Pythons for the engine (3.12) and the families (3.11) ship in the image, so installing a
# model downloads only its dependencies and weights; uv's cache stays on the volume.
ENV UV_PYTHON_INSTALL_DIR=/opt/uv/python \
    UV_LINK_MODE=copy \
    UV_COMPILE_BYTECODE=1 \
    UV_CACHE_DIR=/data/cache/uv \
    FORJA_DATA_DIR=/data \
    FORJA_FAMILIES_DIR=/opt/forja/engine/families \
    FORJA_IMAGE="forja-engine:${VERSION}-${CUDA}" \
    PATH=/opt/forja/engine/.venv/bin:$PATH
RUN --mount=type=cache,target=/root/.cache/uv UV_CACHE_DIR=/root/.cache/uv uv python install 3.12 3.11

WORKDIR /opt/forja/engine
COPY engine/pyproject.toml engine/uv.lock engine/.python-version ./
RUN --mount=type=cache,target=/root/.cache/uv UV_CACHE_DIR=/root/.cache/uv \
    uv sync --frozen --no-dev --no-install-project
COPY engine/src ./src
COPY engine/families ./families
# pip too: platforms that add their own client to an image (Modal) install it with pip.
RUN --mount=type=cache,target=/root/.cache/uv UV_CACHE_DIR=/root/.cache/uv uv sync --frozen --no-dev \
 && python -m ensurepip --default-pip >/dev/null

COPY docker/engine/entrypoint.sh /usr/local/bin/forja-engine-entrypoint
RUN chmod 0755 /usr/local/bin/forja-engine-entrypoint

VOLUME /data
EXPOSE 7861
ENTRYPOINT ["forja-engine-entrypoint"]
