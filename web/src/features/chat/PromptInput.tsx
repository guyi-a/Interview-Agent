import {
  useEffect,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";
import { cn } from "@/lib/utils";

// A card-style composer: unadorned textarea sits inside a rounded border
// that tracks focus, with a small toolbar row at the bottom. Behaviour
// notes:
//   - Enter sends, Shift+Enter inserts a newline (browser default).
//   - ⌘/Ctrl+Enter also sends — matches long-standing chat habits.
//   - IME composition is respected: while the user is composing a Chinese/
//     Japanese/Korean word, Enter picks a candidate rather than submitting.
//   - Clicking anywhere in the card that isn't an interactive control
//     focuses the textarea, widening the hit target.
//   - Textarea auto-grows with content up to a max height, then scrolls.
//   - Streaming mode: textarea stays enabled but the send button becomes
//     a stop button (inverted colours + square glyph).
export function PromptInput({
  streaming,
  onSend,
  onCancel,
}: {
  streaming: boolean;
  onSend: (text: string) => void;
  onCancel: () => void;
}) {
  const [text, setText] = useState("");
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const cardRef = useRef<HTMLDivElement>(null);

  // Auto-resize: reset to `auto` first so shrinking works, then match
  // scrollHeight. The min-height / max-height are enforced by CSS below.
  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${el.scrollHeight}px`;
  }, [text]);

  const canSend = text.trim().length > 0 && !streaming;

  const submit = () => {
    const t = text.trim();
    if (!t || streaming) return;
    onSend(t);
    setText("");
    // Reset height explicitly — the effect will re-run on next render but
    // clearing here avoids a flash of the old height between paints.
    if (textareaRef.current) textareaRef.current.style.height = "auto";
  };

  const onKey = (e: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    // While an IME is composing (e.g. picking a Chinese candidate) Enter
    // must select the candidate, not submit the message.
    if (e.nativeEvent.isComposing) return;

    const isEnter = e.key === "Enter";
    if (!isEnter) return;

    // ⌘/Ctrl+Enter submits regardless of Shift (habit compatibility).
    if (e.metaKey || e.ctrlKey) {
      e.preventDefault();
      submit();
      return;
    }
    // Plain Enter submits; Shift+Enter falls through as a newline.
    if (!e.shiftKey) {
      e.preventDefault();
      submit();
    }
  };

  // Click empty card area → focus textarea. Buttons and the textarea
  // itself keep their native behaviour.
  const onCardMouseDown = (e: React.MouseEvent) => {
    const target = e.target as HTMLElement;
    if (target.closest("button, textarea, input, a, [role='button']")) return;
    e.preventDefault();
    textareaRef.current?.focus();
  };

  return (
    <div className="px-6 pb-5 pt-3 bg-paper">
      <div className="max-w-3xl mx-auto">
        <div
          ref={cardRef}
          onMouseDown={onCardMouseDown}
          className={cn(
            "cursor-text rounded-xl border border-rule bg-paper transition-shadow",
            "shadow-[0_1px_2px_rgba(20,30,50,0.03)]",
            "hover:shadow-[0_4px_16px_rgba(20,30,50,0.06)]",
            "focus-within:border-accent",
            "focus-within:shadow-[0_0_0_3px_oklch(0.36_0.10_245/0.12)]",
          )}
        >
          <textarea
            ref={textareaRef}
            rows={1}
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={onKey}
            placeholder={streaming ? "正在响应…" : "写点什么"}
            className={cn(
              "block w-full resize-none bg-transparent px-5 pt-4 pb-2",
              "text-[15px] leading-7 text-ink",
              "placeholder:italic placeholder:text-muted",
              "focus:outline-none",
            )}
            style={{ minHeight: "44px", maxHeight: "260px" }}
          />

          <div className="flex items-center justify-between gap-3 px-3 pb-2.5 pt-1">
            <div className="pl-2 font-mono text-[10px] uppercase tracking-[0.18em] text-muted">
              {streaming ? "响应中" : "Enter 发送 · Shift+Enter 换行"}
            </div>

            {streaming ? (
              <button
                type="button"
                onClick={onCancel}
                title="停止 (Esc)"
                aria-label="停止响应"
                className={cn(
                  "flex h-9 w-9 items-center justify-center rounded-lg",
                  "bg-ink text-paper transition-opacity",
                  "hover:opacity-85 cursor-pointer",
                )}
              >
                <StopIcon />
              </button>
            ) : (
              <button
                type="button"
                onClick={submit}
                disabled={!canSend}
                title="发送 (Enter)"
                aria-label="发送"
                className={cn(
                  "flex h-9 w-9 items-center justify-center rounded-lg transition-colors",
                  canSend
                    ? "bg-accent text-paper hover:bg-accent-hover cursor-pointer"
                    : "bg-subtle text-muted cursor-not-allowed",
                )}
              >
                <ArrowUpIcon />
              </button>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

// Two inline glyphs — we only need these two, so a full icon library is
// overkill. Paths borrowed from lucide (MIT).
function ArrowUpIcon() {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M12 19V5" />
      <path d="m5 12 7-7 7 7" />
    </svg>
  );
}

function StopIcon() {
  return (
    <svg
      width="12"
      height="12"
      viewBox="0 0 24 24"
      fill="currentColor"
      aria-hidden="true"
    >
      <rect x="5" y="5" width="14" height="14" rx="1.5" />
    </svg>
  );
}
