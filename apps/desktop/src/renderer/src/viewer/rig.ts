// Pose math for rigged assets: what kind of skeleton a model has, and the procedural "bend
// test" that flexes its limbs so skin weights can be judged. Pure, so it is testable without
// a WebGL context; scene.ts applies the angles to the bones.

export type Skeleton = 'humanoid' | 'generic'

/** The bones a game engine's humanoid avatar cannot do without (Unity HumanBodyBones names). */
export const HUMANOID_REQUIRED = [
  'Hips',
  'Spine',
  'Head',
  'LeftUpperArm',
  'LeftLowerArm',
  'LeftHand',
  'RightUpperArm',
  'RightLowerArm',
  'RightHand',
  'LeftUpperLeg',
  'LeftLowerLeg',
  'LeftFoot',
  'RightUpperLeg',
  'RightLowerLeg',
  'RightFoot',
] as const

/** Same rule as the core's GLB reader: humanoid when every required bone name is there. */
export function skeletonOf(names: Iterable<string>): Skeleton | undefined {
  const have = new Set(names)
  if (have.size === 0) return undefined
  return HUMANOID_REQUIRED.every((n) => have.has(n)) ? 'humanoid' : 'generic'
}

export const BEND_MS = 2400
const CYCLES = 2

export interface Phase {
  /** -1..1, for swings that go both ways (arms up and down, legs forward and back). */
  swing: number
  /** 0..1, for joints that bend one way only (elbows, knees). */
  flex: number
  done: boolean
}

/** Where the test is after `elapsed` ms: whole cycles, so it starts and ends at the rest pose. */
export function bendPhase(elapsed: number): Phase {
  const k = Math.min(1, Math.max(0, elapsed / BEND_MS))
  const a = 2 * Math.PI * CYCLES * k
  return { swing: Math.sin(a), flex: (1 - Math.cos(a)) / 2, done: k >= 1 }
}

/** The peak of the test, held still when the user asked for reduced motion. */
export const HELD: Phase = { swing: 1, flex: 1, done: false }

export type Euler3 = [number, number, number]

/**
 * Local rotations (radians, XYZ) on top of each bone's rest rotation. The rest pose has no
 * rotation and the character faces +Z with its left at +X, so local axes are world axes:
 * Z raises an arm held out to the side, Y swings a forearm forward, X swings a leg.
 */
export function humanoidPose({ swing, flex }: Phase): Record<string, Euler3> {
  return {
    Spine: [0.12 * swing, 0, 0],
    Chest: [0.08 * swing, 0, 0],
    Head: [0, 0.5 * swing, 0],
    LeftUpperArm: [0, 0, 0.6 * swing],
    RightUpperArm: [0, 0, -0.6 * swing],
    LeftLowerArm: [0, -1.2 * flex, 0],
    RightLowerArm: [0, 1.2 * flex, 0],
    LeftUpperLeg: [-0.5 * swing, 0, 0],
    RightUpperLeg: [0.5 * swing, 0, 0],
    LeftLowerLeg: [0.9 * flex, 0, 0],
    RightLowerLeg: [0.9 * flex, 0, 0],
  }
}

/** A bone `depth` links below the root bends less, so a chain curls instead of folding. */
export function genericAngle({ swing }: Phase, depth: number): number {
  return depth < 1 ? 0 : (0.4 * swing) / depth
}
