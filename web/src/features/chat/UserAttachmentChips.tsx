import { cn } from "@/lib/utils";
import type { ParsedAttachment } from "@/features/chat/attachments-store";
import { ImageTile } from "@/features/chat/ImageTile";

// Read-only chip strip rendered inside a delivered user message bubble.
// Images use the same ImageTile as the composer (click-to-enlarge) so
// the user can still see what they sent after the message left. Files
// and folders stay as compact chips.
export function UserAttachmentChips({
  attachments,
}: {
  attachments: ParsedAttachment[];
}) {
  if (attachments.length === 0) return null;
  return (
    <div className="mb-2 flex flex-wrap items-center gap-2">
      {attachments.map((a, i) => {
        if (a.kind === "image") {
          return (
            <ImageTile key={`${a.path}-${i}`} path={a.path} name={a.name} />
          );
        }
        return (
          <FileChip
            key={`${a.path}-${i}`}
            name={a.name}
            path={a.path}
            isDirectory={a.kind === "folder"}
          />
        );
      })}
    </div>
  );
}

function FileChip({
  name,
  path,
  isDirectory,
}: {
  name: string;
  path: string;
  isDirectory: boolean;
}) {
  return (
    <div
      title={path}
      className={cn(
        "inline-flex h-7 items-center gap-1.5 rounded-md",
        "border border-rule/70 bg-paper/80 px-2 text-xs text-ink",
      )}
    >
      {isDirectory ? <FolderIcon /> : <FileIcon />}
      <span className="max-w-[280px] truncate">{name}</span>
    </div>
  );
}

function FileIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round"
      className="size-3.5 shrink-0 text-muted" aria-hidden>
      <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" />
      <polyline points="14 2 14 8 20 8" />
    </svg>
  );
}

function FolderIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round"
      className="size-3.5 shrink-0 text-muted" aria-hidden>
      <path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2z" />
    </svg>
  );
}
