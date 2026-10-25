# Rigging

Forja can rig a character: give it a skeleton and skin weights, so it can be posed and animated in Blender or a game engine. Rigging is an optional stage of the Hero character preset, off by default. Forja makes no animation: you bring your own clips, or retarget a library's onto the rig.

## Turn it on

**In the app:** on Generate, pick **Hero character** and turn on **Rig the character**. The switch is greyed out, with the reason, when no rig model is ready on any provider.

**From a terminal:**

```sh
forja generate knight.png --preset hero-character --with rig
forja generate knight.png --preset hero-character --with rig --set rig.model=unirig
```

The rig stage (`rig`, stage type `rig`) runs after the bake and before the LODs, so it rigs the low-poly mesh that ships, and every level of detail carries the same skin. The job still makes one GLB, now skinned, and the stage also writes the skeleton's profile, `skeleton.json` ([below](#humanoids-and-bone-names)).

## The rig models

| Model | id | VRAM | Disk | License | What it does |
|---|---|---|---|---|---|
| SkinTokens (TokenRig) | `skintokens` | 14 GB (needs 14) | 8.1 GB | MIT | Predicts the skeleton and the skin weights together; can also weight Forja's standard humanoid skeleton. Ranked first where it fits |
| UniRig | `unirig` | 8.7 GB (needs 8) | 10.7 GB | MIT¹ | Predicts a skeleton, then skin weights for it. The one that fits a 12 GB card |
| Stub rig | `stub-rig` | | | Apache-2.0 | A deterministic CPU stand-in for tests, used only with `models.stubs = true` |

1. UniRig's upstream tree, fetched at install time, also carries the Michelangelo shape encoder under GPL-3.0.

An open rig stage gets the highest-ranked ready model, as any stage does ([presets](presets.md#which-model-a-stage-uses)): SkinTokens where it is ready, else UniRig. Pin one for every job with `forja models pin rig unirig`, or for one job with `--set rig.model=<id>`. Both need an NVIDIA GPU; neither runs on a Mac or on the CPU, so a computer without one sends the stage to another provider ([routing](routing.md)).

When no rig model is ready, Generate greys out "Rig the character" and offers **Install UniRig** beside it: the rig model that fits this computer, with what the computer needs, its license and the install's progress, as in the first setup. Once it is installed the switch turns on. From a terminal, `forja presets show hero-character` names the same model:

```sh
forja models install unirig
forja models install skintokens --provider studio-pc   # on a computer with 16 GB or more
```

### What fits

| | 8 GB | 12 GB | 16 GB | 24 GB |
|---|---|---|---|---|
| UniRig (8.7 GB, needs 8) | too big | fits | fits | fits |
| SkinTokens (14 GB, needs 14) | too big | too big | fits | fits |

UniRig peaked at about 8.7 GB on the reference 12 GB RTX 4070 Ti, rigging a 30,000-triangle character at its default 15 beams; its skeleton and skin models take the GPU in turn. SkinTokens needs 14 GB, so on a 12 GB card it is remote-only: install it on a computer with 16 GB or more and let routing send the stage there. A rig model takes turns on the GPU with the shape and texture models, like any other ([hardware](hardware.md)).

SkinTokens builds on PyTorch 2.7 with CUDA 12.8, which needs NVIDIA driver 570 or newer.

### Parameters

Set them per job with `--set rig.<param>=<value>`, or for every job under `[presets.hero-character.overrides]` ([presets](presets.md#changing-a-presets-values)).

| Model | Parameter | Default | What it does |
|---|---|---|---|
| UniRig | `beams` | 15 | Skeleton sampling beams, 1 to 20. Fewer use less memory and vary more between seeds |
| UniRig, SkinTokens | `humanoid` | true | Give a humanoid's bones the standard names |
| SkinTokens | `skeleton` | `predict` | `template` weights Forja's standard humanoid skeleton instead of predicting one |
| SkinTokens | `beams` | 10 | Sampling beams, 1 to 20 |
| SkinTokens | `top_k`, `top_p`, `temperature`, `repetition_penalty` | 5, 0.95, 1.0, 2.0 | Upstream's sampling settings |
| SkinTokens | `postprocess` | false | Clean the weights with upstream's voxel pass |
| Stub rig | `profile` | `auto` | `humanoid`, `generic`, or `auto` (a humanoid for a standing figure) |

### flash-attn on Windows

Both rig families use flash-attn, which publishes Linux wheels only. On Linux, installing a rig model builds it in. Elsewhere, Windows included, Forja stands in for the calls the models make with PyTorch's own attention (`scaled_dot_product_attention`, which picks a fused CUDA kernel by itself), and the results match within fp16 precision. Nothing changes for you; the model installs and runs the same way.

## Humanoids and bone names

A skeleton is a **humanoid** or **generic**.

- A predicted skeleton whose bones map onto a humanoid gets the bone names of Unity's HumanBodyBones, which are also VRM's humanoid names: the names Unity's and Unreal's retargeting map with no manual steps.
- It must have all 15 required bones: `Hips`, `Spine`, `Head`, `LeftUpperArm`, `LeftLowerArm`, `LeftHand`, `RightUpperArm`, `RightLowerArm`, `RightHand`, `LeftUpperLeg`, `LeftLowerLeg`, `LeftFoot`, `RightUpperLeg`, `RightLowerLeg` and `RightFoot`. `Chest`, `UpperChest`, `Neck`, `LeftShoulder`, `RightShoulder`, `LeftToes` and `RightToes` are used when there. A skeleton missing a required bone stays **generic**, with the names the model gave it.
- Coordinates are glTF's: Y up, in metres, the character facing +Z, so its left side is at +X.

The engine renames the mapped bones in the GLB itself, and keeps what each one was called in the profile. `skeleton.json` holds the profile (`humanoid` or `generic`), the naming (`unity-humanoid` for a humanoid), the model that made it, every bone with its parent and position, and `bone_map`, from each humanoid name to the predicted bone's own name. The core and the viewer tell a humanoid by its bone names, so a rig stays a humanoid through tools that drop glTF extras.

Each vertex keeps its four strongest bone influences, normalized.

## LODs and exports

The LOD stage carries the skin to every level: each lower level takes its weights from the full-detail surface, as it takes its UVs, so all levels bend with one skeleton. The GLB has one skin shared by every level's mesh, with the joints in the rest pose.

The library's exports through Blender keep the armature in FBX, glTF and .blend ([library](library.md#export-to-other-formats)), and Open in Blender imports it with the mesh. A rigged FBX is written without leaf bones and without an animation take.

An FBX with a rig keeps Blender's axis conversion in its header instead of baking it into the objects, because Blender documents that baking as broken with armatures. A game engine may then show the armature's root rotated -90° on X while the model looks upright. That is the conversion, not a broken rig.

## See the rig

- **Library**: a rigged asset's card says **Rigged**; its page has a **Rigged** tag, whose tooltip gives the bone count and, for a humanoid, that the bones are named for humanoid retargeting.
- **Viewer**: a rigged model gets two buttons. **Skeleton** draws the bones over the model. **Bend test** flexes the limbs for 2.4 seconds and returns to the rest pose, so bad weights show as tearing or collapsing skin: a humanoid bends its spine, turns its head, raises its arms and swings its legs, bending elbows and knees; a generic rig curls along its chain. With reduced motion, it holds one bent pose for a second instead.
- **CLI**: `forja assets ls` has a `RIG` column (`humanoid` or `generic`), and `forja assets show <asset-id>` prints `rigged     humanoid, 22 bones`.
