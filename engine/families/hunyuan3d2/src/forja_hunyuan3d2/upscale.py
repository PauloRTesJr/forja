"""Real-ESRGAN x4plus (BSD-3-Clause) for Hunyuan3D-2 Paint's views.

Upstream's multiview model draws six views at 512 px and its paint pipeline only resizes them to
the render size, so a 2K texture carries 512 px of detail per view. The network here is written
from the published architecture, with the checkpoint's parameter names, instead of depending on
`realesrgan` and `basicsr` (which pull in a pinned, hard-to-build set of packages).

torch is imported inside the functions: the engine's own environment has none.
"""

from __future__ import annotations

import logging
from collections.abc import Sequence
from functools import cache
from pathlib import Path
from typing import Any

import numpy as np
from PIL import Image

log = logging.getLogger("forja.hunyuan3d2")

SCALE = 4
# Where install puts the file, inside the model's weights folder (manifest: weights.extra).
WEIGHTS = Path("realesrgan") / "RealESRGAN_x4plus.pth"
NUM_BLOCKS = 23
NUM_FEAT = 64
NUM_GROW = 32


class Unavailable(RuntimeError):
    """The upscaler cannot run: its file is missing or is not the checkpoint expected."""


@cache
def _rrdbnet_class() -> Any:
    import torch
    from torch import nn
    from torch.nn import functional as F

    class ResidualDenseBlock(nn.Module):
        def __init__(self) -> None:
            super().__init__()
            for i in range(1, 5):
                setattr(self, f"conv{i}", nn.Conv2d(NUM_FEAT + (i - 1) * NUM_GROW, NUM_GROW, 3, 1, 1))
            self.conv5 = nn.Conv2d(NUM_FEAT + 4 * NUM_GROW, NUM_FEAT, 3, 1, 1)

        def forward(self, x: Any) -> Any:
            feats = [x]
            for i in range(1, 5):
                conv = getattr(self, f"conv{i}")
                feats.append(F.leaky_relu(conv(torch.cat(feats, 1)), 0.2))
            return self.conv5(torch.cat(feats, 1)) * 0.2 + x

    class RRDB(nn.Module):
        def __init__(self) -> None:
            super().__init__()
            self.rdb1, self.rdb2, self.rdb3 = (ResidualDenseBlock() for _ in range(3))

        def forward(self, x: Any) -> Any:
            return self.rdb3(self.rdb2(self.rdb1(x))) * 0.2 + x

    class RRDBNet(nn.Module):
        def __init__(self) -> None:
            super().__init__()
            self.conv_first = nn.Conv2d(3, NUM_FEAT, 3, 1, 1)
            self.body = nn.Sequential(*(RRDB() for _ in range(NUM_BLOCKS)))
            self.conv_body = nn.Conv2d(NUM_FEAT, NUM_FEAT, 3, 1, 1)
            self.conv_up1 = nn.Conv2d(NUM_FEAT, NUM_FEAT, 3, 1, 1)
            self.conv_up2 = nn.Conv2d(NUM_FEAT, NUM_FEAT, 3, 1, 1)
            self.conv_hr = nn.Conv2d(NUM_FEAT, NUM_FEAT, 3, 1, 1)
            self.conv_last = nn.Conv2d(NUM_FEAT, 3, 3, 1, 1)

        def forward(self, x: Any) -> Any:
            feat = self.conv_first(x)
            feat = feat + self.conv_body(self.body(feat))
            for up in (self.conv_up1, self.conv_up2):
                feat = F.leaky_relu(up(F.interpolate(feat, scale_factor=2, mode="nearest")), 0.2)
            return self.conv_last(F.leaky_relu(self.conv_hr(feat), 0.2))

    return RRDBNet


def rrdbnet() -> Any:
    """A new RRDBNet with random weights: 3 channels in, the same out at 4x the size."""
    return _rrdbnet_class()()


def load_network(path: Path) -> Any:
    """The network with the checkpoint's weights, on the CPU in eval mode.

    The released file is a dict whose weights sit under `params_ema`. Raises Unavailable when the
    file is missing or its weights are not this network's.
    """
    import torch

    if not path.is_file():
        raise Unavailable(f"{path} is missing")
    try:
        ckpt = torch.load(path, map_location="cpu", weights_only=True)
        net = rrdbnet()
        net.load_state_dict(ckpt["params_ema"], strict=True)
    except Exception as e:  # a bad pickle, a missing key, a shape that does not match
        raise Unavailable(f"{path} is not a Real-ESRGAN x4plus checkpoint: {e}") from e
    return net.eval().requires_grad_(False)


class Upscaler:
    """Enlarges PIL images 4x. Holds the network on the CPU between passes."""

    def __init__(self, path: Path, device: str = "cuda") -> None:
        self.path = path
        self.device = device
        self.net: Any = None
        # Why it is unavailable, remembered with the file it was tried on so a repair that
        # fetches the file is picked up by the next job without restarting the engine.
        self._failed: tuple[tuple[int, int] | None, str] | None = None

    def _stamp(self) -> tuple[int, int] | None:
        try:
            st = self.path.stat()
        except OSError:
            return None
        return st.st_mtime_ns, st.st_size

    def ready(self) -> bool:
        if self.net is not None:
            return True
        stamp = self._stamp()
        if self._failed is not None and self._failed[0] == stamp:
            return False
        try:
            self.net = load_network(self.path)
        except Unavailable as e:
            log.warning("Hunyuan3D-2 Paint will not sharpen its views: %s", e)
            self._failed = (stamp, str(e))
            return False
        if self.device != "cpu":
            self.net.half()  # fp16 on the GPU; rounded once here, not on every pass
        self._failed = None
        return True

    def release(self) -> None:
        self.net = None

    def __call__(self, images: Sequence[Image.Image]) -> list[Image.Image]:
        """Each image 4x larger. The network is on the GPU only for the pass: the paint pipeline
        already fills a 12 GB card."""
        import torch

        if not self.ready():
            raise Unavailable(self._failed[1] if self._failed else "the upscaler is not loaded")
        net = self.net
        try:
            net.to(self.device)
            with torch.inference_mode():
                return [self._one(net, im) for im in images]
        finally:
            net.to("cpu")
            if self.device == "cuda":
                torch.cuda.empty_cache()

    def _one(self, net: Any, image: Image.Image) -> Image.Image:
        import torch

        rgb = np.array(image.convert("RGB"), dtype=np.uint8)  # a copy: torch wants a writable array
        x = torch.from_numpy(rgb).to(self.device).permute(2, 0, 1).unsqueeze(0)
        x = x.to(next(net.parameters()).dtype) / 255.0
        y = net(x).clamp_(0.0, 1.0).mul_(255.0).round_().to(torch.uint8)
        out = y[0].permute(1, 2, 0).cpu().numpy()
        return Image.fromarray(out, "RGB")


class SharpenedViews:
    """Stands in for upstream's Multiview_Diffusion_Net in the paint pipeline's `models`.

    Its call returns the diffusion model's views enlarged 4x (upstream resizes them to the render
    size right after). Everything else, `device` and `pipeline` for the offload hooks included,
    belongs to the wrapped object.
    """

    def __init__(self, net: Any, upscaler: Upscaler) -> None:
        object.__setattr__(self, "_net", net)
        object.__setattr__(self, "_upscaler", upscaler)
        object.__setattr__(self, "enabled", False)
        object.__setattr__(self, "failed", "")

    def __getattr__(self, name: str) -> Any:
        return getattr(self._net, name)

    def __setattr__(self, name: str, value: Any) -> None:
        if name in ("enabled", "failed"):
            object.__setattr__(self, name, value)
        else:
            setattr(self._net, name, value)

    def __call__(self, *args: Any, **kwargs: Any) -> list[Image.Image]:
        views: list[Image.Image] = self._net(*args, **kwargs)
        if not self.enabled:
            return views
        try:
            return self._upscaler(views)
        except (RuntimeError, MemoryError) as e:
            # An out-of-memory error here would throw away minutes of diffusion for a nicety.
            log.warning("Hunyuan3D-2 Paint kept its views unsharpened: %s", e)
            self.failed = str(e)
            return views
