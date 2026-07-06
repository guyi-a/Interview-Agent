import { useEffect, useRef } from "react";
import { cn } from "@/lib/utils";
import { localFileURL } from "@/lib/electron-api";

// Interactive image tile used in composer + transcript chip strips.
// Renders a 64x64 preview; click opens a native <dialog> at max viewport
// size so the user can actually see the picture. Native <dialog> gives us
// escape-to-close, backdrop, and focus trap for free — no portal /
// framework dialog needed.
export function ImageTile({
  path,
  name,
  removable,
  onRemove,
}: {
  path: string;
  name: string;
  removable?: boolean;
  onRemove?: () => void;
}) {
  const dialogRef = useRef<HTMLDialogElement>(null);

  const src = localFileURL(path);

  const open = () => dialogRef.current?.showModal();
  const close = () => dialogRef.current?.close();

  // Close on backdrop click. showModal() gives us native focus + esc, but
  // the default click-outside-to-close only kicks in if we detect a
  // click on the dialog element itself (which is the backdrop area since
  // the inner content stops propagation).
  useEffect(() => {
    const el = dialogRef.current;
    if (!el) return;
    const onClick = (e: MouseEvent) => {
      if (e.target === el) el.close();
    };
    el.addEventListener("click", onClick);
    return () => el.removeEventListener("click", onClick);
  }, []);

  return (
    <>
      <div className="group relative">
        <button
          type="button"
          onClick={open}
          title={path}
          aria-label={`预览 ${name}`}
          className={cn(
            "block size-24 overflow-hidden rounded-lg border border-rule/70",
            "bg-subtle/50 transition-shadow",
            "hover:shadow-[0_2px_8px_rgba(20,30,50,0.08)]",
          )}
        >
          <img
            src={src}
            alt={name}
            className="size-full object-cover"
            // If the file has gone missing (moved / deleted), the img just
            // renders empty on top of the subtle bg — good enough, no need
            // for a fallback path here since the tile is still identifiable
            // by the tooltip / remove-button.
          />
        </button>
        {removable && onRemove && (
          <button
            type="button"
            aria-label={`移除 ${name}`}
            onClick={onRemove}
            className={cn(
              "absolute -right-1.5 -top-1.5 inline-flex size-5 items-center justify-center",
              "rounded-full border border-rule bg-paper text-muted shadow-sm",
              "opacity-0 transition-opacity group-hover:opacity-100",
              "hover:text-ink",
            )}
          >
            <XIcon />
          </button>
        )}
      </div>

      <dialog
        ref={dialogRef}
        className={cn(
          "m-auto max-w-[95vw] max-h-[95vh] w-fit h-fit",
          "rounded-xl bg-transparent p-0",
          "backdrop:bg-ink/60",
        )}
      >
        <div className="flex flex-col items-center gap-2 p-4">
          <img
            src={src}
            alt={name}
            className="max-h-[85vh] max-w-[85vw] rounded-lg object-contain"
          />
          <div className="flex items-center gap-3 text-xs text-paper/85">
            <span className="truncate max-w-[60vw]">{name}</span>
            <button
              type="button"
              onClick={close}
              className="rounded border border-paper/30 px-2 py-0.5 text-paper hover:bg-paper/10"
            >
              关闭 (Esc)
            </button>
          </div>
        </div>
      </dialog>
    </>
  );
}

function XIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"
      strokeLinecap="round" strokeLinejoin="round"
      className="size-3" aria-hidden>
      <path d="M18 6 6 18" />
      <path d="m6 6 12 12" />
    </svg>
  );
}
