"""The adapter SDK as a package of its own, its artifact validators and its conformance suite."""

import json
import random
import struct
from pathlib import Path

import forja_sdk
import pytest
import trimesh
import yaml
from forja_sdk import (
    Adapter,
    ArtifactKind,
    Capabilities,
    Device,
    StageContext,
    StageOutput,
    StageType,
    artifacts,
    samples,
)
from forja_sdk.cli import main as sdk_main
from forja_sdk.conformance import assert_conforms, run_suite

import forja_engine.sdk as engine_sdk
from forja_engine import blender
from forja_engine.adapters.builtin import BUILTINS
from forja_engine.ops import mesh as mesh_ops
from forja_engine.worker import ctx_from_json

MANIFESTS = Path(engine_sdk.__file__).parents[1] / "manifests"


def test_the_engine_reexports_the_sdk_objects_themselves():
    for name in forja_sdk.__all__:
        assert getattr(engine_sdk, name) is getattr(forja_sdk, name), name
    ctx = ctx_from_json(
        {"stage_id": "s", "task": "shape", "inputs": [], "params": {}, "seed": 1, "out_dir": "."}
    )
    assert isinstance(ctx, forja_sdk.StageContext) and ctx.task is forja_sdk.StageType.SHAPE


@pytest.mark.parametrize("name", ["stub-background", "stub-shape", "stub-paint"])
def test_stub_models_conform(name, tmp_path):
    doc = yaml.safe_load((MANIFESTS / f"{name}.yaml").read_text())
    report = assert_conforms(doc["adapter"], manifest=doc, work_dir=tmp_path)
    assert report.result("manifest").status == "pass"


@pytest.mark.parametrize("task", list(BUILTINS), ids=str)
def test_builtin_stages_keep_the_contract_too(task, tmp_path):
    if getattr(BUILTINS[task], "blender", False) and blender.problem() is not None:
        pytest.skip("Blender 4.2+ is not installed")
    # QuadriFlow makes a different mesh on each run for the same seed (see Remesh), so neither a
    # second run nor one after a reload can match the first byte for byte.
    skip = ("determinism", "reload") if task == StageType.MESH_REMESH else ()
    assert_conforms(BUILTINS[task], work_dir=tmp_path, skip=skip)


# --- the suite catches broken adapters ----------------------------------------------------


class Base(Adapter):
    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.SHAPE,), devices=(Device.CPU,))

    def mesh(self, ctx: StageContext, name: str = "shape.glb") -> Path:
        return samples.box_glb(ctx.out_dir / name)


class Good(Base):
    def run(self, ctx):
        ctx.progress(1, 2)
        ctx.check_cancel()
        out = self.mesh(ctx)
        ctx.progress(2, 2)
        return [StageOutput(out, ArtifactKind.MESH_GLB)]


class Random(Base):
    def run(self, ctx):
        out = samples.box_glb(ctx.out_dir / "shape.glb", half=(0.3 + random.random(), 0.4, 0.15))
        return [StageOutput(out, ArtifactKind.MESH_GLB)]


class IgnoresCancel(Base):
    def run(self, ctx):
        for i in range(3):
            ctx.progress(i, 3)
        return [StageOutput(self.mesh(ctx), ArtifactKind.MESH_GLB)]


class WritesElsewhere(Base):
    def run(self, ctx):
        return [StageOutput(self.mesh(ctx, "../elsewhere.glb"), ArtifactKind.MESH_GLB)]


class Corrupt(Base):
    def run(self, ctx):
        out = self.mesh(ctx)
        data = bytearray(out.read_bytes())
        data[-4:] = struct.pack("<I", 1 << 30)  # the last index points far past the vertices
        out.write_bytes(bytes(data))
        return [StageOutput(out, ArtifactKind.MESH_GLB)]


class OpaqueCutout(Adapter):
    def capabilities(self):
        return Capabilities(tasks=(StageType.BACKGROUND_REMOVAL,), devices=(Device.CPU,))

    def run(self, ctx):
        out = samples.subject_png(ctx.out_dir / "rgb.png", alpha=False)
        return [StageOutput(out, ArtifactKind.IMAGE_PNG)]


class EditsInputs(Good):
    def run(self, ctx):
        ctx.input(ArtifactKind.IMAGE_PNG).path.write_bytes(b"overwritten")
        return super().run(ctx)


class BadEstimate(Good):
    def estimate_vram(self, params):
        return 1.5  # GB as a float, not bytes


@pytest.mark.parametrize(
    ("adapter", "check", "says"),
    [
        (Random, "determinism[shape]", "different bytes"),
        (IgnoresCancel, "cancellation[shape]", "kept running"),
        (WritesElsewhere, "run[shape]", "outside ctx.out_dir"),
        (Corrupt, "run[shape]", "indexes past"),
        (OpaqueCutout, "run[background-removal]", "no alpha"),
        (EditsInputs, "inputs[shape]", "changed its inputs"),
        (BadEstimate, "vram", "want bytes"),
    ],
)
def test_the_suite_reports_broken_adapters(adapter, check, says, tmp_path):
    report = run_suite(adapter, work_dir=tmp_path)
    result = report.result(check)
    assert result is not None and result.status == "fail", report.format()
    assert says in result.detail
    assert not report.ok


def test_a_device_the_adapter_does_not_list_fails_to_load(tmp_path):
    class GpuOnly(Good):
        def capabilities(self):
            return Capabilities(tasks=(StageType.SHAPE,), devices=(Device.CUDA,))

    report = run_suite(GpuOnly, device="cpu", work_dir=tmp_path)
    assert report.result("load").status == "fail" and "not cpu" in report.result("load").detail


def test_the_suite_checks_the_manifest_against_the_adapter(tmp_path):
    doc = {
        "id": "good",
        "family": "test",
        "adapter": "x:Good",
        "tasks": ["shape", "texture"],
        "adapter_version": "2",
        "license": {"spdx": "MIT", "commercial": True},
        "requirements": {"cuda": {"status": "supported"}},
        "params": {"steps": {"type": "integer", "default": 200, "maximum": 100}},
    }
    detail = run_suite(Good, manifest=doc, work_dir=tmp_path).result("manifest").detail
    for words in (
        "[shape, texture] in the manifest",
        "bump both",
        "license.url",
        "cuda",
        "above the maximum",
    ):
        assert words in detail
    del doc["license"]
    assert (
        "license.spdx is missing"
        in run_suite(Good, manifest=doc, work_dir=tmp_path).result("manifest").detail
    )


def test_checks_can_be_skipped_by_name(tmp_path):
    report = run_suite(Random, skip=["determinism", "reload"], work_dir=tmp_path)
    assert report.ok and report.result("determinism[shape]").status == "skip"


def test_cli_exit_code_says_whether_it_conforms(capsys):
    with pytest.raises(SystemExit) as ok:
        sdk_main(["test", str(MANIFESTS / "stub-shape.yaml"), "--json"])
    assert ok.value.code == 0 and json.loads(capsys.readouterr().out)["ok"]
    with pytest.raises(SystemExit) as bad:
        sdk_main(["test", f"{__name__}:IgnoresCancel"])
    assert bad.value.code == 1 and "FAIL  cancellation[shape]" in capsys.readouterr().out


# --- artifact validators ------------------------------------------------------------------


def test_png_round_trip_and_damage(tmp_path):
    path = samples.subject_png(tmp_path / "a.png", alpha=True, size=40)
    info = artifacts.read_png_info(path.read_bytes())
    assert (info.width, info.height, info.channels, info.has_alpha) == (40, 40, 4, True)
    assert not artifacts.read_png_info(
        samples.subject_png(tmp_path / "b.png", alpha=False).read_bytes()
    ).has_alpha
    data = bytearray(path.read_bytes())
    data[30] ^= 0xFF  # inside IHDR's CRC
    with pytest.raises(artifacts.InvalidArtifact, match="CRC"):
        artifacts.read_png_info(bytes(data))
    with pytest.raises(artifacts.InvalidArtifact, match="signature"):
        artifacts.read_png_info(b"GIF89a")


def test_glb_writer_output_loads_in_trimesh(tmp_path):
    path = samples.box_glb(tmp_path / "box.glb", textured=True)
    info = artifacts.read_glb_info(path.read_bytes())
    assert (info.triangles, info.vertices, info.has_uv, info.textured) == (12, 24, True, True)
    mesh = trimesh.load(path, force="mesh")
    assert len(mesh.faces) == 12 and mesh.visual.uv.shape == (24, 2)
    assert mesh.area == pytest.approx(2 * (0.6 * 0.8 + 0.6 * 0.3 + 0.8 * 0.3))


def test_glb_from_trimesh_validates(tmp_path):
    path = mesh_ops.save(trimesh.creation.icosphere(subdivisions=2), tmp_path / "ico.glb")
    assert artifacts.validate(path, ArtifactKind.MESH_GLB).triangles == 320


def test_glb_damage_is_caught(tmp_path):
    good = samples.box_glb(tmp_path / "box.glb").read_bytes()
    with pytest.raises(artifacts.InvalidArtifact, match="header says"):
        artifacts.read_glb_info(good + b"\x00\x00\x00\x00")
    with pytest.raises(artifacts.InvalidArtifact, match="magic"):
        artifacts.read_glb_info(b"PK" + good[2:])


def test_meshopt_compressed_glb_validates():
    """gltfpack -cc output: the views live in a fallback buffer with no bytes of its own."""
    doc = {
        "asset": {"version": "2.0"},
        "extensionsUsed": ["EXT_meshopt_compression"],
        "buffers": [
            {"byteLength": 16},
            {"byteLength": 48, "extensions": {"EXT_meshopt_compression": {"fallback": True}}},
        ],
        "bufferViews": [
            {
                "buffer": 1,
                "byteOffset": 0,
                "byteLength": 36,
                "byteStride": 12,
                "extensions": {
                    "EXT_meshopt_compression": {"buffer": 0, "byteLength": 16, "byteStride": 12, "count": 3}
                },
            }
        ],
        "accessors": [{"bufferView": 0, "componentType": 5126, "count": 3, "type": "VEC3"}],
        "meshes": [{"primitives": [{"attributes": {"POSITION": 0}}]}],
    }
    text = json.dumps(doc).encode()
    text += b" " * (-len(text) % 4)
    binary = bytes(16)
    glb = (
        b"glTF"
        + struct.pack("<II", 2, 28 + len(text) + len(binary))
        + struct.pack("<II", len(text), 0x4E4F534A)
        + text
        + struct.pack("<II", len(binary), 0x004E4942)
        + binary
    )
    info = artifacts.read_glb_info(glb)
    assert info.triangles == 1 and info.extensions == ("EXT_meshopt_compression",)
