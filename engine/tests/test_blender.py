import sys

import numpy as np
import pytest
import trimesh

from forja_engine import blender
from forja_engine.adapters import builtin
from forja_engine.ops import blender_mesh
from forja_engine.ops import mesh as m
from forja_engine.sdk import ArtifactKind, StageContext, StageInput, StageType

needs_blender = pytest.mark.skipif(blender.problem() is not None, reason="Blender 4.2+ is not installed")


def fake_blender(tmp_path, version: str):
    """A program that answers --version as the given Blender would."""
    if sys.platform == "win32":
        path = tmp_path / "blender.cmd"
        path.write_text(f"@echo Blender {version}\r\n")
    else:
        path = tmp_path / "blender"
        path.write_text(f"#!/bin/sh\necho 'Blender {version}'\n")
        path.chmod(0o755)
    return str(path)


def test_a_configured_blender_is_the_only_one_tried(tmp_path, monkeypatch):
    monkeypatch.setenv(blender.ENV, str(tmp_path / "nowhere" / "blender.exe"))
    with pytest.raises(blender.BlenderError, match="there is no Blender at"):
        blender.find()
    monkeypatch.setenv(blender.ENV, sys.executable)  # a program, but not Blender
    assert "did not answer as Blender" in str(blender.problem())


def test_blender_older_than_4_2_is_refused(tmp_path, monkeypatch):
    monkeypatch.setenv(blender.ENV, fake_blender(tmp_path, "3.6.5"))
    assert "too old" in str(blender.problem())
    (tmp_path / "new").mkdir()
    monkeypatch.setenv(blender.ENV, fake_blender(tmp_path / "new", "4.2.0"))
    assert blender.find().version == "4.2.0"


def test_engines_without_blender_do_not_offer_its_stages(monkeypatch):
    def missing():
        raise blender.BlenderError("Blender 4.2 or newer was not found")

    monkeypatch.setattr(builtin.blender, "find", missing)
    stages, unavailable = builtin.offered()
    assert "mesh.remesh" not in stages and "mesh.uv.smart" not in stages
    assert unavailable == {
        "mesh.remesh": "Blender 4.2 or newer was not found",
        "mesh.uv.smart": "Blender 4.2 or newer was not found",
    }
    assert stages["bake"] == builtin.Bake.version

    monkeypatch.setattr(builtin.blender, "find", lambda: blender.Blender("/opt/blender", "5.2.1"))
    stages, unavailable = builtin.offered()
    # The Blender is part of the version, so a new Blender keys the stage cache apart.
    assert stages["mesh.remesh"] == "1+blender-5.2.1" and unavailable == {}


def ctx(tmp_path, task, mesh, **params):
    path = m.save(mesh, tmp_path / "in.glb")
    return StageContext(task.value, task, [StageInput(path, ArtifactKind.MESH_GLB, "")], params, 7, tmp_path)


def holed_sphere() -> trimesh.Trimesh:
    sphere = trimesh.creation.icosphere(subdivisions=5)
    sphere.update_faces(np.arange(len(sphere.faces)) % 997 != 0)  # a few holes: QuadriFlow refuses it as is
    sphere.remove_unreferenced_vertices()
    return sphere


@needs_blender
def test_quad_remesh_stays_within_the_triangle_budget(tmp_path):
    for mesh in (trimesh.creation.icosphere(subdivisions=5), holed_sphere()):
        out = m.load(
            builtin.Remesh().run(ctx(tmp_path, StageType.MESH_REMESH, mesh, target_tris=1200))[0].path
        )
        assert 600 <= len(out.faces) <= 1200
        assert out.is_watertight
        # Still the sphere: every vertex lies close to the unit surface.
        assert np.abs(np.linalg.norm(out.vertices, axis=1) - 1).max() < 0.05


@needs_blender
def test_smart_uv_gives_every_face_uvs_and_welded_normals(tmp_path):
    box = trimesh.creation.box((1.0, 0.5, 0.25)).subdivide().subdivide()
    out = m.load(
        builtin.SmartUnwrap().run(ctx(tmp_path, StageType.MESH_UV_SMART, box))[0].path, keep_normals=True
    )
    uv = m.uvs_of(out)
    assert len(out.faces) == len(box.faces)
    assert uv.min() >= 0 and uv.max() <= 1
    assert len(out.vertices) > len(box.vertices)  # split along the new seams
    # A box's islands are its six sides: one per cube face direction.
    islands = trimesh.graph.connected_components(out.face_adjacency, nodes=np.arange(len(out.faces)))
    assert len(islands) == 6


@needs_blender
def test_remeshed_and_smart_unwrapped_mesh_bakes(tmp_path):
    from forja_engine.ops import bake

    high = trimesh.creation.icosphere(subdivisions=5)
    low = blender_mesh.smart_uv(blender_mesh.quad_remesh(high, 800))
    r = bake.bake(low, high, 64, ao_samples=2)
    assert r.hit_ratio > 0.99
