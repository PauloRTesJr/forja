import { describe, expect, it } from 'vitest'
import { rigOf, type Asset } from '../lib/api'
import { BEND_MS, bendPhase, genericAngle, HELD, HUMANOID_REQUIRED, humanoidPose, skeletonOf } from './rig'

describe('skeletonOf', () => {
  it('calls a skeleton humanoid only when every required bone is named', () => {
    expect(skeletonOf([...HUMANOID_REQUIRED, 'LeftToes', 'Bone_12'])).toBe('humanoid')
    expect(skeletonOf(HUMANOID_REQUIRED.filter((n) => n !== 'LeftFoot'))).toBe('generic')
    expect(skeletonOf(['Root', 'Bone_1'])).toBe('generic')
    expect(skeletonOf([])).toBeUndefined()
  })
})

describe('bend test', () => {
  it('starts and ends at the rest pose and stops after its loop', () => {
    for (const t of [0, BEND_MS]) {
      const p = bendPhase(t)
      expect(p.swing).toBeCloseTo(0)
      expect(p.flex).toBeCloseTo(0)
    }
    expect(bendPhase(BEND_MS / 2).done).toBe(false)
    expect(bendPhase(BEND_MS).done).toBe(true)
    expect(bendPhase(BEND_MS * 3).done).toBe(true)
    expect(bendPhase(BEND_MS / 8).swing).toBeCloseTo(1)
  })

  it('bends elbows and knees one way only', () => {
    for (let t = 0; t <= BEND_MS; t += BEND_MS / 40) {
      const p = bendPhase(t)
      expect(p.flex).toBeGreaterThanOrEqual(0)
      expect(p.flex).toBeLessThanOrEqual(1)
    }
  })

  it('mirrors the arms and legs of a humanoid', () => {
    const pose = humanoidPose(HELD)
    expect(pose.LeftUpperArm![2]).toBeCloseTo(-pose.RightUpperArm![2])
    expect(pose.LeftLowerArm![1]).toBeCloseTo(-pose.RightLowerArm![1])
    expect(pose.LeftUpperLeg![0]).toBeCloseTo(-pose.RightUpperLeg![0])
    // Facing +Z with the left arm along +X: a negative turn about Y brings the forearm forward.
    expect(pose.LeftLowerArm![1]).toBeLessThan(0)
    expect(pose.Hips).toBeUndefined() // the whole body would move
  })

  it('bends deeper bones of a generic chain less and leaves the root alone', () => {
    expect(genericAngle(HELD, 0)).toBe(0)
    expect(Math.abs(genericAngle(HELD, 1))).toBeGreaterThan(Math.abs(genericAngle(HELD, 3)))
  })
})

describe('rigOf', () => {
  const asset = (stats?: Asset['stats']): { stats?: Asset['stats'] } => ({ stats })

  it('reads the bones and skeleton the core found in the GLB', () => {
    expect(rigOf(asset({ triangles: 30000, bones: 52, skeleton: 'humanoid' }))).toEqual({ bones: 52, skeleton: 'humanoid' })
    expect(rigOf(asset({ bones: 7, skeleton: 'something-new' }))).toEqual({ bones: 7, skeleton: undefined })
  })

  it('says nothing for an asset without bones', () => {
    expect(rigOf(asset({ triangles: 5000 }))).toBeUndefined()
    expect(rigOf(asset({ bones: 0 }))).toBeUndefined()
    expect(rigOf(asset())).toBeUndefined()
  })
})
