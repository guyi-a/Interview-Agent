import { useState } from "react";
import { formatClock, cn } from "@/lib/utils";
import type { ChatTurn, SubAgentEvent, ToolCall } from "@/hooks/useChatStream";
import { MessageBody } from "./MessageBody";

function CopyIcon() {
  return (
    <svg
      width="12"
      height="12"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <rect x="4" y="4" width="9" height="9" rx="1.5" />
      <path d="M3 10V3.5A1.5 1.5 0 0 1 4.5 2H10" />
    </svg>
  );
}

function CheckIcon() {
  return (
    <svg
      width="12"
      height="12"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.75"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M3 8.5l3 3 7-7" />
    </svg>
  );
}

function ThinkingCard({
  content,
  label,
  dense,
  streaming,
  defaultOpen,
}: {
  content: string;
  label: string;
  dense?: boolean;
  streaming?: boolean;
  defaultOpen?: boolean;
}) {
  const [open, setOpen] = useState(Boolean(defaultOpen));
  const trimmed = content?.trim() ?? "";
  const isEmpty = trimmed.length === 0;
  const clickable = !isEmpty;
  return (
    <div className={dense ? "my-2" : "my-3"}>
      <button
        type="button"
        onClick={() => clickable && setOpen((o) => !o)}
        disabled={!clickable}
        className={cn(
          "flex items-center gap-2 text-[13px] font-medium text-ink",
          clickable && "hover:text-accent transition-colors cursor-pointer",
          !clickable && "opacity-60",
        )}
      >
        <span
          aria-hidden
          className={cn(
            "inline-block size-1.5 rounded-full bg-accent",
            streaming && "animate-pulse",
          )}
        />
        <span>{label}</span>
      </button>
      {open && !isEmpty && (
        <div
          className={cn(
            "mt-2 pl-4 border-l-2 border-ink/15",
            "italic whitespace-pre-wrap text-muted leading-relaxed",
            dense ? "text-[13px]" : "text-sm",
          )}
        >
          {content}
        </div>
      )}
    </div>
  );
}

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        } catch {
          /* clipboard may be unavailable in some contexts; silently ignore */
        }
      }}
      className={cn(
        "ml-auto inline-flex items-center gap-1.5 px-2 py-1 rounded",
        "text-muted hover:text-ink hover:bg-subtle",
        "opacity-0 group-hover:opacity-100 focus-visible:opacity-100",
        "transition-opacity",
        copied && "opacity-100 text-ink",
      )}
      aria-label={copied ? "已复制" : "复制消息"}
    >
      {copied ? <CheckIcon /> : <CopyIcon />}
      <span className="tracking-normal normal-case">
        {copied ? "已复制" : "复制"}
      </span>
    </button>
  );
}

const ROLE_LABEL: Record<ChatTurn["role"], string> = {
  user: "CANDIDATE",
  assistant: "INTERVIEWER",
};

// Sub-agent tools — wrapped via adk.NewAgentTool on the backend. When
// these appear in tool_call events, label them as AGENT so the UI reflects
// delegation, not a plain tool invocation.
const AGENT_TOOL_NAMES = new Set(["job_search", "deep_research"]);

export function TranscriptEntry({
  turn,
  showRule,
  streaming,
}: {
  turn: ChatTurn;
  showRule: boolean;
  streaming: boolean;
}) {
  const isUser = turn.role === "user";
  return (
    <article
      className={cn(
        "group",
        isUser && "ml-auto max-w-[85%] flex flex-col items-end",
        showRule &&
          (isUser ? "mt-8" : "border-t border-rule pt-8 mt-8"),
      )}
    >
      <header
        className={cn(
          "font-mono text-[10px] tracking-[0.18em] uppercase text-muted mb-3 flex items-center gap-3",
          isUser && "justify-end",
        )}
      >
        <span>{ROLE_LABEL[turn.role]}</span>
        <span aria-hidden="true">·</span>
        <span>{formatClock(turn.createdAt)}</span>
        {streaming && (
          <span className="text-accent normal-case tracking-normal lowercase">
            ● streaming
          </span>
        )}
        {turn.role === "assistant" && turn.content && !streaming && (
          <CopyButton text={turn.content} />
        )}
      </header>

      {turn.reasoning && (
        <ThinkingCard
          content={turn.reasoning}
          label={streaming && !turn.content ? "Thinking" : "Thoughts"}
          streaming={streaming && !turn.content}
        />
      )}

      {turn.tools.length > 0 && (
        <div className="my-4 space-y-3">
          {turn.tools.map((tc) => (
            <ToolEntry
              key={tc.id}
              tool={tc}
              subEvents={turn.subEvents.filter(
                (e) => e.parentToolCallId === tc.id,
              )}
            />
          ))}
        </div>
      )}

      {(() => {
        const orphans = turn.subEvents.filter(
          (e) =>
            !e.parentToolCallId ||
            !turn.tools.some((t) => t.id === e.parentToolCallId),
        );
        return orphans.length > 0 ? (
          <SubAgentTimeline events={orphans} />
        ) : null;
      })()}

      <div
        className={cn(
          "text-ink",
          isUser && "rounded-2xl bg-subtle px-4 py-3",
        )}
      >
        {turn.content ? (
          <MessageBody content={turn.content} streaming={streaming} />
        ) : (
          streaming && <span className="text-muted">…</span>
        )}
      </div>

      {turn.error && (
        <p className="mt-2 text-sm text-red-700">⚠ {turn.error}</p>
      )}
    </article>
  );
}

// SubAgentTimeline renders sub-agent events as a compact mini assistant turn:
// all thinking is merged into one Thoughts card, all tools become tool cards,
// and all text is merged into one markdown body. This mirrors the root agent
// presentation instead of exposing the raw interleaved event stream.
function SubAgentTimeline({ events }: { events: SubAgentEvent[] }) {
  type Block = {
    agent: string;
    reasoning: string;
    content: string;
    tools: ToolCall[];
    errors: string[];
  };

  const blocks: Block[] = [];
  const blockByAgent = new Map<string, Block>();
  const toolByID = new Map<string, ToolCall>();

  const blockFor = (agent: string) => {
    const existing = blockByAgent.get(agent);
    if (existing) return existing;
    const created: Block = {
      agent,
      reasoning: "",
      content: "",
      tools: [],
      errors: [],
    };
    blockByAgent.set(agent, created);
    blocks.push(created);
    return created;
  };

  for (const e of events) {
    const block = blockFor(e.agent);
    if (e.type === "tool_call") {
      const id = e.toolCallId ?? "";
      const tool: ToolCall = {
        id,
        name: e.name ?? "",
        argsJson: e.argsJson ?? "",
        status: "running",
      };
      block.tools.push(tool);
      if (id) toolByID.set(id, tool);
    } else if (e.type === "tool_result") {
      const id = e.toolCallId ?? "";
      const prev = toolByID.get(id);
      if (prev) {
        Object.assign(prev, {
          ...prev,
          name: prev.name || e.name || "",
          status: e.ok === false ? "error" : "ok",
          content: e.ok === false ? undefined : e.content,
          error: e.ok === false ? e.error : undefined,
        });
      } else {
        const tool: ToolCall = {
          id,
          name: e.name ?? "",
          argsJson: "",
          status: e.ok === false ? "error" : "ok",
          content: e.ok === false ? undefined : e.content,
          error: e.ok === false ? e.error : undefined,
        };
        block.tools.push(tool);
        if (id) toolByID.set(id, tool);
      }
    } else if (e.type === "thinking") {
      block.reasoning += e.content ?? "";
    } else if (e.type === "text") {
      block.content += e.content ?? "";
    } else {
      block.errors.push(e.error ?? e.content ?? "unknown error");
    }
  }

  if (blocks.length === 0) return null;

  return (
    <section className="my-4 pl-4 border-l border-ink/15 space-y-3">
      {blocks.map((block) => (
        <div key={block.agent} className="space-y-3">
          {block.reasoning && (
            <ThinkingCard
              content={block.reasoning}
              label="Thoughts"
              dense
              defaultOpen
            />
          )}
          {block.tools.length > 0 && (
            <div className="space-y-3">
              {block.tools.map((tool, i) => (
                <ToolEntry key={tool.id || `${block.agent}-${i}`} tool={tool} />
              ))}
            </div>
          )}
          {block.content && (
            <div className="text-ink/80">
              <MessageBody content={block.content} dense />
            </div>
          )}
          {block.errors.map((err, i) => (
            <div key={i} className="text-[13px] leading-relaxed whitespace-pre-wrap text-red-700">
              {err}
            </div>
          ))}
        </div>
      ))}
    </section>
  );
}

function ToolEntry({
  tool,
  subEvents,
}: {
  tool: ToolCall;
  subEvents?: SubAgentEvent[];
}) {
  const [open, setOpen] = useState(false);

  const argsParsed = tryParseJson(tool.argsJson);
  const hasArgs = argsParsed !== undefined && tool.argsJson !== "";
  const hasResult = Boolean(tool.content || tool.error);
  const hasSubEvents = Boolean(subEvents && subEvents.length > 0);
  const expandable = hasArgs || hasResult || hasSubEvents;
  const argLabel = toolArgLabel(argsParsed);
  const isAgent = AGENT_TOOL_NAMES.has(tool.name);

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
        <span
          className={cn(
            "text-[11px] tracking-[0.14em] uppercase font-semibold shrink-0",
            isAgent ? "text-accent" : "text-ink/75",
          )}
        >
          {isAgent ? "agent" : "tool"}
        </span>
        <span className="text-ink">{tool.name || "(unnamed)"}</span>
        {argLabel && (
          <span className="text-muted normal-case tracking-normal truncate">
            <span className="text-ink/70">{argLabel}</span>
          </span>
        )}
        <span
          className={cn(
            "inline-flex items-center gap-1.5 shrink-0 ml-1 text-[11px] uppercase tracking-[0.12em]",
            labelClass,
          )}
        >
          {dot}
          <span>{label}</span>
        </span>
      </button>

      {open && expandable && (
        <div className="mt-2 space-y-2">
          {hasSubEvents && subEvents && (
            <SubAgentTimeline events={subEvents} />
          )}
          {!hasSubEvents && hasArgs && (
            <div>
              <div className="text-[9px] tracking-[0.2em] uppercase text-muted mb-1">
                Args
              </div>
              <pre className="text-[11px] text-muted whitespace-pre-wrap break-all">
                {prettyJson(argsParsed)}
              </pre>
            </div>
          )}
          {!hasSubEvents && tool.content && (
            <div>
              <div className="text-[9px] tracking-[0.2em] uppercase text-muted mb-1">
                Result
              </div>
              <MessageBody content={truncate(tool.content, 1200)} dense />
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
      dot: (
        <span className="inline-block size-1.5 rounded-full bg-accent animate-pulse" />
      ),
      label: "running",
      labelClass: "text-accent font-medium",
    };
  }
  if (status === "ok") {
    return {
      dot: (
        <span
          aria-hidden
          className="inline-flex items-center justify-center size-3 text-emerald-600 leading-none"
        >
          <svg
            width="10"
            height="10"
            viewBox="0 0 12 12"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
          >
            <path d="M2.5 6.5l2.5 2.5 4.5-5" />
          </svg>
        </span>
      ),
      label: "done",
      labelClass: "text-emerald-600 font-medium",
    };
  }
  return {
    dot: (
      <span
        aria-hidden
        className="inline-flex items-center justify-center size-3 text-red-600 leading-none"
      >
        <svg
          width="10"
          height="10"
          viewBox="0 0 12 12"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
          strokeLinecap="round"
          strokeLinejoin="round"
        >
          <path d="M3 3l6 6M9 3l-6 6" />
        </svg>
      </span>
    ),
    label: "failed",
    labelClass: "text-red-600 font-medium",
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
