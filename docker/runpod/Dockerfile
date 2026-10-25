# syntax=docker/dockerfile:1.7
#
# The engine as a RunPod serverless worker. Build it on an engine image (plain or baked):
#
#   docker build -f docker/runpod/Dockerfile --build-arg BASE=forja-engine:0.1.0-cu124 \
#     -t forja-engine:0.1.0-cu124-runpod .
#
# Push it to a registry you own and create a serverless endpoint from it, with a network volume
# attached: envs, weights and artifacts live there (/runpod-volume/forja) and outlive workers.
# Endpoint environment variables:
#
#   FORJA_MODELS       models each worker needs, comma separated; installed on the volume once
#   FORJA_ENGINE_ARGS  more engine flags, such as --fit-mode quality-first on a 24 GB+ GPU
#
# Then add it in Forja (Settings, or `forja providers add runpod`) with the endpoint id and
# your RunPod API key. Give Forja the volume's S3 API key too, and files move through the
# volume by hash instead of riding in the jobs (RunPod caps a job's payload at 10 MB).
#
# Locally, RunPod's SDK serves the same job API for a try without an account:
#   docker run --rm -p 8000:8000 forja-engine:0.1.0-cpu-runpod --rp_serve_api --rp_api_host 0.0.0.0

ARG BASE=forja-engine:0.1.0-cu124
FROM ${BASE}
# RunPod's SDK and what it needs, pinned with hashes against the engine's lock (requirements.in).
COPY docker/runpod/requirements.txt /opt/forja/runpod-requirements.txt
RUN --mount=type=cache,target=/root/.cache/uv UV_CACHE_DIR=/root/.cache/uv \
    uv pip install --python /opt/forja/engine/.venv/bin/python --require-hashes -r /opt/forja/runpod-requirements.txt
ENTRYPOINT ["python", "-m", "forja_engine.serverless.runpod"]
CMD []
