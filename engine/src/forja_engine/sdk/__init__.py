"""Adapter SDK: the contract every model family implements.

An adapter declares which stage types it serves (`tasks`) and runs one stage at a time.
It never touches the network, the job graph or the cache: it gets input files, writes
output files, reports progress, and checks for cancellation between steps.
"""

from forja_engine.sdk.types import (
    VIEWS,
    Adapter,
    ArtifactKind,
    Cancelled,
    Capabilities,
    Device,
    StageContext,
    StageInput,
    StageOutput,
    StageType,
)

__all__ = [
    "VIEWS",
    "Adapter",
    "ArtifactKind",
    "Cancelled",
    "Capabilities",
    "Device",
    "StageContext",
    "StageInput",
    "StageOutput",
    "StageType",
]
