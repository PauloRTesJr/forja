# forja-engine-sdk

The contract between Forja and a model adapter: stage types, artifact kinds, the `Adapter` base class, and a conformance suite that checks an adapter keeps the contract. It needs only the Python standard library.

Forja's engine imports the same objects as `forja_engine.sdk`; an adapter can import either.

## An adapter in brief

```python
from forja_sdk import Adapter, ArtifactKind, Capabilities, Device, StageContext, StageOutput, StageType


class MyShape(Adapter):
    # Bump when outputs change for the same inputs; the manifest's adapter_version mirrors it.
    version = "1"

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.SHAPE,), devices=(Device.CUDA,), vram_bytes=8 << 30)

    def load(self, device: Device, dtype: str = "fp16") -> None:
        self.pipeline = load_pipeline(self.weights_dir, device)  # once; the worker keeps it warm

    def run(self, ctx: StageContext) -> list[StageOutput]:
        image = ctx.input(ArtifactKind.IMAGE_PNG).path
        steps = 30
        for step in range(steps):
            self.pipeline.step(image, seed=ctx.seed)
            ctx.progress(step + 1, steps)
            ctx.check_cancel()
        out = self.pipeline.save(ctx.out_dir / "shape.glb")
        return [StageOutput(out, ArtifactKind.MESH_GLB, preview=True)]
```

## Conformance

```sh
forja-sdk test my_package:MyShape                 # sample inputs, on the CPU when the adapter runs there
forja-sdk test manifests/my-model.yaml --device cuda --weights path/to/weights
```

The suite loads the adapter, runs every stage type it serves on sample inputs, and checks output files, progress, determinism for a fixed seed, cancellation, the VRAM estimate (against PyTorch's measured peak on a GPU), and unload and reload. Given a manifest, it also checks the manifest against the adapter. It exits 1 when a check fails. From pytest:

```python
from forja_sdk.conformance import assert_conforms


def test_conforms():
    assert_conforms(MyShape)
```

The full guide is in Forja's repository: `docs/contributing/adapter-sdk.md`.

## License

Apache-2.0.
