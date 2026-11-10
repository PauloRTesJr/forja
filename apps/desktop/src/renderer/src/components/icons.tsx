// Every icon the app draws, under the name of what it means here. They are drawn for Forja on
// one construction: a 24 grid, one stroke weight, square caps, mitred joins, corners cut at 45
// degrees instead of rounded. Screens import from this file only, so an icon changes in one
// place. All are decoration: the control that holds one carries the label.
import type { CSSProperties, ReactNode, SVGProps } from 'react'

export type IconProps = Omit<SVGProps<SVGSVGElement>, 'ref'> & { size?: number }
export type IconType = (props: IconProps) => React.JSX.Element

/** 16px, 1.75 stroke, hidden from assistive technology unless the caller says otherwise. */
function icon(body: ReactNode): IconType {
  return function Icon({ size = 16, ...props }) {
    return (
      <svg
        width={size}
        height={size}
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth={1.75}
        strokeLinecap="square"
        strokeLinejoin="miter"
        aria-hidden
        {...props}
      >
        {body}
      </svg>
    )
  }
}

// The one filled detail of an icon. It is cold until an ancestor sets `--icon-hot`: the current
// nav row and the status line while the forge works do (styles/shell.css).
const hot: CSSProperties = { fill: 'var(--icon-hot, var(--text-3))', stroke: 'none' }
const Hot = ({ d }: { d: string }): React.JSX.Element => <path d={d} style={hot} />
const Solid = ({ d }: { d: string }): React.JSX.Element => <path d={d} fill="currentColor" stroke="none" />

const ANVIL = (
  <>
    <path d="M2 9.5H21V12L16 13.5V16H9V13.5L5 12.5Z" />
    <path d="M9 16l-2 4h11l-2-4" />
  </>
)
const SHEET = 'M4 4.5h12.5L20 8v11.5H4z'
const LENS = 'M8 3.5h5L16.5 7v5L13 15.5H8L4.5 12V7zM15.5 15.5l5 5'
const OCTAGON = 'M8.5 3h7L21 8.5v7L15.5 21h-7L3 15.5v-7z'
/** An open eight-sided loop with an arrowhead, turning clockwise. */
const LOOP = 'M18.5 9L16 5H8L4.5 9v6L8 19h8l3.5-4M19 4.5V9h-4.5'

// Navigation
/** An anvil and its spark. */
export const GenerateIcon = icon(
  <>
    {ANVIL}
    <Hot d="M12 2l1.5 2.75L12 7.5l-1.5-2.75z" />
  </>,
)
/** Ingots waiting their turn. */
export const QueueIcon = icon(
  <>
    <path d="M3 18.5l1.5-5.5H10l1.5 5.5z" />
    <path d="M12.5 18.5L14 13h5.5l1.5 5.5z" />
    <Hot d="M7.75 12l1.5-5.5h5.5l1.5 5.5z" />
  </>,
)
/** The chest the finished work is kept in. */
export const LibraryIcon = icon(
  <>
    <path d="M3.5 10.5h17V19h-17z" />
    <path d="M3.5 10.5l2-5.5h13l2 5.5" />
    <Hot d="M12 8.5l1.75 2L12 13l-1.75-2.5z" />
  </>,
)
/** A tome: what the forge knows how to make. */
export const ModelsIcon = icon(
  <>
    <path d="M5 3.5h12l2 2v15H5z" />
    <path d="M8 3.5v17" />
    <Hot d="M13.5 9l2 3-2 3-2-3z" />
  </>,
)
export const SettingsIcon = icon(
  <>
    <path d={OCTAGON} />
    <Hot d="M12 8.5l3.5 3.5-3.5 3.5L8.5 12z" />
  </>,
)

// The phases of a job, in the order they run.
/** Raw ore: the image is cut out and framed. */
export const PreparePhaseIcon = icon(<path d="M12 3l6.5 5.5L12 21 5.5 8.5zM5.5 8.5h13" />)
export const ShapePhaseIcon = icon(<path d="M5 4h11l3 3v3H5zM10.5 10h3v10.5h-3z" />)
export const PaintPhaseIcon = icon(<path d="M20 4l-8.5 8.5M9 10.5l4.5 4.5-3 4H5v-5.5z" />)
export const RigPhaseIcon = icon(
  <>
    <path d="M6 4v5.5l6 4.5 6-4.5V4M12 15.5v5" />
    <Solid d="M12 12l1.75 2L12 16l-1.75-2z" />
  </>,
)
/** A cut gem: the asset is finished and exported. */
export const FinishPhaseIcon = icon(<path d="M7.5 4h9L21 9 12 20.5 3 9zM3 9h18M9 9l3 11.5L15 9" />)

// Actions
export const UploadIcon = icon(<path d={`${SHEET}M12 9v6M9 12h6`} />)
export const SearchIcon = icon(<path d={LENS} />)
export const MoreIcon = icon(<Solid d="M5 10l2 2-2 2-2-2zM12 10l2 2-2 2-2-2zM19 10l2 2-2 2-2-2z" />)
export const DeleteIcon = icon(<path d="M4.5 7h15M9 7V4h6v3M6.5 7l1 13.5h9l1-13.5" />)
export const ExportIcon = icon(<path d="M12 3v11.5M7.5 10l4.5 4.5 4.5-4.5M4 16.5v4h16v-4" />)
export const BackIcon = icon(<path d="M20 12H5M10.5 6L4.5 12l6 6" />)
export const DragHandleIcon = icon(
  <Solid d="M9 5l1.5 1.5L9 8 7.5 6.5zM15 5l1.5 1.5L15 8l-1.5-1.5zM9 10.5l1.5 1.5L9 13.5 7.5 12zM15 10.5l1.5 1.5-1.5 1.5-1.5-1.5zM9 16l1.5 1.5L9 19l-1.5-1.5zM15 16l1.5 1.5L15 19l-1.5-1.5z" />,
)
export const PauseIcon = icon(<path d="M8 5.5v13M16 5.5v13" />)
export const OpenExternalIcon = icon(<path d="M13 4h7v7M20 4l-9 9M17 14.5V20H4V7h5.5" />)
export const FolderIcon = icon(<path d="M3.5 6h6l2 2.5h9V19h-17z" />)
/** Back to the default value. */
export const ResetIcon = icon(<path d="M5.5 9L8 5h8l3.5 4v6L16 19H8l-3.5-4M5 4.5V9h4.5" />)
export const CopyIcon = icon(<path d="M9 8h8l3 3v10H9zM6 16H4V3h9l2 2" />)
export const AddIcon = icon(<path d="M12 5v14M5 12h14" />)
/** A 3D file opened in another program. */
export const Open3DIcon = icon(<path d="M12 3l8 4.5v9L12 21l-8-4.5v-9zM4 7.5l8 4.5 8-4.5M12 12v9" />)
export const CloseIcon = icon(<path d="M6 6l12 12M18 6L6 18" />)
export const ChevronIcon = icon(<path d="M6 9l6 6 6-6" />)
export const RenameIcon = icon(<path d="M4 20l1-4.5L16 4.5 19.5 8l-11 11zM13.5 7l3.5 3.5" />)
/** Make something again from its source, such as a thumbnail. */
export const RefreshIcon = icon(<path d={LOOP} />)

// The 3D viewer
export const TurntableIcon = icon(
  <>
    <path d={LOOP} />
    <Solid d="M12 9.5l2.5 2.5-2.5 2.5L9.5 12z" />
  </>,
)
export const SkeletonIcon = RigPhaseIcon
export const BendTestIcon = icon(
  <>
    <Solid d="M12 2.5l2 2-2 2-2-2z" />
    <path d="M12 8.5v6.5M7 11h10M12 15l-3.5 6M12 15l3.5 6" />
  </>,
)
/** Frame the model again. */
export const ResetViewIcon = icon(<path d="M4 9V4h5M15 4h5v5M20 15v5h-5M9 20H4v-5" />)

// States
/** The forge at work: lit while something generates or installs, cold otherwise. */
export const ForgingIcon = icon(<Hot d="M12 2.5l4.5 6.5 1.5 5-2.5 5.5h-7L6 14l1.5-4.5 2 2z" />)
export const AlertIcon = icon(
  <>
    <path d="M12 3.5l9.5 17h-19zM12 10v4.5" />
    <Solid d="M12 16l1 1-1 1-1-1z" />
  </>,
)
export const CheckIcon = icon(<path d="M4.5 12.5l5 5L19.5 7" />)
/** A value set somewhere the app cannot change. */
export const LockedIcon = icon(<path d="M6 11h12v9.5H6zM8.5 11V7l2-3h3l2 3v4" />)
/** A change that waits for running work to end. */
export const WaitingIcon = icon(<path d="M6 3h12M6 21h12M7.5 3v3.5L12 12l4.5-5.5V3M7.5 21v-3.5L12 12l4.5 5.5V21" />)
export const SkippedIcon = icon(<path d="M6 12h12" />)
export const HelpIcon = icon(
  <>
    <path d={`${OCTAGON}M9.5 9.5L12 7.5l2.5 2-2.5 3v1`} />
    <Solid d="M12 15.5l1 1-1 1-1-1z" />
  </>,
)
export const NoResultsIcon = icon(<path d={`${LENS}M8.5 7.5l4 4M12.5 7.5l-4 4`} />)
/** A cold anvil: nothing is being made. */
export const NoJobsIcon = icon(ANVIL)
/** Stands in for an image or a thumbnail that is not there. */
export const ImageIcon = icon(<path d={`${SHEET}M4 16l5-5 4 4 2.5-2.5L20 17`} />)

// Traced from the app icon (build/icon.*): coordinates are the source artwork's pixels, the viewBox frames the emblem.
const EMBLEM = {
  ring: 'M997.3 524.7A376 376 0 0 0 692.3 219.7M561.7 219.7A376 376 0 0 0 256.7 524.7M256.7 655.3A376 376 0 0 0 997.3 655.3',
  anvil:
    'M306 563H958L900 617C830 645 738 642 722 740C722 790 772 826 799 830V868H748C720 823 540 823 507 868H458V832C497 820 540 800 552 770Q560 752 560 740C560 690 450 625 310 601L306 597Z',
  spark:
    'M627 130C628.5 300 636 392 690 398C655 405 635 440 635 470V538Q635 559 642 563H612Q619 559 619 538V470C619 440 599 405 564 398C618 392 625.5 300 627 130Z',
}

/** The Forja emblem from the app icon: spark, anvil and broken ring, in the accent colour. */
export function BrandMark({ size = 24 }: { size?: number }) {
  return (
    <svg aria-hidden width={size} height={size} viewBox="197 125 860 860" fill="var(--accent)">
      {/* Heavier than in the artwork, so the ring still reads at sidebar size. */}
      <path d={EMBLEM.ring} fill="none" stroke="var(--accent)" strokeWidth="40" />
      <path d={EMBLEM.anvil} />
      <path d={EMBLEM.spark} fill="var(--accent-hi)" />
    </svg>
  )
}
