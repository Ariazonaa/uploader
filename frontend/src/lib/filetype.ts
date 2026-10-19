// Maps a MIME type to a category icon (from the lucide icon set), label key and
// accent color used for file-type coding in the dashboard. The label is returned
// as an i18n message key (labelKey) so callers translate it in the active
// language; this keeps the helper free of any React/runtime i18n dependency.
import {
  Archive,
  FileText,
  File as FileIcon,
  FileType,
  Image as ImageIcon,
  Music,
  Video,
  type LucideIcon,
} from "lucide-react";
import type { MessageKey } from "./i18n/types";

export interface FileTypeInfo {
  icon: LucideIcon;
  labelKey: MessageKey;
  color: string;
}

export function fileType(mime: string): FileTypeInfo {
  const m = mime.toLowerCase();
  if (m.startsWith("image/")) return { icon: ImageIcon, labelKey: "filetype.image", color: "#3b82f6" };
  if (m.startsWith("video/")) return { icon: Video, labelKey: "filetype.video", color: "#a855f7" };
  if (m.startsWith("audio/")) return { icon: Music, labelKey: "filetype.audio", color: "#ec4899" };
  if (m === "application/pdf") return { icon: FileType, labelKey: "filetype.pdf", color: "#ef4444" };
  if (
    m.startsWith("text/") ||
    m.includes("json") ||
    m.includes("xml") ||
    m.includes("javascript") ||
    m.includes("typescript")
  )
    return { icon: FileText, labelKey: "filetype.textCode", color: "#22c55e" };
  if (
    m.includes("zip") ||
    m.includes("tar") ||
    m.includes("rar") ||
    m.includes("7z") ||
    m.includes("gzip") ||
    m.includes("compress")
  )
    return { icon: Archive, labelKey: "filetype.archive", color: "#eab308" };
  return { icon: FileIcon, labelKey: "filetype.file", color: "#6b7280" };
}
