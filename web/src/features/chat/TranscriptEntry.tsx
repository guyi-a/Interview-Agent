import { useState } from "react";
import { formatClock, cn } from "@/lib/utils";
import type { ChatTurn, ToolCall } from "@/hooks/useChatStream";

const ROLE_LABEL: Record<ChatTurn["role"], string> = {
  user: "CANDIDATE",
  assistant: "INTERVIEWER",
};

export function TranscriptEntry({
  turn,
  showRule,
  streaming,
}: {
  turn: ChatTurn;
  showRule: boolean;
  streaming: boolean;
}) {
  return (
    <article className={showRule ? "border-t border-rule pt-8 mt-8" : ""}>
      <header className="font-mono text-[10px] tracking-[0.18em] uppercase text-muted mb-3 flex items-center gap-3">
        <span>{ROLE_LABEL[turn.role]}</span>
        <span aria-hidden="true">·</span>
        <span>{formatClock(turn.createdAt)}</span>
        {streaming && (
          <span className="text-accent normal-case tracking-normal lowercase">
            ● streaming
          </span>
        )}
      </header>

      {turn.reasoning && (
        <aside className="my-3 pl-4 border-l-2 border-ink/15 text-sm text-muted italic whitespace-pre-wrap leading-relaxed">
          {turn.reasoning}
        </aside>
      )}

      {turn.tools.length > 0 && (
        <div className="my-4 space-y-3">
          {turn.tools.map((tc) => (
            <ToolEntry key={tc.id} tool={tc} />
          ))}
        </div>
      )}

      <div className="text-[15px] leading-7 whitespace-pre-wrap text-ink">
        {turn.content}
        {streaming && !turn.content && (
          <span className="text-muted">…</span>
        )}
      </div>

      {turn.error && (
        <p className="mt-2 text-sm text-red-700">⚠ {turn.error}</p>
      )}
    </article>
  );
}

function ToolEntry({ tool }: { tool: ToolCall }) {
  const [open, setOpen] = useState(false);

  const argsParsed = tryParseJson(tool.argsJson);
  const hasArgs = argsParsed !== undefined && tool.argsJson !== "";
  const hasResult = Boolean(tool.content || tool.error);
  const expandable = hasArgs || hasResult;

  const { dot, label, labelClass } = statusBits(tool.status);

  return (
    <aside className="pl-4 border-l-2 border-accent font-mono text-[12px] leading-relaxed">
      <button
        type="button"
        onClick={() => expandable && setOpen((v) => !v)}
        className={cn(
          "flex items-baseline gap-2 w-full text-left",
          expandable && "cursor-pointer",
        )}
      >
        <span className="text-[10px] tracking-[0.18em] uppercase text-muted shrink-0">
          tool
        </span>
        <span className="text-ink">{tool.name || "(unnamed)"}</span>
        <span
          className={cn("inline-flex items-center gap-1 shrink-0 ml-1", labelClass)}
        >
          {dot}
          <span>{label}</span>
        </span>
      </button>

      {open && expandable && (
        <div className="mt-2 space-y-2">
          {hasArgs && (
            <div>
              <div className="text-[9px] tracking-[0.2em] uppercase text-muted mb-1">
                Args
              </div>
              <pre className="text-[11px] text-muted whitespace-pre-wrap break-all">
                {prettyJson(argsParsed)}
              </pre>
            </div>
          )}
          {tool.content && (
            <div>
              <div className="text-[9px] tracking-[0.2em] uppercase text-muted mb-1">
                Result
              </div>
              <pre className="text-[11px] text-ink whitespace-pre-wrap break-all">
                {truncate(tool.content, 1200)}
              </pre>
            </div>
          )}
          {tool.error && (
            <div>
              <div className="text-[9px] tracking-[0.2em] uppercase text-red-700 mb-1">
                Error
              </div>
              <pre className="text-[11px] text-red-700 whitespace-pre-wrap break-all">
                {tool.error}
              </pre>
            </div>
          )}
        </div>
      )}
    </aside>
  );
}

function statusBits(status: ToolCall["status"]): {
  dot: React.ReactNode;
  label: string;
  labelClass: string;
} {
  if (status === "running") {
    return {
      dot: <span className="size-1.5 rounded-full bg-accent animate-pulse" />,
      label: "调用中",
      labelClass: "text-muted",
    };
  }
  if (status === "ok") {
    return {
      dot: <span className="size-1.5 rounded-full bg-ink/40" />,
      label: "已完成",
      labelClass: "text-muted",
    };
  }
  return {
    dot: <span className="text-red-700">✕</span>,
    label: "失败",
    labelClass: "text-red-700",
  };
}

function tryParseJson(s: string): unknown {
  try {
    return s ? JSON.parse(s) : undefined;
  } catch {
    return s;
  }
}

function prettyJson(v: unknown): string {
  if (v === undefined) return "";
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}

function truncate(s: string, n: number): string {
  if (s.length <= n) return s;
  return s.slice(0, n) + "…";
}
