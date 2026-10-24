"""Rigging: skins in GLB files, the stub rig, LODs that keep the skin, and humanoid names."""

import json
import struct
from pathlib import Path

import numpy as np
import pytest
import trimesh
from PIL import Image

from forja_engine.adapters.builtin import Lods
from forja_engine.adapters.stub import StubRig
from forja_engine.ops import mesh as mesh_ops
from forja_engine.ops import rig as rig_ops
from forja_engine.ops import skin as skin_ops
from forja_engine.sdk import ArtifactKind, StageContext, StageInput, StageType


def glb_json(path: Path) -> dict:
    raw = path.read_bytes()
    n = struct.unpack_from("<I", raw, 12)[0]
    return json.loads(raw[20 : 20 + n])


def character(arms_out: bool = False) -> trimesh.Trimesh:
    """A blocky 1.8 m figure facing +Z: legs, torso, head and arms, textured and normal-mapped."""
    parts = [
        trimesh.creation.box((0.14, 0.9, 0.16), trimesh.transformations.translation_matrix([0.11, 0.45, 0])),
        trimesh.creation.box((0.14, 0.9, 0.16), trimesh.transformations.translation_matrix([-0.11, 0.45, 0])),
        trimesh.creation.box((0.42, 0.6, 0.22), trimesh.transformations.translation_matrix([0, 1.2, 0])),
        trimesh.creation.box((0.22, 0.26, 0.22), trimesh.transformations.translation_matrix([0, 1.66, 0])),
    ]
    for sign in (1, -1):
        if arms_out:
            arm = trimesh.creation.box(
                (0.62, 0.1, 0.1), trimesh.transformations.translation_matrix([sign * 0.52, 1.44, 0])
            )
        else:
            arm = trimesh.creation.box(
                (0.1, 0.62, 0.1), trimesh.transformations.translation_matrix([sign * 0.28, 1.14, 0])
            )
        parts.append(arm)
    body = trimesh.util.concatenate([p.subdivide().subdivide() for p in parts])
    body = mesh_ops.unwrap(body)
    material = trimesh.visual.material.PBRMaterial(
        baseColorTexture=Image.new("RGB", (32, 32), (180, 120, 90)),
        normalTexture=Image.new("RGB", (32, 32), (128, 128, 255)),
    )
    body.visual = trimesh.visual.TextureVisuals(uv=body.visual.uv, material=material)
    return body


def ctx(tmp_path: Path, task: StageType, path: Path, **params) -> StageContext:
    out = tmp_path / task.replace(".", "_")
    out.mkdir(exist_ok=True)
    return StageContext("s", task, [StageInput(path, ArtifactKind.MESH_GLB, "")], params, 7, out)


# --- skins in GLB files ----------------------------------------------------------------------


def two_bones() -> tuple[trimesh.Trimesh, skin_ops.Skeleton, skin_ops.VertexSkin]:
    mesh = mesh_ops.unwrap(trimesh.creation.box((0.2, 1.0, 0.2)).subdivide())
    skeleton = skin_ops.Skeleton(("Root", "Tip"), (-1, 0), np.array([[0, -0.5, 0], [0, 0.0, 0]]))
    dense = np.stack([0.5 - mesh.vertices[:, 1], 0.5 + mesh.vertices[:, 1]], axis=1)
    return mesh, skeleton, skin_ops.from_dense(dense)


def test_a_skin_round_trips_through_a_glb(tmp_path):
    mesh, skeleton, weights = two_bones()
    mesh.metadata[skin_ops.SKIN], mesh.metadata[skin_ops.SKELETON] = weights, skeleton
    path = mesh_ops.save(mesh, tmp_path / "rig.glb")
    assert (
        skin_ops.SKIN in mesh.metadata and skin_ops.SKELETON in mesh.metadata
    )  # the caller's mesh keeps them

    doc = glb_json(path)
    (skin,) = doc["skins"]
    joints = [doc["nodes"][j] for j in skin["joints"]]
    assert [j["name"] for j in joints] == ["Root", "Tip"]
    assert joints[1]["translation"] == [0, 0.5, 0] and joints[0]["children"] == [skin["joints"][1]]
    assert skin["skeleton"] in doc["scenes"][0]["nodes"]
    assert skin["extras"] == {"forja_skeleton": "generic"}
    prim = doc["meshes"][0]["primitives"][0]
    assert doc["accessors"][prim["attributes"]["JOINTS_0"]]["componentType"] == 5121
    mesh_node = next(n for n in doc["nodes"] if "mesh" in n)
    assert mesh_node["skin"] == 0
    assert "skin" not in doc["meshes"][0].get("extras", {})

    back, _ = skin_ops.read(path)
    assert back.names == skeleton.names and back.parents == skeleton.parents
    assert np.allclose(back.heads, skeleton.heads, atol=1e-6)
    loaded = skin_ops.attach(mesh_ops.load(path), path)
    got = loaded.metadata[skin_ops.SKIN]
    assert np.allclose(got.dense(2), weights.dense(2), atol=1e-6)
    assert np.allclose(got.weights.sum(axis=1), 1, atol=1e-6)


def test_dense_weights_keep_the_four_strongest_and_sum_to_one():
    w = np.array([[0.1, 0.2, 0.3, 0.4, 0.5, 0.0], [0, 0, 0, 0, 0, 0], [1, 0, 0, 0, 0, 0]], dtype=float)
    vs = skin_ops.from_dense(w)
    assert vs.joints.shape == (3, 4)
    assert set(vs.joints[0]) == {1, 2, 3, 4}
    assert np.allclose(vs.weights.sum(axis=1), 1)
    assert vs.joints[1, 0] == 0 and vs.weights[1, 0] == 1  # nothing reaches it: it follows the root
    assert vs.weights[2].tolist() == [1, 0, 0, 0]


def test_a_skeleton_refuses_loops_and_duplicate_names():
    with pytest.raises(ValueError, match="cycle"):
        skin_ops.Skeleton(("a", "b"), (1, 0), np.zeros((2, 3)))
    with pytest.raises(ValueError, match="unique"):
        skin_ops.Skeleton(("a", "a"), (-1, 0), np.zeros((2, 3)))


# --- the stub rig ------------------------------------------------------------------------------


@pytest.mark.parametrize("arms_out", [False, True])
def test_the_stub_rigs_a_character_as_a_humanoid(tmp_path, arms_out):
    src = mesh_ops.save(character(arms_out), tmp_path / "baked.glb")
    glb, profile = StubRig().run(ctx(tmp_path, StageType.RIG, src))
    assert glb.kind == ArtifactKind.MESH_GLB and profile.kind == ArtifactKind.JSON
    skeleton, skins = skin_ops.read(glb.path)
    assert skeleton.profile == "humanoid" and set(rig_ops.REQUIRED) <= set(skeleton.names)
    info = json.loads(profile.path.read_text())
    assert info["profile"] == "humanoid" and info["naming"] == "unity-humanoid"
    assert info["bone_map"]["LeftHand"] == "LeftHand" and len(info["bones"]) == len(skeleton)

    # The character's left hand is at +X; the vertices at the end of that arm follow it.
    mesh = mesh_ops.load(glb.path)
    (weights,) = skins.values()
    tip = np.argmax(mesh.vertices[:, 0])
    assert skeleton.names[weights.joints[tip, 0]] in ("LeftHand", "LeftLowerArm")
    hand = skeleton.heads[skeleton.names.index("LeftHand")]
    assert hand[0] > 0.2 and (hand[1] > 1.3 if arms_out else hand[1] < 1.2)
    # The painted maps survive, tangents included.
    doc = glb_json(glb.path)
    assert "TANGENT" in doc["meshes"][0]["primitives"][0]["attributes"]
    assert getattr(mesh.visual.material, "normalTexture", None) is not None


def test_the_stub_gives_a_prop_a_generic_chain_and_is_deterministic(tmp_path):
    chest = mesh_ops.unwrap(trimesh.creation.box((1.0, 0.6, 0.6)).subdivide())
    src = mesh_ops.save(chest, tmp_path / "chest.glb")
    first = StubRig().run(ctx(tmp_path, StageType.RIG, src))
    skeleton, _ = skin_ops.read(first[0].path)
    assert skeleton.profile == "generic" and skeleton.names[0] == "Root"
    a = [o.path.read_bytes() for o in first]
    b = [o.path.read_bytes() for o in StubRig().run(ctx(tmp_path, StageType.RIG, src))]
    assert a == b
    forced = StubRig().run(ctx(tmp_path, StageType.RIG, src, profile="humanoid"))
    assert skin_ops.read(forced[0].path)[0].profile == "humanoid"


def test_lods_carry_the_skin_to_every_level(tmp_path):
    src = mesh_ops.save(character(), tmp_path / "baked.glb")
    rigged = StubRig().run(ctx(tmp_path, StageType.RIG, src))[0].path
    out = Lods().run(ctx(tmp_path, StageType.MESH_LODS, rigged, ratios=[0.5, 0.25], name="hero"))[0].path
    doc = glb_json(out)
    assert len(doc["skins"]) == 1
    lods = [n for n in doc["nodes"] if "mesh" in n]
    assert [n["name"] for n in lods] == ["hero_LOD0", "hero_LOD1", "hero_LOD2"]
    assert all(n["skin"] == 0 for n in lods)
    skeleton, skins = skin_ops.read(out)
    assert skeleton.profile == "humanoid" and len(skins) == 3
    for vs in skins.values():
        assert np.allclose(vs.weights.sum(axis=1), 1, atol=1e-5)
        assert vs.joints.max() < len(skeleton)
    # A level's vertex near the left hand is moved by the left arm, as LOD0's are.
    mesh = trimesh.load(out, file_type="glb", process=False)
    lod2 = mesh.geometry["hero_LOD2"]
    tip = np.argmax(lod2.vertices[:, 0])
    assert skeleton.names[skins["hero_LOD2"].joints[tip, 0]].startswith("Left")


def test_lods_of_an_unrigged_mesh_have_no_skin(tmp_path):
    src = mesh_ops.save(mesh_ops.unwrap(trimesh.creation.icosphere(subdivisions=3)), tmp_path / "ball.glb")
    out = Lods().run(ctx(tmp_path, StageType.MESH_LODS, src))[0].path
    assert "skins" not in glb_json(out)


# --- humanoid names for a predicted skeleton ---------------------------------------------------


def predicted(seed: int = 0) -> tuple[skin_ops.Skeleton, dict[int, str]]:
    """The template as a rig model might predict it: anonymous names in its own joint order,
    one spine joint fewer, fingers on each hand, a tail and a head end bone. Also returns what
    each joint really is."""
    t = rig_ops.template(character())
    names, parents, heads = list(t.names), list(t.parents), list(t.heads)

    def add(name: str, parent: str, offset: list[float]) -> None:
        names.append(name)
        parents.append(names.index(parent))
        heads.append(heads[names.index(parent)] + np.asarray(offset))

    for side, sign in (("Left", 1), ("Right", -1)):
        for f in range(3):
            add(f"{side}Finger{f}a", f"{side}Hand", [sign * 0.05, -0.03, 0.02 * (f - 1)])
            add(f"{side}Finger{f}b", f"{side}Finger{f}a", [sign * 0.03, -0.02, 0])
    add("Tail1", "Hips", [0, -0.05, -0.15])
    add("Tail2", "Tail1", [0, -0.1, -0.2])
    add("HeadEnd", "Head", [0, 0.16, 0])
    chest = names.index("Chest")
    parents = [parents[chest] if p == chest else p for p in parents]
    parents = [p - 1 if p > chest else p for p in parents]
    del names[chest], parents[chest], heads[chest]

    order = np.random.default_rng(seed).permutation(len(names))  # order[new] = old
    new = {int(old): i for i, old in enumerate(order)}
    skeleton = skin_ops.Skeleton(
        tuple(f"bone_{i}" for i in range(len(names))),
        tuple(new[parents[old]] if parents[old] >= 0 else -1 for old in order),
        np.array([heads[old] for old in order]),
    )
    return skeleton, {i: names[old] for i, old in enumerate(order)}


@pytest.mark.parametrize("seed", [0, 1, 2])
def test_a_predicted_humanoid_gets_the_standard_names(seed):
    skeleton, truth = predicted(seed)
    named, bone_map = rig_ops.as_humanoid(skeleton)
    assert named.profile == "humanoid"
    by_name = {n: j for j, n in enumerate(named.names)}
    for standard in (
        "Hips",
        "Spine",
        "Neck",
        "Head",
        "LeftUpperArm",
        "LeftLowerArm",
        "LeftHand",
        "RightHand",
        "LeftUpperLeg",
        "LeftLowerLeg",
        "LeftFoot",
        "LeftToes",
        "RightFoot",
    ):
        assert truth[by_name[standard]] == standard, standard
    # Two spine joints below the arms: the lower is the spine, the one the arms leave is the chest.
    assert truth[by_name["Chest"]] == "UpperChest" and "UpperChest" not in by_name
    assert truth[by_name["LeftShoulder"]] == "LeftShoulder"
    assert bone_map["LeftHand"] == skeleton.names[by_name["LeftHand"]]
    # Fingers and the tail keep their own names, and nothing else claims a humanoid one.
    assert sum(n in rig_ops.HUMANOID_BONES for n in named.names) == len(bone_map)


def test_a_quadruped_stays_generic():
    # Hips with hind legs to the ground on both sides and a spine up to a raised head look like
    # a humanoid's, but the "arms" off the chest also reach the ground: they are front legs.
    names, parents, heads = ["pelvis"], [-1], [[0, 0.6, -0.3]]

    def add(parent: int, xyz: list[float]) -> int:
        parents.append(parent)
        heads.append(xyz)
        names.append(f"b{len(names)}")
        return len(names) - 1

    chest = add(0, [0, 0.65, 0.3])
    neck = add(chest, [0, 0.9, 0.5])
    add(neck, [0, 1.0, 0.6])
    for x in (0.15, -0.15):
        for root, z in ((0, -0.3), (chest, 0.3)):
            j = add(root, [x, 0.45, z])
            j = add(j, [x, 0.25, z])
            j = add(j, [x, 0.02, z])
            add(j, [x, 0.0, z + 0.08])
    tail = add(0, [0, 0.55, -0.5])
    add(tail, [0, 0.5, -0.8])
    dog = skin_ops.Skeleton(tuple(names), tuple(parents), np.array(heads, dtype=float))
    named, bone_map = rig_ops.as_humanoid(dog)
    assert named.profile == "generic" and bone_map == {} and named.names == dog.names


def test_a_bone_whose_own_name_is_a_humanoid_one_gives_it_up():
    skeleton, truth = predicted(0)
    finger = next(j for j, n in truth.items() if n == "LeftFinger0a")
    names = list(skeleton.names)
    names[finger] = "Head"
    named, _ = rig_ops.as_humanoid(skin_ops.Skeleton(tuple(names), skeleton.parents, skeleton.heads))
    assert truth[named.names.index("Head")] == "Head"
    assert named.names[finger] == f"Head_{finger}"
    assert len(set(named.names)) == len(named.names)
