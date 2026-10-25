"""The engine on a serverless GPU: behind RunPod's job queue, or behind a gateway on Modal.

A worker starts one engine on loopback and drives it through the /v1 API, exactly as a core
does, so serverless runs share the engine's validation, scheduling and store. Jobs arrive as
JSON (`op`: describe or run) and answer with the engine's own run and model descriptions.
"""

from forja_engine.serverless.worker import EngineHTTP, Job, Worker, models_from_env, start_engine

__all__ = ["EngineHTTP", "Job", "Worker", "models_from_env", "start_engine"]
