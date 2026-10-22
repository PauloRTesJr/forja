"""BiRefNet background removal (MIT) through ONNX Runtime.

One model file runs on CUDA, CoreML or CPU. The ONNX export expects a 1024x1024 RGB tensor
with ImageNet normalization and returns logits; the last output is the finest prediction.
"""

from __future__ import annotations

from typing import Any

import numpy as np
from PIL import Image

from forja_engine.ops import image as img_ops
from forja_engine.sdk import (
    Adapter,
    ArtifactKind,
    Capabilities,
    Device,
    StageContext,
    StageOutput,
    StageType,
)

MEAN = np.array([0.485, 0.456, 0.406], dtype=np.float32)
STD = np.array([0.229, 0.224, 0.225], dtype=np.float32)
MODEL_FILE = "onnx/model.onnx"
GB = 1 << 30

_PROVIDERS = {
    Device.CUDA: ["CUDAExecutionProvider", "CPUExecutionProvider"],
    # Not CoreML: loading this graph hung in the Neural Engine compiler (ORT 1.31, macOS 26),
    # while the CPU provider runs a 1024x1024 pass in about 6 s on Apple Silicon.
    Device.MPS: ["CPUExecutionProvider"],
    Device.CPU: ["CPUExecutionProvider"],
}


def preprocess(img: Image.Image, size: int) -> np.ndarray:
    rgb = img.convert("RGB").resize((size, size), Image.Resampling.BILINEAR)
    x = (np.asarray(rgb, dtype=np.float32) / 255.0 - MEAN) / STD
    return np.ascontiguousarray(x.transpose(2, 0, 1)[None])


def to_alpha(logits: np.ndarray, width: int, height: int) -> Image.Image:
    mask = 1.0 / (1.0 + np.exp(-logits.reshape(logits.shape[-2:]).astype(np.float32)))
    alpha = Image.fromarray((mask * 255).astype(np.uint8), "L")
    return alpha.resize((width, height), Image.Resampling.BILINEAR)


class BiRefNet(Adapter):
    version = "1"

    def __init__(self) -> None:
        self.session: Any = None
        self.input_name = ""
        self.size = 1024

    def capabilities(self) -> Capabilities:
        return Capabilities(
            tasks=(StageType.BACKGROUND_REMOVAL,),
            devices=(Device.CUDA, Device.MPS, Device.CPU),
            vram_bytes=8 * GB,
        )

    def load(self, device: Device, dtype: str = "fp16") -> None:
        import onnxruntime as ort

        if device is Device.CUDA and hasattr(ort, "preload_dlls"):
            # Loads CUDA and cuDNN from the nvidia-* wheels installed by the cuda extras.
            ort.preload_dlls()
        if self.weights_dir is None:
            raise RuntimeError("birefnet: no weights folder; run forja models install")
        path = self.weights_dir / MODEL_FILE
        available = set(ort.get_available_providers())
        providers = [p for p in _PROVIDERS.get(device, _PROVIDERS[Device.CPU]) if p in available]
        opts = ort.SessionOptions()
        opts.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
        self.session = ort.InferenceSession(str(path), opts, providers=providers)
        inp = self.session.get_inputs()[0]
        self.input_name = inp.name
        side = inp.shape[-1]
        self.size = side if isinstance(side, int) else 1024

    def unload(self) -> None:
        self.session = None

    def run(self, ctx: StageContext) -> list[StageOutput]:
        img = img_ops.normalize(Image.open(ctx.input(ArtifactKind.IMAGE_PNG).path))
        out_path = ctx.out_dir / "rgba.png"
        if ctx.params.get("keep_alpha", True) and img_ops.has_real_alpha(img):
            img.save(out_path)  # the artist already cut it out
            return [StageOutput(out_path, ArtifactKind.IMAGE_PNG)]
        ctx.progress(1, 3)
        ctx.check_cancel()
        outputs = self.session.run(None, {self.input_name: preprocess(img, self.size)})
        ctx.progress(2, 3)
        alpha = to_alpha(outputs[-1], img.width, img.height)
        rgba = img.convert("RGB")
        rgba.putalpha(alpha)
        rgba.save(out_path)
        ctx.progress(3, 3)
        return [StageOutput(out_path, ArtifactKind.IMAGE_PNG)]
