import { create } from "zustand";
import { isImagePath, type PickedLocalFile } from "@/lib/electron-api";

// Push the bytes of one or more freshly-produced File objects (paste,
// drag-drop) through the Electron IPC bridge so they land on disk and can
// flow through the exact same [image: /abs/path] pipeline as files picked
// with the "+" button. Anything that fails to save is skipped with a log
// warning — the user still gets whatever did succeed.
export async function saveImageFiles(
  convID: string,
  files: File[],
  savePastedImage: (
    bytes: Uint8Array,
    mimeType: string,
    suggestedName?: string,
  ) => Promise<{ path: string; name: string }>,
  add: (convID: string, items: PickedLocalFile[]) => void,
): Promise<void> {
  const saved: PickedLocalFile[] = [];
  for (const f of files) {
    if (!f.type.startsWith("image/")) continue;
    try {
      const bytes = new Uint8Array(await f.arrayBuffer());
      const result = await savePastedImage(bytes, f.type, f.name || undefined);
      saved.push({ path: result.path, name: result.name, isDirectory: false });
    } catch (err) {
      console.error("[attach] savePastedImage failed for", f.name, err);
    }
  }
  if (saved.length > 0) add(convID, saved);
}

// One attachment in the composer. `id` is a client-generated key for React
// list rendering and the remove-by-id call path; `path` is the actual
// absolute path we ship to the model in [file:] / [folder:] markers.
export interface AttachedFile extends PickedLocalFile {
  id: string;
}

// Attachments store, keyed by conversation id so switching conversations
// keeps each one's pending pile isolated. In-memory only — a page refresh
// drops unsent attachments (which matches expectations: they never left
// the user's disk, so nothing to recover). Marker text is what persists;
// see conversation.tsx.
interface AttachmentsStore {
  pending: Record<string, AttachedFile[]>;
  add: (convID: string, files: PickedLocalFile[]) => void;
  remove: (convID: string, id: string) => void;
  clear: (convID: string) => void;
}

let seq = 0;
const nextId = () => `att-${Date.now().toString(36)}-${(seq++).toString(36)}`;

export const useAttachmentsStore = create<AttachmentsStore>((set, get) => ({
  pending: {},

  add: (convID, files) => {
    if (files.length === 0) return;
    const current = get().pending[convID] ?? [];
    // Dedupe by path so double-clicking the file picker or picking the same
    // thing twice doesn't stack chips.
    const seen = new Set(current.map((f) => f.path));
    const added: AttachedFile[] = [];
    for (const f of files) {
      if (seen.has(f.path)) continue;
      seen.add(f.path);
      added.push({ id: nextId(), ...f });
    }
    if (added.length === 0) return;
    set({ pending: { ...get().pending, [convID]: [...current, ...added] } });
  },

  remove: (convID, id) => {
    const current = get().pending[convID] ?? [];
    const next = current.filter((f) => f.id !== id);
    const map = { ...get().pending };
    if (next.length === 0) delete map[convID];
    else map[convID] = next;
    set({ pending: map });
  },

  clear: (convID) => {
    const map = { ...get().pending };
    delete map[convID];
    set({ pending: map });
  },
}));

// Marker text a message carries when it's sent with attachments. Prepended
// to the user's typed text so the model can see exactly which local paths
// were being referenced. Marker choice by kind:
//   - directory  → [folder: /abs] — model uses list_files
//   - image      → [image:  /abs] — backend expands into multi-part image
//                                   content (multimodal.BuildUserMessage)
//   - anything   → [file:   /abs] — model picks a reader tool
// Formats match krow-app so the model prompt (general.go) can carry one
// consistent instruction set.
export function serializeAttachments(files: AttachedFile[]): string {
  return files
    .map((f) => {
      if (f.isDirectory) return `[folder: ${f.path}]`;
      if (isImagePath(f.name)) return `[image: ${f.path}]`;
      return `[file: ${f.path}]`;
    })
    .join("\n");
}

// One attachment lifted out of a persisted user message's leading marker
// block. Same shape the composer chip renders, so the transcript can reuse
// the AttachmentChip visual for read-only display.
export interface ParsedAttachment {
  kind: "file" | "folder" | "image";
  path: string;
  name: string;
}

const MARKER_RE = /^\[(file|folder|image):\s*(.+)\]$/;

// parseAttachmentMarkers pulls consecutive [file:] / [folder:] lines off
// the top of a message and returns them plus whatever prose follows. Used
// by the transcript to render user attachments as chips instead of raw
// bracketed text. Only leading markers are consumed — anything in the
// middle of the message is preserved verbatim (a marker-shaped substring
// inside prose is not a real attachment).
export function parseAttachmentMarkers(content: string): {
  attachments: ParsedAttachment[];
  text: string;
} {
  const lines = content.split("\n");
  const attachments: ParsedAttachment[] = [];
  let i = 0;
  while (i < lines.length) {
    const m = MARKER_RE.exec(lines[i].trim());
    if (!m) break;
    const path = m[2];
    const name = path.split(/[/\\]/).pop() || path;
    attachments.push({ kind: m[1] as "file" | "folder" | "image", path, name });
    i++;
  }
  return {
    attachments,
    text: lines.slice(i).join("\n").replace(/^\n+/, ""),
  };
}
