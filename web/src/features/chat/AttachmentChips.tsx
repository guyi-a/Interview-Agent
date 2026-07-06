import { cn } from "@/lib/utils";
import {
  useAttachmentsStore,
  type AttachedFile,
} from "@/features/chat/attachments-store";

// Stable empty array — avoids returning a fresh [] from the Zustand
// selector on every render, which would hang React in a max-update loop
// via useSyncExternalStore (same trick as approval-store).
const EMPTY: AttachedFile[] = [];

export function AttachmentChips({ conversationID }: { conversationID: string }) {
  const files = useAttachmentsStore(
    (s) => s.pending[conversationID] ?? EMPTY,
  );
  const remove = useAttachmentsStore((s) => s.remove);

  if (files.length === 0) return null;

  return (
    <div className="flex flex-wrap gap-1.5 border-b border-rule/60 px-3 py-2">
      {files.map((f) => (
        <div
          key={f.id}
          title={f.path}
          className={cn(
            "group inline-flex h-7 items-center gap-1.5 rounded-md",
            "border border-rule/70 bg-subtle/60 pl-1.5 pr-1 text-xs text-ink",
          )}
        >
          {f.isDirectory ? <FolderIcon /> : <FileIcon />}
          <span className="max-w-[220px] truncate">{f.name}</span>
          <button
            type="button"
            aria-label={`移除 ${f.name}`}
            onClick={() => remove(conversationID, f.id)}
            className={cn(
              "inline-flex size-4 items-center justify-center rounded",
              "text-muted transition-colors",
              "hover:bg-rule/70 hover:text-ink",
            )}
          >
            <XIcon />
          </button>
        </div>
      ))}
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

function XIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round"
      className="size-3" aria-hidden>
      <path d="M18 6 6 18" />
      <path d="m6 6 12 12" />
    </svg>
  );
}
