"""Stand-ins for the flash-attn calls that rig models make, on PyTorch's own attention.

flash-attn publishes Linux wheels only, and compiling it on Windows takes hours when it works
at all. UniRig and SkinTokens import it in a few places: the `MHA` module (a projection
layout around plain attention), the packed variable-length kernel, and `flash_attn_func`.
These compute the same attention with `torch.nn.functional.scaled_dot_product_attention`,
which picks a fused kernel on CUDA by itself, so the families run where flash-attn is not
installed. `install` registers them only then; torch is imported on first use, so the engine
itself never needs it.
"""

from __future__ import annotations

import importlib.machinery
import importlib.util
import itertools
import sys
import types
from typing import Any

NAME = "flash_attn"
VERSION = "0+forja.sdpa"


def standing_in() -> bool:
    """True when `flash_attn` is these stand-ins, so callers ask transformers for "sdpa"."""
    return getattr(sys.modules.get(NAME), "__version__", "") == VERSION


def install() -> bool:
    """Registers the stand-ins as `flash_attn` unless the real package is there; says if it did."""
    if NAME in sys.modules or importlib.util.find_spec(NAME) is not None:
        return False
    root = _package(NAME)
    root.__version__ = VERSION  # type: ignore[attr-defined]
    modules = _package(f"{NAME}.modules")
    mha = _module(f"{NAME}.modules.mha")
    mha.__getattr__ = _mha_attr  # type: ignore[method-assign]  # built on first use: needs torch
    interface = _module(f"{NAME}.flash_attn_interface")
    for m in (root, interface):
        m.flash_attn_func = flash_attn_func  # type: ignore[attr-defined]
        m.flash_attn_varlen_qkvpacked_func = flash_attn_varlen_qkvpacked_func  # type: ignore[attr-defined]
    root.modules, modules.mha = modules, mha  # type: ignore[attr-defined]
    root.flash_attn_interface = interface  # type: ignore[attr-defined]
    for m in (root, modules, mha, interface):
        sys.modules[m.__name__] = m
    return True


def _package(name: str) -> types.ModuleType:
    m = types.ModuleType(name)
    # transformers asks importlib for a spec; a module without one makes it raise.
    m.__spec__ = importlib.machinery.ModuleSpec(name, None, is_package=True)
    m.__path__ = []
    return m


def _module(name: str) -> types.ModuleType:
    m = types.ModuleType(name)
    m.__spec__ = importlib.machinery.ModuleSpec(name, None)
    return m


def _sdpa(q: Any, k: Any, v: Any, scale: float | None, causal: bool, mask: Any = None) -> Any:
    """(batch, seq, heads, dim) in and out, as flash-attn lays them out."""
    import torch.nn.functional as F

    out = F.scaled_dot_product_attention(
        q.transpose(1, 2), k.transpose(1, 2), v.transpose(1, 2), attn_mask=mask, is_causal=causal, scale=scale
    )
    return out.transpose(1, 2)


def flash_attn_func(
    q: Any,
    k: Any,
    v: Any,
    dropout_p: float = 0.0,
    softmax_scale: float | None = None,
    causal: bool = False,
    **_: Any,
) -> Any:
    return _sdpa(q, k, v, softmax_scale, causal)


def flash_attn_varlen_qkvpacked_func(
    qkv: Any,
    cu_seqlens: Any,
    max_seqlen: int,
    dropout_p: float = 0.0,
    softmax_scale: float | None = None,
    causal: bool = False,
    **_: Any,
) -> Any:
    """qkv is (total, 3, heads, dim): sequences one after another, cut at cu_seqlens."""
    import torch

    bounds = cu_seqlens.tolist()
    lengths = {b - a for a, b in itertools.pairwise(bounds)}
    total, _, heads, dim = qkv.shape
    if len(lengths) == 1:
        # Point patches all have the same length: one batched call.
        (n,) = lengths
        q, k, v = qkv.reshape(-1, n, 3, heads, dim).unbind(2)
        return _sdpa(q, k, v, softmax_scale, causal).reshape(total, heads, dim)
    out = []
    for a, b in itertools.pairwise(bounds):
        q, k, v = qkv[a:b].unsqueeze(0).unbind(2)
        out.append(_sdpa(q, k, v, softmax_scale, causal)[0])
    return torch.cat(out)


_MHA: Any = None


def _mha_attr(name: str) -> Any:
    if name != "MHA":
        raise AttributeError(name)
    global _MHA
    if _MHA is None:
        _MHA = _build_mha()
    return _MHA


def _build_mha() -> Any:
    import torch
    from torch import nn

    class MHA(nn.Module):  # type: ignore[misc]
        """flash_attn.modules.mha.MHA's parameters (Wqkv, or Wq and Wkv; out_proj), so its
        checkpoints load as they are."""

        def __init__(
            self,
            embed_dim: int,
            num_heads: int,
            cross_attn: bool = False,
            qkv_proj_bias: bool = True,
            out_proj_bias: bool = True,
            softmax_scale: float | None = None,
            causal: bool = False,
            device: Any = None,
            dtype: Any = None,
            **_: Any,
        ) -> None:
            super().__init__()
            kw = {"device": device, "dtype": dtype}
            self.num_heads, self.head_dim = num_heads, embed_dim // num_heads
            self.cross_attn, self.causal, self.softmax_scale = cross_attn, causal, softmax_scale
            if cross_attn:
                self.Wq = nn.Linear(embed_dim, embed_dim, bias=qkv_proj_bias, **kw)
                self.Wkv = nn.Linear(embed_dim, 2 * embed_dim, bias=qkv_proj_bias, **kw)
            else:
                self.Wqkv = nn.Linear(embed_dim, 3 * embed_dim, bias=qkv_proj_bias, **kw)
            self.out_proj = nn.Linear(embed_dim, embed_dim, bias=out_proj_bias, **kw)

        def forward(self, x: torch.Tensor, x_kv: Any = None, key_padding_mask: Any = None, **_: Any) -> Any:
            b, s, _ = x.shape
            h, d = self.num_heads, self.head_dim
            if self.cross_attn:
                q = self.Wq(x).view(b, s, h, d)
                k, v = self.Wkv(x if x_kv is None else x_kv).view(b, -1, 2, h, d).unbind(2)
            else:
                q, k, v = self.Wqkv(x).view(b, s, 3, h, d).unbind(2)
            mask = None if key_padding_mask is None else key_padding_mask[:, None, None, :]
            out = _sdpa(q, k, v, self.softmax_scale, self.causal, mask)
            return self.out_proj(out.reshape(b, s, h * d))

    return MHA
