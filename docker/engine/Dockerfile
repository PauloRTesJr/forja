# syntax=docker/dockerfile:1.7
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

FROM ubuntu:22.04 AS base-cpu

# Families compile CUDA extensions against torch when they install (custom_rasterizer, diso),
# which needs nvcc: the devel image, not the runtime one.
FROM nvidia/cuda:12.4.1-devel-ubuntu22.04 AS base-cu124

FROM ghcr.io/astral-sh/uv:0.12.22 AS uv

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
