import { useState } from "react";
import { formatClock, cn } from "@/lib/utils";
import type { ChatTurn, SubAgentEvent, ToolCall } from "@/hooks/useChatStream";

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

      {turn.subEvents.length > 0 && (
        <SubAgentTimeline events={turn.subEvents} />
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

// SubAgentTimeline renders the captured events from sub-agents (e.g.
// deep_research) for one assistant turn. It walks the events in arrival
// order, coalesces matching tool_call + tool_result pairs into a single
// ToolEntry card, and renders thinking / text / error as labeled prose.
//
// First-pass UX: everything is flat under one indented column with a faint
// left rule, and each item carries a `↳ <agent>` chip so the user can tell
// the supervisor's own events from the sub-agent's. We can switch to a
// disclosure-style nested layout later by grouping on parentToolCallId.
function SubAgentTimeline({ events }: { events: SubAgentEvent[] }) {
  type ProseItem = {
    kind: "prose";
    agent: string;
    type: "thinking" | "text" | "error";
    content: string;
  };
  type ToolItem = {
    kind: "tool";
    agent: string;
    toolCallId: string;
    name: string;
    argsJson: string;
    status: ToolCall["status"];
    content?: string;
    error?: string;
  };
  type Item = ProseItem | ToolItem;

  const items: Item[] = [];
  // toolCallId → index into items so a later tool_result can mutate the
  // matching tool_call entry in place.
  const toolIdx = new Map<string, number>();

  for (const e of events) {
    if (e.type === "tool_call") {
      const id = e.toolCallId ?? "";
      items.push({
        kind: "tool",
        agent: e.agent,
        toolCallId: id,
        name: e.name ?? "",
        argsJson: e.argsJson ?? "",
        status: "running",
      });
      toolIdx.set(id, items.length - 1);
    } else if (e.type === "tool_result") {
      const id = e.toolCallId ?? "";
      const idx = toolIdx.get(id);
      if (idx !== undefined) {
        const prev = items[idx] as ToolItem;
        items[idx] = {
          ...prev,
          name: prev.name || e.name || "",
          status: e.ok === false ? "error" : "ok",
          content: e.ok === false ? undefined : e.content,
          error: e.ok === false ? e.error : undefined,
        };
      } else {
        // Result without a matching call (shouldn't happen, but render
        // defensively rather than dropping the event).
        items.push({
          kind: "tool",
          agent: e.agent,
          toolCallId: id,
          name: e.name ?? "",
          argsJson: "",
          status: e.ok === false ? "error" : "ok",
          content: e.ok === false ? undefined : e.content,
          error: e.ok === false ? e.error : undefined,
        });
      }
    } else {
      items.push({
        kind: "prose",
        agent: e.agent,
        type: e.type,
        content: e.content ?? e.error ?? "",
      });
    }
  }

  if (items.length === 0) return null;

  return (
    <section className="my-4 pl-4 border-l border-ink/15 space-y-3">
      {items.map((it, i) => {
        if (it.kind === "prose") {
          return (
            <div key={i}>
              <div className="font-mono text-[10px] tracking-[0.18em] uppercase text-muted mb-1">
                ↳ {it.agent} · {it.type}
              </div>
              <div
                className={cn(
                  "text-[13px] leading-relaxed whitespace-pre-wrap",
                  it.type === "thinking" && "text-muted italic",
                  it.type === "text" && "text-ink/80",
                  it.type === "error" && "text-red-700",
                )}
              >
                {it.content}
              </div>
            </div>
          );
        }
        const synthetic: ToolCall = {
          id: it.toolCallId || `sub-${i}`,
          name: it.name,
          argsJson: it.argsJson,
          status: it.status,
          content: it.content,
          error: it.error,
        };
        return (
          <div key={i}>
            <div className="font-mono text-[10px] tracking-[0.18em] uppercase text-muted mb-1">
              ↳ {it.agent}
            </div>
            <ToolEntry tool={synthetic} />
          </div>
        );
      })}
    </section>
  );
}

function ToolEntry({ tool }: { tool: ToolCall }) {
  const [open, setOpen] = useState(false);

  const argsParsed = tryParseJson(tool.argsJson);
  const hasArgs = argsParsed !== undefined && tool.argsJson !== "";
  const hasResult = Boolean(tool.content || tool.error);
  const expandable = hasArgs || hasResult;
  const argLabel = toolArgLabel(argsParsed);

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
        {argLabel && (
          <span className="text-muted normal-case tracking-normal truncate">
            <span className="text-ink/70">{argLabel}</span>
          </span>
        )}
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

function toolArgLabel(v: unknown): string {
  if (!v || typeof v !== "object" || Array.isArray(v)) return "";
  const args = v as Record<string, unknown>;
  const action = args.action;
  if (typeof action === "string" && action) return action;
  const name = args.name;
  if (typeof name === "string" && name) return name;
  return "";
}

function truncate(s: string, n: number): string {
  if (s.length <= n) return s;
  return s.slice(0, n) + "…";
}
