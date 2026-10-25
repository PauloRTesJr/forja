"""The adapter SDK, as the engine and its families import it.

The contract lives in the `forja-engine-sdk` distribution (`forja_sdk`, in engine/sdk/), so
third-party adapters can depend on it without the engine. These are the same objects, not
copies: `isinstance` checks and `except Cancelled` hold across both import paths.
"""

from forja_sdk import (
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
