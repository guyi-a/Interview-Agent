import { useEffect, useMemo, useRef, useState } from "react";
import { cn } from "@/lib/utils";
import { useApprovalStore, type PendingApproval } from "@/features/chat/approval-store";

// Stable empty array — avoids a fresh `[]` from the selector on every render,
// which would give useSyncExternalStore a new reference each read and hang
// React in a maximum-update-depth loop.
const EMPTY: PendingApproval[] = [];

// Human-readable label for each tool we currently prompt on. Extend this map
// when adding a tool to policy.NeedsApproval.
const TOOL_TITLES: Record<string, string> = {
  write_file: "写入文件",
  edit_file: "修改文件",
  write_file_chunked: "写入长文件",
};

const REASON_MAX = 500;

// One-line summary of a tool call's arguments. Aims for signal density under
// ~80 chars: enough for the user to recognise the operation without unfolding
// the raw JSON.
function summarize(tool: string, argsJson: string): string {
  if (!argsJson) return "";
  let args: Record<string, unknown> = {};
  try {
    args = JSON.parse(argsJson) as Record<string, unknown>;
  } catch {
    return argsJson.length > 80 ? argsJson.slice(0, 80) + "…" : argsJson;
  }
  switch (tool) {
    case "write_file":
    case "edit_file":
      return typeof args.path === "string" ? args.path : "";
    case "write_file_chunked": {
      const path = typeof args.path === "string" ? args.path : "";
      const mode = typeof args.mode === "string" ? args.mode : "";
      return path && mode ? `${path} · ${mode}` : path || mode;
    }
    default: {
      // Best-effort fallback: any path-ish field
      for (const key of ["path", "target", "file", "name"]) {
        const v = args[key];
        if (typeof v === "string" && v) return v;
      }
      return "";
    }
  }
}

function toolTitle(tool: string): string {
  return TOOL_TITLES[tool] ?? tool;
}

export function ApprovalBar({
  conversationID,
  onResume,
}: {
  conversationID: string;
  onResume?: () => Promise<void> | void;
}) {
  const pending = useApprovalStore(
    (s) => s.pending[conversationID] ?? EMPTY,
  );
  const decide = useApprovalStore((s) => s.decide);
  const [busy, setBusy] = useState(false);
  const [mode, setMode] = useState<"idle" | "denying">("idle");
  const [reason, setReason] = useState("");
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  const current: PendingApproval | undefined = pending[0];
  const summary = useMemo(
    () => (current ? summarize(current.tool, current.argsJson) : ""),
    [current],
  );

  // Reset local state whenever the visible pending item changes — otherwise
  // a reason typed for tool A would leak into the prompt for tool B.
  useEffect(() => {
    setMode("idle");
    setReason("");
    setBusy(false);
  }, [current?.interruptId]);

  // Autofocus so the user can just start typing after clicking 拒绝.
  useEffect(() => {
    if (mode === "denying") textareaRef.current?.focus();
  }, [mode]);

  if (!current) return null;

  const submit = async (decision: "approve" | "deny", withReason?: string) => {
    if (busy) return;
    setBusy(true);
    try {
      await decide(conversationID, current.interruptId, decision, withReason);
      // Backend spun up a fresh run into a new SSE buffer; reconnect so the
      // continuation streams into the same conversation view.
      await onResume?.();
    } finally {
      setBusy(false);
    }
  };

  const onApprove = () => submit("approve");
  const onStartDeny = () => setMode("denying");
  const onCancelDeny = () => {
    setMode("idle");
    setReason("");
  };
  const onConfirmDeny = () => submit("deny", reason);

  return (
    <div className="absolute inset-0 z-10 flex items-end bg-paper px-6 pb-3 pt-2">
      <div className="mx-auto w-full max-w-3xl">
        <div
          className={cn(
            "rounded-xl border border-rule bg-paper px-4 py-3",
            "space-y-3",
            "shadow-[0_-8px_24px_-8px_rgba(0,0,0,0.14),0_-2px_6px_-2px_rgba(0,0,0,0.08)]",
          )}
        >
          <div className="flex items-center gap-2.5">
            <ShieldIcon />
            <span className="text-[15px] font-semibold text-ink">
              {toolTitle(current.tool)}
            </span>
            {pending.length > 1 && (
              <span className="rounded bg-subtle px-1.5 py-0.5 font-mono text-[10px] text-muted tabular-nums">
                1/{pending.length}
              </span>
            )}
          </div>

          {mode === "idle" ? (
            <>
              {summary && (
                <code className="block min-h-9 w-full overflow-hidden truncate rounded-lg bg-subtle/65 px-3 py-2 font-mono text-[12px] leading-5 text-muted">
                  {summary}
                </code>
              )}

              <div className="flex items-center justify-center gap-2.5">
                <button
                  type="button"
                  disabled={busy}
                  onClick={onStartDeny}
                  className={cn(
                    "inline-flex h-8 items-center gap-1.5 rounded-lg border border-rule bg-paper px-4 text-sm font-medium text-ink",
                    "shadow-[0_1px_2px_rgba(20,30,50,0.06)] transition-colors hover:bg-subtle",
                    busy && "pointer-events-none opacity-50",
                  )}
                >
                  <XIcon />
                  拒绝
                </button>
                <button
                  type="button"
                  disabled={busy}
                  onClick={onApprove}
                  className={cn(
                    "inline-flex h-8 items-center gap-1.5 rounded-lg bg-ink px-4 text-sm font-medium text-paper",
                    "shadow-[0_1px_2px_rgba(20,30,50,0.12)] transition-opacity hover:opacity-90",
                    busy && "pointer-events-none opacity-50",
                  )}
                >
                  <CheckIcon />
                  允许
                </button>
              </div>
            </>
          ) : (
            <>
              <textarea
                ref={textareaRef}
                value={reason}
                onChange={(e) => setReason(e.target.value.slice(0, REASON_MAX))}
                onKeyDown={(e) => {
                  if (e.key === "Escape") {
                    e.preventDefault();
                    onCancelDeny();
                  } else if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                    e.preventDefault();
                    onConfirmDeny();
                  }
                }}
                placeholder="告诉模型为什么不允许（可选，⌘/Ctrl+Enter 提交）"
                rows={2}
                className={cn(
                  "block w-full resize-none rounded-lg border border-rule bg-subtle/65 px-3 py-2",
                  "text-sm leading-5 text-ink placeholder:text-muted",
                  "focus:outline-none focus:ring-1 focus:ring-ink/20",
                )}
              />

              <div className="flex items-center justify-between gap-2.5">
                <span className="font-mono text-[10px] text-muted tabular-nums">
                  {reason.length}/{REASON_MAX}
                </span>
                <div className="flex items-center gap-2.5">
                  <button
                    type="button"
                    disabled={busy}
                    onClick={onCancelDeny}
                    className={cn(
                      "inline-flex h-8 items-center rounded-lg px-3 text-sm font-medium text-muted",
                      "transition-colors hover:bg-subtle hover:text-ink",
                      busy && "pointer-events-none opacity-50",
                    )}
                  >
                    取消
                  </button>
                  <button
                    type="button"
                    disabled={busy}
                    onClick={onConfirmDeny}
                    className={cn(
                      "inline-flex h-8 items-center gap-1.5 rounded-lg bg-ink px-4 text-sm font-medium text-paper",
                      "shadow-[0_1px_2px_rgba(20,30,50,0.12)] transition-opacity hover:opacity-90",
                      busy && "pointer-events-none opacity-50",
                    )}
                  >
                    <XIcon />
                    确认拒绝
                  </button>
                </div>
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function ShieldIcon() {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-4 shrink-0 text-muted"
    >
      <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" />
      <path d="m9 12 2 2 4-4" />
    </svg>
  );
}

function XIcon() {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-4 shrink-0"
      aria-hidden
    >
      <path d="M18 6 6 18" />
      <path d="m6 6 12 12" />
    </svg>
  );
}

function CheckIcon() {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="size-4 shrink-0"
      aria-hidden
    >
      <path d="M20 6 9 17l-5-5" />
    </svg>
  );
}
