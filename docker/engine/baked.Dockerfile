# syntax=docker/dockerfile:1.7
#
# A "baked" engine image: one model's environment and weights inside, for serverless cold
# starts that download nothing. Larger than the plain image; build it only for the models an
# endpoint serves:
#
#   docker build -f docker/engine/baked.Dockerfile --build-arg BASE=forja-engine:0.1.0-cu124 \
#     --build-arg MODELS=triposg -t forja-engine:0.1.0-cu124-triposg .
#
# The build has no GPU, so the lane is given and CUDA extensions compile for the architectures
# in TORCH_CUDA_ARCH_LIST (Ampere to Hopper by default). Licenses still apply: an image with a
# restricted model's weights must not be shared where its license does not allow.

ARG BASE=forja-engine:0.1.0-cu124
FROM ${BASE}
ARG MODELS
ARG LANE=cuda
ARG TORCH_CUDA_ARCH_LIST="8.0;8.6;8.9;9.0"
ENV TORCH_CUDA_ARCH_LIST=${TORCH_CUDA_ARCH_LIST}
RUN test -n "${MODELS}" || (echo "give --build-arg MODELS=<model>[,<model>]" >&2 && exit 1)
RUN --mount=type=cache,target=/root/.cache/uv UV_CACHE_DIR=/root/.cache/uv \
    forja-engine install $(echo "${MODELS}" | tr ',' ' ') --lane "${LANE}" \
      --store /opt/forja/baked/store --envs /opt/forja/baked/envs --models /opt/forja/baked/models \
 && rm -rf /opt/forja/baked/store
# Serve (entrypoint.sh) and serverless workers (start_engine) both pass these to the engine.
ENV FORJA_ENGINE_ARGS="--envs /opt/forja/baked/envs --models /opt/forja/baked/models"
