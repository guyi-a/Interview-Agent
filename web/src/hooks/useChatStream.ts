import { useCallback, useEffect, useRef, useState } from "react";
import {
  cancelChat,
  listMessages,
  postChat,
  resumeChat,
  type PersistedMessage,
} from "@/lib/api";
import { useWorkspaceStore } from "@/features/workspace/store";

export type ToolCall = {
  id: string;
  name: string;
  argsJson: string;
  status: "running" | "ok" | "error";
  content?: string;
  error?: string;
};

// One captured event from a sub-agent (e.g. deep_research) inside a single
// assistant turn. The wire shape mirrors PersistedSubAgentEvent so live SSE
// frames and history replay produce the same structure. parentToolCallId
// links the event back to the root tool_call card.
export type SubAgentEvent = {
  seq: number;
  agent: string;
  parentToolCallId?: string;
  type: "thinking" | "text" | "tool_call" | "tool_result" | "error";
  content?: string;
  toolCallId?: string;
  name?: string;
  argsJson?: string;
  ok?: boolean;
  error?: string;
};

export type ChatTurn = {
  id: string;
  role: "user" | "assistant";
  content: string;
  reasoning: string;
  tools: ToolCall[];
  subEvents: SubAgentEvent[];
  createdAt: string;
  done: boolean;
  error?: string;
};

type Frame = {
  type:
    | "text"
    | "thinking"
    | "tool_call"
    | "tool_result"
    | "project_bound"
    | "usage"
    | "done"
    | "error";
  // Routing
  agent?: string;
  parent_tool_call_id?: string;
  // Common
  content?: string;
  message?: string;
  // Tool
  id?: string;
  name?: string;
  args_json?: string;
  ok?: boolean;
  error?: string;
  // Project (PR B, ignored for now if it ever shows up early)
  project_id?: string;
  project_name?: string;
  workspace_path?: string;
};

const WORKSPACE_TOOL_NAMES = new Set([
  "write_file",
  "edit_file",
  "create_file",
  "delete_file",
  "rename_file",
  "move_file",
  "mkdir",
  "create_directory",
  "remove_file",
  "shell",
  "bash",
  "run_shell",
  "run_command",
]);

function mayAffectWorkspace(name?: string): boolean {
  if (!name) return false;
  const normalized = name.toLowerCase();
  if (WORKSPACE_TOOL_NAMES.has(normalized)) return true;
  return (
    normalized.includes("file") ||
    normalized.includes("workspace") ||
    normalized.includes("shell") ||
    normalized.includes("command")
  );
}

function parseFrames(buffer: string): { frames: Frame[]; rest: string } {
  const frames: Frame[] = [];
  let rest = buffer;
  while (true) {
    const idx = rest.indexOf("\n\n");
    if (idx < 0) break;
    const block = rest.slice(0, idx);
    rest = rest.slice(idx + 2);
    const dataLines: string[] = [];
    for (const line of block.split("\n")) {
      if (line.startsWith("data:")) {
        dataLines.push(line.slice(5).trimStart());
      }
    }
    if (dataLines.length === 0) continue;
    try {
      frames.push(JSON.parse(dataLines.join("\n")) as Frame);
    } catch (err) {
      console.warn("[sse] bad frame", err, dataLines);
    }
  }
  return { frames, rest };
}

function fromPersisted(rows: PersistedMessage[]): ChatTurn[] {
  return rows
    .filter((r) => r.role === "user" || r.role === "assistant")
    .map((r) => ({
      id: `db-${r.seq}`,
      role: r.role as "user" | "assistant",
      content: r.content,
      reasoning: r.reasoning_content ?? "",
      tools: (r.tools ?? []).map((t) => ({
        id: t.id,
        name: t.name,
        argsJson: t.args_json ?? "",
        status: t.ok ? ("ok" as const) : ("error" as const),
        content: t.content,
        error: t.error,
      })),
      subEvents: (r.sub_events ?? []).map((e) => ({
        seq: e.seq,
        agent: e.agent,
        parentToolCallId: e.parent_tool_call_id,
        type: e.type,
        content: e.content,
        toolCallId: e.tool_call_id,
        name: e.name,
        argsJson: e.args_json,
        ok: e.ok,
        error: e.error,
      })),
      createdAt: r.created_at,
      done: true,
    }));
}

export type ProjectBoundEvent = {
  projectId: string;
  projectName: string;
  workspacePath: string;
};

// Drives an already-opened SSE Response: reads frames and routes them to the
// caller-provided mutators. Returns normally on `done`/`error` frame or when
// the server closes the stream. Throws AbortError when the underlying fetch
// is aborted — caller handles that.
async function runSSELoop(
  res: Response,
  updateAssistant: (fn: (t: ChatTurn) => ChatTurn) => void,
  upsertTool: (id: string, patch: Partial<ToolCall>) => void,
  appendSubEvent: (
    agentName: string,
    partial: Omit<SubAgentEvent, "seq" | "agent">,
  ) => void,
  onProjectBound: ((e: ProjectBoundEvent) => void) | undefined,
  onWorkspaceChanged: (() => void) | undefined,
  onError: (msg: string) => void,
) {
  if (!res.ok || !res.body) {
    throw new Error(`SSE: ${res.status}`);
  }
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  let finished = false;
  while (!finished) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    const { frames, rest } = parseFrames(buf);
    buf = rest;
    for (const f of frames) {
      // Sub-agent frames (e.g. deep_research's internal thinking + tool
      // calls) route to subEvents so they neither pollute the supervisor's
      // content/reasoning nor compete with the supervisor's tool cards.
      if (f.agent) {
        switch (f.type) {
          case "thinking":
          case "text":
            if (f.content) {
              appendSubEvent(f.agent, {
                parentToolCallId: f.parent_tool_call_id,
                type: f.type,
                content: f.content,
              });
            }
            break;
          case "tool_call":
            if (f.id) {
              appendSubEvent(f.agent, {
                parentToolCallId: f.parent_tool_call_id,
                type: "tool_call",
                toolCallId: f.id,
                name: f.name,
                argsJson: f.args_json,
              });
            }
            break;
          case "tool_result":
            if (f.id) {
              appendSubEvent(f.agent, {
                parentToolCallId: f.parent_tool_call_id,
                type: "tool_result",
                toolCallId: f.id,
                name: f.name,
                ok: f.ok,
                content: f.ok ? f.content : undefined,
                error: f.ok ? undefined : f.error ?? f.message,
              });
              if (f.ok && mayAffectWorkspace(f.name)) onWorkspaceChanged?.();
            }
            break;
          case "error":
            appendSubEvent(f.agent, {
              parentToolCallId: f.parent_tool_call_id,
              type: "error",
              error: f.message ?? f.error ?? "unknown error",
            });
            break;
          // usage / project_bound / done from a sub-agent are ignored —
          // those are root-agent concerns.
        }
        continue;
      }

      switch (f.type) {
        case "text":
          if (f.content)
            updateAssistant((t) => ({
              ...t,
              content: t.content + f.content,
            }));
          break;
        case "thinking":
          if (f.content)
            updateAssistant((t) => ({
              ...t,
              reasoning: t.reasoning + f.content,
            }));
          break;
        case "tool_call":
          if (f.id) {
            upsertTool(f.id, {
              name: f.name ?? "",
              argsJson: f.args_json ?? "",
              status: "running",
            });
          }
          break;
        case "tool_result":
          if (f.id) {
            upsertTool(f.id, {
              name: f.name ?? undefined,
              status: f.ok ? "ok" : "error",
              content: f.ok ? f.content : undefined,
              error: f.ok ? undefined : f.error ?? f.message,
            });
            if (f.ok && mayAffectWorkspace(f.name)) onWorkspaceChanged?.();
          }
          break;
        case "project_bound":
          if (f.project_id) {
            onProjectBound?.({
              projectId: f.project_id,
              projectName: f.project_name ?? "",
              workspacePath: f.workspace_path ?? "",
            });
          }
          break;
        case "usage":
          break;
        case "done":
          updateAssistant((t) => ({ ...t, done: true }));
          finished = true;
          break;
        case "error":
          updateAssistant((t) => ({
            ...t,
            done: true,
            error: f.message ?? "unknown error",
          }));
          onError(f.message ?? "unknown error");
          finished = true;
          break;
      }
      if (finished) break;
    }
  }
}

export function useChatStream(
  conversationID: string,
  opts?: {
    onProjectBound?: (e: ProjectBoundEvent) => void;
    projectId?: string;
  },
) {
  const [turns, setTurns] = useState<ChatTurn[]>([]);
  const [loading, setLoading] = useState(true);
  const [streaming, setStreaming] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);
  const onProjectBoundRef = useRef(opts?.onProjectBound);
  onProjectBoundRef.current = opts?.onProjectBound;
  const refreshWorkspaceFiles = useWorkspaceStore((s) => s.refreshFiles);
  const refreshWorkspaceFilesRef = useRef(refreshWorkspaceFiles);
  refreshWorkspaceFilesRef.current = refreshWorkspaceFiles;
  const projectIdRef = useRef(opts?.projectId);
  projectIdRef.current = opts?.projectId;

  // Drives a Response into the assistant turn with the given id. Owns the
  // streaming flag, abort-ref bookkeeping, and AbortError/error -> turn-state
  // translation. Used by both send (POST) and the mount-time resume (GET).
  const runStreamingResponse = useCallback(
    async (
      res: Response,
      assistantTurnId: string,
      controller: AbortController,
    ) => {
      const updateAssistant = (fn: (t: ChatTurn) => ChatTurn) => {
        setTurns((prev) => {
          const next = prev.slice();
          for (let i = next.length - 1; i >= 0; i--) {
            if (next[i].id === assistantTurnId) {
              next[i] = fn(next[i]);
              break;
            }
          }
          return next;
        });
      };

      const upsertTool = (id: string, patch: Partial<ToolCall>) => {
        updateAssistant((t) => {
          const idx = t.tools.findIndex((tc) => tc.id === id);
          if (idx < 0) {
            const next: ToolCall = {
              id,
              name: patch.name ?? "",
              argsJson: patch.argsJson ?? "",
              status: patch.status ?? "running",
              content: patch.content,
              error: patch.error,
            };
            return { ...t, tools: [...t.tools, next] };
          }
          // Drop empty-string fields from the patch so a later tool_result
          // frame (which may not carry a name) cannot wipe out the tool_call
          // frame's name. Explicit undefined still passes through — that's
          // how callers intentionally clear stale content/error.
          const clean: Partial<ToolCall> = {};
          (Object.keys(patch) as (keyof ToolCall)[]).forEach((k) => {
            const v = patch[k];
            if (v === "") return;
            (clean as Record<string, unknown>)[k] = v;
          });
          const merged = { ...t.tools[idx], ...clean };
          const tools = t.tools.slice();
          tools[idx] = merged;
          return { ...t, tools };
        });
      };

      // Append one sub-agent event. Consecutive thinking/text chunks from the
      // same agent are coalesced into the previous event so the rendered
      // narrative reads as continuous prose rather than per-token noise.
      // Everything else (tool_call / tool_result / error) pushes a new entry.
      const appendSubEvent = (
        agentName: string,
        partial: Omit<SubAgentEvent, "seq" | "agent">,
      ) => {
        updateAssistant((t) => {
          const next = t.subEvents.slice();
          const last = next[next.length - 1];
          const coalescable =
            partial.type === "thinking" || partial.type === "text";
          if (
            coalescable &&
            last &&
            last.agent === agentName &&
            last.type === partial.type &&
            last.parentToolCallId === partial.parentToolCallId
          ) {
            next[next.length - 1] = {
              ...last,
              content: (last.content ?? "") + (partial.content ?? ""),
            };
          } else {
            next.push({
              seq: next.length + 1,
              agent: agentName,
              ...partial,
            });
          }
          return { ...t, subEvents: next };
        });
      };

      try {
        await runSSELoop(
          res,
          updateAssistant,
          upsertTool,
          appendSubEvent,
          onProjectBoundRef.current,
          refreshWorkspaceFilesRef.current,
          setError,
        );
      } catch (err) {
        if ((err as { name?: string }).name === "AbortError") {
          updateAssistant((t) => ({ ...t, done: true, error: "已取消" }));
        } else {
          console.error("[chat] stream error:", err);
          const msg = String(err);
          updateAssistant((t) => ({ ...t, done: true, error: msg }));
          setError(msg);
        }
      } finally {
        refreshWorkspaceFilesRef.current();
        setStreaming(false);
        if (abortRef.current === controller) abortRef.current = null;
      }
    },
    [],
  );

  useEffect(() => {
    let cancelled = false;
    setTurns([]);
    setLoading(true);
    setError(null);

    const controller = new AbortController();

    (async () => {
      let rows: PersistedMessage[];
      try {
        rows = await listMessages(conversationID);
      } catch (err) {
        if (cancelled) return;
        console.error("[chat] load history failed:", err);
        setError(String(err));
        setLoading(false);
        return;
      }
      if (cancelled) return;
      setTurns(fromPersisted(rows));
      setLoading(false);

      // Always probe for an in-flight stream — backend returns 204 when there
      // is no live buffer, so we don't need to guess from the persisted rows.
      let res: Response | null;
      try {
        res = await resumeChat(conversationID, controller.signal);
      } catch (err) {
        if (cancelled || (err as { name?: string }).name === "AbortError")
          return;
        console.error("[chat] resume probe failed:", err);
        return;
      }
      if (cancelled || !res) return;

      const nowIso = new Date().toISOString();
      const assistantTurn: ChatTurn = {
        id: `a-resume-${nowIso}`,
        role: "assistant",
        content: "",
        reasoning: "",
        tools: [],
        subEvents: [],
        createdAt: nowIso,
        done: false,
      };
      setTurns((prev) => [...prev, assistantTurn]);
      setStreaming(true);
      abortRef.current = controller;
      await runStreamingResponse(res, assistantTurn.id, controller);
    })();

    return () => {
      cancelled = true;
      controller.abort();
      if (abortRef.current === controller) abortRef.current = null;
    };
  }, [conversationID, runStreamingResponse]);

  const send = useCallback(
    async (text: string) => {
      if (streaming) return;
      const trimmed = text.trim();
      if (!trimmed) return;

      const nowIso = new Date().toISOString();
      const userTurn: ChatTurn = {
        id: `u-${nowIso}`,
        role: "user",
        content: trimmed,
        reasoning: "",
        tools: [],
        subEvents: [],
        createdAt: nowIso,
        done: true,
      };
      const assistantTurn: ChatTurn = {
        id: `a-${nowIso}`,
        role: "assistant",
        content: "",
        reasoning: "",
        tools: [],
        subEvents: [],
        createdAt: nowIso,
        done: false,
      };
      setTurns((prev) => [...prev, userTurn, assistantTurn]);
      setStreaming(true);
      setError(null);

      const controller = new AbortController();
      abortRef.current = controller;

      let res: Response;
      try {
        res = await postChat(conversationID, trimmed, controller.signal, {
          projectId: projectIdRef.current,
        });
      } catch (err) {
        if ((err as { name?: string }).name === "AbortError") {
          setTurns((prev) =>
            prev.map((t) =>
              t.id === assistantTurn.id
                ? { ...t, done: true, error: "已取消" }
                : t,
            ),
          );
        } else {
          const msg = String(err);
          console.error("[chat] post failed:", err);
          setTurns((prev) =>
            prev.map((t) =>
              t.id === assistantTurn.id ? { ...t, done: true, error: msg } : t,
            ),
          );
          setError(msg);
        }
        setStreaming(false);
        if (abortRef.current === controller) abortRef.current = null;
        return;
      }

      await runStreamingResponse(res, assistantTurn.id, controller);
    },
    [conversationID, streaming, runStreamingResponse],
  );

  const cancel = useCallback(async () => {
    abortRef.current?.abort();
    abortRef.current = null;
    await cancelChat(conversationID);
  }, [conversationID]);

  return { turns, loading, streaming, error, send, cancel };
}
