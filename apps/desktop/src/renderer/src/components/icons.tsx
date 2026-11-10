// Every icon the app draws, under the name of what it means here. Screens import from this file,
// never from lucide-react, so an icon changes in one place. All are decoration: the control that
// holds one carries the label.
import {
  ArrowLeft,
  Bone,
  Box,
  Boxes,
  Check,
  ChevronDown,
  CircleQuestionMark,
  Clock,
  Copy,
  Download,
  Ellipsis,
  ExternalLink,
  FolderOpen,
  GripVertical,
  Image,
  ImagePlus,
  Inbox,
  LayoutGrid,
  ListChecks,
  Lock,
  Minus,
  Pause,
  Pencil,
  PersonStanding,
  Plus,
  RefreshCw,
  Rotate3d,
  RotateCcw,
  Scan,
  Search,
  SearchX,
  Settings,
  Sparkles,
  Trash,
  TriangleExclamationPoint,
  X,
  type LucideIcon,
  type LucideProps,
} from 'lucide-react'

export type IconProps = Omit<LucideProps, 'ref'>
export type IconType = (props: IconProps) => React.JSX.Element

/** 16px, 1.75 stroke, hidden from assistive technology unless the caller says otherwise. */
function icon(Glyph: LucideIcon): IconType {
  return function Icon(props) {
    return <Glyph size={16} strokeWidth={1.75} aria-hidden {...props} />
  }
}

// Navigation
export const GenerateIcon = icon(Sparkles)
export const QueueIcon = icon(ListChecks)
export const LibraryIcon = icon(LayoutGrid)
export const ModelsIcon = icon(Boxes)
export const SettingsIcon = icon(Settings)

// Actions
export const UploadIcon = icon(ImagePlus)
export const SearchIcon = icon(Search)
export const MoreIcon = icon(Ellipsis)
export const DeleteIcon = icon(Trash)
export const ExportIcon = icon(Download)
export const BackIcon = icon(ArrowLeft)
export const DragHandleIcon = icon(GripVertical)
export const PauseIcon = icon(Pause)
export const OpenExternalIcon = icon(ExternalLink)
export const FolderIcon = icon(FolderOpen)
/** Back to the default value. */
export const ResetIcon = icon(RotateCcw)
export const CopyIcon = icon(Copy)
export const AddIcon = icon(Plus)
/** A 3D file opened in another program. */
export const Open3DIcon = icon(Box)
export const CloseIcon = icon(X)
export const ChevronIcon = icon(ChevronDown)
export const RenameIcon = icon(Pencil)
/** Make something again from its source, such as a thumbnail. */
export const RefreshIcon = icon(RefreshCw)

// The 3D viewer
export const TurntableIcon = icon(Rotate3d)
export const SkeletonIcon = icon(Bone)
export const BendTestIcon = icon(PersonStanding)
/** Frame the model again. */
export const ResetViewIcon = icon(Scan)

// States
export const AlertIcon = icon(TriangleExclamationPoint)
export const CheckIcon = icon(Check)
/** A value set somewhere the app cannot change. */
export const LockedIcon = icon(Lock)
/** A change that waits for running work to end. */
export const WaitingIcon = icon(Clock)
export const SkippedIcon = icon(Minus)
export const HelpIcon = icon(CircleQuestionMark)
export const NoResultsIcon = icon(SearchX)
export const NoJobsIcon = icon(Inbox)
/** Stands in for an image or a thumbnail that is not there. */
export const ImageIcon = icon(Image)

// Traced from the app icon (build/icon.*): coordinates are the source artwork's pixels, the viewBox frames the emblem.
const EMBLEM = {
  ring: 'M997.3 524.7A376 376 0 0 0 692.3 219.7M561.7 219.7A376 376 0 0 0 256.7 524.7M256.7 655.3A376 376 0 0 0 997.3 655.3',
  anvil:
    'M306 563H958L900 617C830 645 738 642 722 740C722 790 772 826 799 830V868H748C720 823 540 823 507 868H458V832C497 820 540 800 552 770Q560 752 560 740C560 690 450 625 310 601L306 597Z',
  spark:
    'M627 130C628.5 300 636 392 690 398C655 405 635 440 635 470V538Q635 559 642 563H612Q619 559 619 538V470C619 440 599 405 564 398C618 392 625.5 300 627 130Z',
}

/** The Forja emblem from the app icon: spark, anvil and broken ring, in the accent colour. */
export function BrandMark({ size = 22 }: { size?: number }) {
  return (
    <svg aria-hidden width={size} height={size} viewBox="197 125 860 860" fill="var(--accent)">
      {/* Heavier than in the artwork, so the ring still reads at sidebar size. */}
      <path d={EMBLEM.ring} fill="none" stroke="var(--accent)" strokeWidth="40" />
      <path d={EMBLEM.anvil} />
      <path d={EMBLEM.spark} />
    </svg>
  )
}
