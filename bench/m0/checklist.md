# M0 acceptance checklist

**Fill in and commit this before the first real run** (decision 4). The gate is decided by
this list, not by impressions afterwards.

An output **passes** when every "must" holds, judged in the Forja viewer and in Unity at the
camera distance the asset is meant for. Score each image in `scores.csv`.

## Must
- [ ] **Silhouette**: from the front, the outline matches the reference image.
- [ ] **Clean geometry**: no holes, floaters or spikes visible at game-camera distance.
- [ ] **No baked lighting**: base color has no shadows or highlights painted in.
- [ ] **Plausible back**: sides and back read as the same object (not a smear or mirror).
- [ ] **Budget**: at or under 5,000 triangles; LOD1 and LOD2 present.
- [ ] **Texture fit**: front texture lines up with the geometry (no visible offset or rotation).
- [ ] **Imports cleanly**: Unity imports the GLB via glTFast at correct scale and facing.

## Should (recorded, not gating)
- [ ] Thin parts survive (handles, straps, antennae).
- [ ] Texture seams invisible at game distance.
- [ ] Usable without any manual cleanup (success target: 80% of the set).

## Gate
- **Local passes**: 14 or more of 20 images pass → continue as specified.
- **Only cloud passes**: run the cloud arm (README); if it passes, default texturing routes to
  cloud and the claim becomes "local where it fits".
- **Neither passes**: stop and revisit in six months.
