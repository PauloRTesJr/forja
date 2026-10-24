import time
from pathlib import Path

import pytest
import trimesh

from forja_engine.metrics import Sampler
from forja_engine.ops import mesh as m


def test_the_sampler_keeps_the_peak_memory_and_the_mean_utilization():
    rss = iter([100, 300, 200] + [150] * 100)
    util = iter([40.0, 60.0] + [50.0] * 100)
    with Sampler(lambda: next(rss), lambda: next(util), hz=100) as s:
        time.sleep(0.1)
    assert s.peak_ram_bytes == 300
    assert s.gpu_util is not None and 40 <= s.gpu_util <= 60


def test_a_sampler_with_nothing_to_read_reads_nothing():
    with Sampler() as s:
        pass
    assert s.peak_ram_bytes is None and s.gpu_util is None
    # Readings that fail (a process gone, no NVML) leave the value unknown.
    with Sampler(lambda: None, lambda: None, hz=100) as s:
        time.sleep(0.05)
    assert s.peak_ram_bytes is None and s.gpu_util is None


def test_output_stats_judge_watertightness_on_positions_across_uv_seams(tmp_path: Path):
    box = m.unwrap(trimesh.creation.box())
    assert not trimesh.Trimesh(box.vertices, box.faces, process=False).is_watertight  # split at seams
    stats = m.file_stats(str(m.save(box, tmp_path / "box.glb")))
    assert stats is not None
    assert stats["triangles"] == 12 and stats["watertight"] is True
    assert 0 < stats["uv_coverage"] <= 1
    open_box = trimesh.Trimesh(box.vertices, box.faces[:-1], process=False)
    assert m.file_stats(str(m.save(open_box, tmp_path / "open.glb")))["watertight"] is False


def test_output_stats_count_the_full_detail_level_only(tmp_path: Path):
    lod0 = trimesh.creation.icosphere(subdivisions=2)
    scene = trimesh.Scene()
    scene.add_geometry(lod0, node_name="asset_LOD0", geom_name="asset_LOD0")
    scene.add_geometry(m.decimate(lod0, 80), node_name="asset_LOD1", geom_name="asset_LOD1")
    stats = m.file_stats(str(m.save(scene, tmp_path / "lods.glb")))
    assert stats == {
        "triangles": len(lod0.faces),
        "vertices": len(lod0.vertices),
        "watertight": True,
        "uv_coverage": None,
    }


def test_output_stats_of_a_file_it_cannot_read_are_unknown(tmp_path: Path):
    bad = tmp_path / "packed.glb"
    bad.write_bytes(b"glTF not really")
    assert m.file_stats(str(bad)) is None


def test_uv_coverage_counts_overlaps_once():
    import numpy as np

    uv = np.array([[0, 0], [1, 0], [0, 1], [1, 1]], dtype=np.float64)
    half = np.array([[0, 1, 2]])
    assert m.uv_coverage([(uv, half)]) == pytest.approx(0.5, abs=0.02)
    assert m.uv_coverage([(uv, np.vstack([half, half]))]) == pytest.approx(0.5, abs=0.02)
    assert m.uv_coverage([(uv, np.array([[0, 1, 2], [1, 3, 2]]))]) == pytest.approx(1.0, abs=0.01)
