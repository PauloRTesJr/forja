"""Adapter SDK: the contract every model family implements.

An adapter declares which stage types it serves (`tasks`) and runs one stage at a time.
It never touches the network, the job graph or the cache: it gets input files, writes
output files, reports progress, and checks for cancellation between steps.

`forja-sdk test` (see `forja_sdk.conformance`) checks an adapter against this contract.
"""

from forja_sdk.types import (
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

__version__ = "0.1.0"

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
