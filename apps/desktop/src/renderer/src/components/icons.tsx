// Line-art icons for the Arcane Forge look. All are decoration: callers label the control.
import type { ReactNode } from 'react'

type IconProps = { size?: number; className?: string }

const SPARK = 'M8 0Q9.3 6.7 16 8Q9.3 9.3 8 16Q6.7 9.3 0 8Q6.7 6.7 8 0Z'

function Line({ size = 20, className, view = 24, width = 1.4, children }: IconProps & { view?: number; width?: number; children: ReactNode }) {
  return (
    <svg
      aria-hidden
      className={className}
      width={size}
      height={size}
      viewBox={`0 0 ${view} ${view}`}
      fill="none"
      stroke="currentColor"
      strokeWidth={width}
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      {children}
    </svg>
  )
}

/** The four-point star used as the bullet, ornament and stage marker throughout. */
export function Sparkle({ size = 10, className }: IconProps) {
  return (
    <svg aria-hidden className={className} width={size} height={size} viewBox="0 0 16 16">
      <path d={SPARK} fill="currentColor" />
    </svg>
  )
}

/** A stage marker; fill and outline come from CSS by status. */
export function Star({ size = 18, className }: IconProps) {
  return (
    <svg aria-hidden className={`star ${className ?? ''}`} width={size} height={size} viewBox="-1 -1 18 18">
      <path d={SPARK} />
    </svg>
  )
}

// Traced from the app icon (build/icon.*): coordinates are the source artwork's pixels, the viewBox frames the emblem.
const EMBLEM = {
  ring: 'M997.3 524.7A376 376 0 0 0 692.3 219.7M561.7 219.7A376 376 0 0 0 256.7 524.7M256.7 655.3A376 376 0 0 0 997.3 655.3',
  anvil:
    'M306 563H958L900 617C830 645 738 642 722 740C722 790 772 826 799 830V868H748C720 823 540 823 507 868H458V832C497 820 540 800 552 770Q560 752 560 740C560 690 450 625 310 601L306 597Z',
  spark:
    'M627 130C628.5 300 636 392 690 398C655 405 635 440 635 470V538Q635 559 642 563H612Q619 559 619 538V470C619 440 599 405 564 398C618 392 625.5 300 627 130Z',
}

/** The Forja emblem from the app icon: spark, anvil and broken ring. `muted` is the dim gold of empty states. */
export function BrandMark({ size = 36, muted = false }: IconProps & { muted?: boolean }) {
  const gold = muted ? 'var(--gold-dim)' : 'var(--accent)'
  return (
    <svg aria-hidden width={size} height={size} viewBox="197 125 860 860">
      {/* Heavier than in the artwork, so the ring still reads at sidebar size. */}
      <path d={EMBLEM.ring} fill="none" stroke={gold} strokeWidth="28" />
      <path d={EMBLEM.anvil} fill={gold} />
      <path d={EMBLEM.spark} fill={muted ? gold : 'var(--accent-hi)'} />
    </svg>
  )
}

export const Anvil = (p: IconProps) => (
  <Line {...p}>
    <path d="M3 9h12c.6 1.9 2.6 3 6 3v1.5h-6.5L12.5 16v3H15v1.5H6V19h2.5v-3L6 13.5H4.5C3.7 13.5 3 12.8 3 12z" />
    <path d="M18 2.5Q18.5 5 21 5.5Q18.5 6 18 8.5Q17.5 6 15 5.5Q17.5 5 18 2.5Z" fill="currentColor" stroke="none" />
  </Line>
)

export const Hourglass = (p: IconProps) => (
  <Line {...p}>
    <path d="M6 3h12M6 21h12M7.5 3c0 4.5 4.5 5.5 4.5 9s-4.5 4.5-4.5 9M16.5 3c0 4.5-4.5 5.5-4.5 9s4.5 4.5 4.5 9" />
    <path d="M9.5 19h5" strokeWidth="2" />
    <circle cx="12" cy="15.5" r="0.7" fill="currentColor" stroke="none" />
  </Line>
)

export const Grimoire = (p: IconProps) => (
  <Line {...p}>
    <path d="M3.5 5.5c2.9-1 5.9-.9 8.5 1 2.6-1.9 5.6-2 8.5-1v13c-2.9-1-5.9-.9-8.5 1-2.6-1.9-5.6-2-8.5-1z" />
    <path d="M12 6.5v12.5" />
    <path d="M7.5 9.5Q7.9 11.1 9.5 11.5Q7.9 11.9 7.5 13.5Q7.1 11.9 5.5 11.5Q7.1 11.1 7.5 9.5Z" fill="currentColor" stroke="none" />
  </Line>
)

export const Eye = (p: IconProps) => (
  <Line {...p}>
    <path d="M2.5 13c2.6-4.2 6-6.3 9.5-6.3s6.9 2.1 9.5 6.3c-2.6 4.2-6 6.3-9.5 6.3S5.1 17.2 2.5 13z" />
    <circle cx="12" cy="13" r="3" />
    <circle cx="12" cy="13" r="1" fill="currentColor" stroke="none" />
    <path d="M12 2.5V4M7 3.6l.8 1.3M17 3.6l-.8 1.3" />
  </Line>
)

export const Sun = (p: IconProps) => (
  <Line {...p}>
    <circle cx="12" cy="12" r="4" />
    <path d="M12 2.5V5M12 19v2.5M2.5 12H5M19 12h2.5M5.3 5.3l1.8 1.8M16.9 16.9l1.8 1.8M5.3 18.7l1.8-1.8M16.9 7.1l1.8-1.8" />
  </Line>
)

export const Folder = (p: IconProps) => (
  <Line size={18} {...p}>
    <path d="M3 7.5V18a1.5 1.5 0 0 0 1.5 1.5h15A1.5 1.5 0 0 0 21 18V9a1.5 1.5 0 0 0-1.5-1.5H12L10 5H4.5A1.5 1.5 0 0 0 3 6.5z" />
  </Line>
)

export const ArrowLeft = (p: IconProps) => (
  <Line size={16} width={1.6} {...p}>
    <path d="M20 12H5M11 6l-6 6 6 6" />
  </Line>
)

export const Chevron = (p: IconProps) => (
  <Line size={14} width={2} {...p}>
    <path d="M6 9l6 6 6-6" />
  </Line>
)

export const Search = (p: IconProps) => (
  <Line size={18} width={1.5} {...p}>
    <circle cx="10.5" cy="10.5" r="6.5" />
    <path d="M15.5 15.5L21 21" />
    <path d="M10.5 7Q11 9.9 13.5 10.5Q11 11 10.5 13.5Q10 11 7.5 10.5Q10 9.9 10.5 7Z" fill="currentColor" stroke="none" />
  </Line>
)

/** A sleeping moon: the engine at rest. */
export const PauseMoon = (p: IconProps) => (
  <Line size={18} width={1.5} {...p}>
    <path d="M15 3.5a8.5 8.5 0 1 0 5.5 13a7 7 0 0 1 -5.5 -13z" />
    <path d="M9 9.5v5M12.5 9.5v5" />
  </Line>
)

export const Alert = (p: IconProps) => (
  <Line size={18} width={1.5} {...p}>
    <circle cx="12" cy="12" r="9" />
    <path d="M12 7.5v5.5" />
    <circle cx="12" cy="16.5" r="0.6" fill="currentColor" />
  </Line>
)

export const More = (p: IconProps) => (
  <Line size={18} {...p}>
    <circle cx="5" cy="12" r="1.4" fill="currentColor" stroke="none" />
    <circle cx="12" cy="12" r="1.4" fill="currentColor" stroke="none" />
    <circle cx="19" cy="12" r="1.4" fill="currentColor" stroke="none" />
  </Line>
)

export function CornerOrnament({ className }: { className?: string }) {
  return (
    <svg aria-hidden className={`corner ${className ?? ''}`} width="26" height="26" viewBox="0 0 26 26">
      <path d="M1 25V9Q1 1 9 1H25" fill="none" stroke="var(--gold-dim)" />
      <path d="M8 4Q8.7 7.3 12 8Q8.7 8.7 8 12Q7.3 8.7 4 8Q7.3 7.3 8 4Z" fill="var(--accent)" />
    </svg>
  )
}

/** Four corner flourishes; the parent must be positioned. */
export function Corners() {
  return (
    <>
      <CornerOrnament className="tl" />
      <CornerOrnament className="tr" />
      <CornerOrnament className="bl" />
      <CornerOrnament className="br" />
    </>
  )
}

const skyDots: [number, number, number, number][] = [
  [18, 92, 1.1, 0.5], [64, 140, 0.7, 0.35], [201, 66, 1.2, 0.55], [150, 210, 0.8, 0.3], [30, 300, 0.9, 0.4],
  [210, 330, 0.7, 0.35], [110, 410, 1, 0.4], [56, 455, 0.6, 0.3], [186, 470, 1.1, 0.45], [20, 560, 0.8, 0.35],
  [212, 560, 1, 0.4], [80, 690, 0.6, 0.3], [170, 740, 0.9, 0.4], [96, 30, 0.7, 0.35], [170, 24, 0.9, 0.4],
  [8, 420, 0.7, 0.3], [224, 420, 0.8, 0.35], [100, 260, 0.6, 0.3], [196, 160, 0.7, 0.35],
]
const constellation: [number, number][] = [[56, 612], [92, 586], [120, 620], [152, 598], [180, 628], [204, 606]]

/** The sidebar's night sky: scattered stars and one constellation, drawn once. */
export function SidebarSky() {
  return (
    <svg aria-hidden className="sky" viewBox="0 0 232 860" preserveAspectRatio="xMidYMin slice">
      <g fill="var(--accent-hi)">
        {skyDots.map(([x, y, r, o]) => (
          <circle key={`${x},${y}`} cx={x} cy={y} r={r} opacity={o} />
        ))}
        <path opacity="0.55" d="M196 104Q196.9 108.1 201 109Q196.9 109.9 196 114Q195.1 109.9 191 109Q195.1 108.1 196 104Z" />
        <path opacity="0.4" d="M30 500Q30.7 503.3 34 504Q30.7 504.7 30 508Q29.3 504.7 26 504Q29.3 503.3 30 500Z" />
      </g>
      <path d={`M${constellation.map(([x, y]) => `${x} ${y}`).join('L')}`} fill="none" stroke="var(--accent)" strokeOpacity="0.28" strokeWidth="0.8" />
      <g fill="var(--accent-hi)" opacity="0.6">
        {constellation.map(([x, y], i) => (
          <circle key={x} cx={x} cy={y} r={i % 2 ? 2 : 1.5} />
        ))}
      </g>
    </svg>
  )
}

/** New, waxing, quarter, full and waning moons: the sidebar's footer ornament. */
export function MoonPhases() {
  return (
    <svg aria-hidden className="moon-phases" width="148" height="16" viewBox="0 0 148 16" fill="none" stroke="currentColor">
      <path d="M2 8H14M134 8H146" strokeOpacity="0.6" />
      <circle cx="30" cy="8" r="5" />
      <circle cx="52" cy="8" r="5" />
      <path d="M52 3a5 5 0 0 1 0 10a2.5 5 0 0 0 0 -10z" fill="currentColor" />
      <circle cx="74" cy="8" r="5" fill="var(--accent)" stroke="var(--accent)" />
      <circle cx="96" cy="8" r="5" />
      <path d="M96 3a5 5 0 0 0 0 10z" fill="currentColor" />
      <circle cx="118" cy="8" r="5" />
    </svg>
  )
}

/** The drop zone's sigil: a turning ring of ticks around a crescent with a rising arrow. */
export function DropSigil() {
  return (
    <span className="sigil" aria-hidden>
      <svg className="sigil-ring" viewBox="0 0 200 200" fill="none" stroke="var(--accent)" strokeLinecap="round">
        <circle cx="100" cy="100" r="92" strokeOpacity="0.45" />
        <circle cx="100" cy="100" r="80" strokeDasharray="1 7" strokeWidth="1.6" />
        <circle cx="100" cy="100" r="62" strokeOpacity="0.35" />
        <path
          strokeOpacity="0.6"
          d="M100 20V8M140 30.7L146 20.3M169.3 60L179.7 54M180 100H192M169.3 140L179.7 146M140 169.3L146 179.7M100 180V192M60 169.3L54 179.7M30.7 140L20.3 146M20 100H8M30.7 60L20.3 54M60 30.7L54 20.3"
        />
        <path
          stroke="none"
          fill="var(--accent)"
          d="M165 28Q166.2 33.8 172 35Q166.2 36.2 165 42Q163.8 36.2 158 35Q163.8 33.8 165 28ZM165 158Q166.2 163.8 172 165Q166.2 166.2 165 172Q163.8 166.2 158 165Q163.8 163.8 165 158ZM35 158Q36.2 163.8 42 165Q36.2 166.2 35 172Q33.8 166.2 28 165Q33.8 163.8 35 158ZM35 28Q36.2 33.8 42 35Q36.2 36.2 35 42Q33.8 36.2 28 35Q33.8 33.8 35 28Z"
        />
      </svg>
      <svg className="sigil-core" viewBox="0 0 200 200" fill="none">
        <path d="M62 104a38 38 0 0 0 76 0a38 30 0 0 1 -76 0z" fill="var(--accent)" fillOpacity="0.9" />
        <path d="M100 126V56M84 72L100 56L116 72" stroke="#ecd3a4" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round" />
        <path
          fill="#ecd3a4"
          d="M128 61Q128.9 65.1 133 66Q128.9 66.9 128 71Q127.1 66.9 123 66Q127.1 65.1 128 61ZM72 77Q72.6 79.4 75 80Q72.6 80.6 72 83Q71.4 80.6 69 80Q71.4 79.4 72 77Z"
        />
      </svg>
    </span>
  )
}

/** Stands in for an input image or a thumbnail that is not there yet. */
export const ImageGlyph = (p: IconProps) => (
  <Line size={46} view={48} {...p}>
    <rect x="6" y="8" width="36" height="32" rx="3" />
    <path d="M6 34l10-10 8 8 6-6 12 12" />
    <path d="M33 13a5 5 0 1 0 4 8a4.5 4.5 0 0 1 -4 -8z" />
  </Line>
)

const Emblem = ({ children, size = 40 }: IconProps & { children: ReactNode }) => (
  <Line size={size} view={72} width={2.4}>
    {children}
  </Line>
)

const Quill = () => (
  <Emblem>
    <path d="M58 10C40 12 25 25 20 48L14 60" />
    <path d="M58 10C56 26 44 42 20 48" />
    <path d="M28 38L40 35M24 44L34 43M34 30L45 26M41 22L50 18" />
    <path stroke="none" fill="currentColor" d="M16 15Q16.8 19.2 21 20Q16.8 20.8 16 25Q15.2 20.8 11 20Q15.2 19.2 16 15Z" />
  </Emblem>
)

const Flask = () => (
  <Emblem>
    <path d="M30 10h12M32 10v13L19 45a9 9 0 0 0 8 13h18a9 9 0 0 0 8-13L40 23V10" />
    <path d="M23 41h26" />
    <circle cx="33" cy="49" r="2.2" />
    <circle cx="41" cy="45" r="1.4" />
    <path stroke="none" fill="currentColor" d="M54 11Q54.8 15.2 59 16Q54.8 16.8 54 21Q53.2 16.8 49 16Q53.2 15.2 54 11Z" />
  </Emblem>
)

const Peaks = () => (
  <Emblem>
    <path d="M8 58L26 32L36 44L46 30L64 58Z" />
    <path d="M22 38L26 32L30 37M42 35L46 30L50 36" />
    <path d="M56 8a9 9 0 1 0 6 14a8 8 0 0 1 -6 -14z" />
    <circle cx="16" cy="18" r="1.2" fill="currentColor" stroke="none" />
    <circle cx="30" cy="12" r="1" fill="currentColor" stroke="none" />
  </Emblem>
)

const Sword = () => (
  <Emblem>
    <path d="M36 6L40 12V46H32V12Z" />
    <path d="M36 15V42" strokeOpacity="0.5" />
    <path d="M24 46H48M36 46V58" />
    <circle cx="36" cy="62" r="3" />
    <path stroke="none" fill="currentColor" d="M20 13Q20.8 17.2 25 18Q20.8 18.8 20 23Q19.2 18.8 15 18Q19.2 17.2 20 13ZM54 24.5Q54.6 27.4 57.5 28Q54.6 28.6 54 31.5Q53.4 28.6 50.5 28Q53.4 27.4 54 24.5Z" />
  </Emblem>
)

/** A twenty-sided die: the generic sign for "a 3D asset". */
export const D20 = ({ size = 40 }: IconProps) => (
  <Line size={size} view={100} width={size > 60 ? 1.1 : 2.4}>
    <path d="M50 6L88 28V72L50 94L12 72V28Z" />
    <path d="M50 24L76 66H24Z" />
    <path d="M50 24V6M50 24L12 28M50 24L88 28M76 66L88 28M76 66L88 72M76 66L50 94M24 66L12 28M24 66L12 72M24 66L50 94" strokeOpacity="0.55" />
  </Line>
)

const presetEmblems: Record<string, () => ReactNode> = {
  draft: Quill,
  'mobile-prop': Flask,
  'environment-piece': Peaks,
  'hero-character': Sword,
}

export function PresetEmblem({ id }: { id: string }) {
  const E = presetEmblems[id]
  return E ? <E /> : <D20 />
}

export const Check = (p: IconProps) => (
  <Line size={14} width={2.2} {...p}>
    <path d="M5 12.5l4.5 4.5L19 7.5" />
  </Line>
)

export const Trash = (p: IconProps) => (
  <Line size={17} width={1.5} {...p}>
    <path d="M4 6.5h16M9.5 6.5V4.5h5v2M6.5 6.5l1 13h9l1-13M10.5 10.5v6M13.5 10.5v6" />
  </Line>
)

/** An arrow leaving a tray: files written out of the library. */
export const ExportArrow = (p: IconProps) => (
  <Line size={17} width={1.5} {...p}>
    <path d="M12 15V3.5M7.5 8L12 3.5 16.5 8M4.5 13.5v5a1.5 1.5 0 0 0 1.5 1.5h12a1.5 1.5 0 0 0 1.5-1.5v-5" />
  </Line>
)

/** A cube on its corner: a 3D file opened in another program. */
export const Cube = (p: IconProps) => (
  <Line size={17} width={1.5} {...p}>
    <path d="M12 3l8 4.5v9L12 21l-8-4.5v-9z" />
    <path d="M4 7.5l8 4.5 8-4.5M12 12v9" />
  </Line>
)

/** Six dots: a handle to drag something out by. */
export const Grip = (p: IconProps) => (
  <Line size={16} {...p}>
    {[8, 12, 16].map((y) => (
      <g key={y}>
        <circle cx="9.5" cy={y} r="1.2" fill="currentColor" stroke="none" />
        <circle cx="14.5" cy={y} r="1.2" fill="currentColor" stroke="none" />
      </g>
    ))}
  </Line>
)
