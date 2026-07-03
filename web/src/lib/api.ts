export const API_BASE = "http://localhost:9001";

export type ConversationItem = {
  id: string;
  project_id?: string | null;
  title: string;
  updated_at: string;
};

export type ProjectItem = {
  id: string;
  name: string;
  workspace: string;
  updated_at: string;
};

export type PersistedToolEvent = {
  id: string;
  name: string;
  args_json?: string;
  ok: boolean;
  content?: string;
  error?: string;
};

// One event captured from a sub-agent (e.g. deep_research) during a single
// assistant turn. Persisted as an ordered array so the UI can replay the
// nested timeline after the page reloads. parent_tool_call_id links each
// event back to the root tool_call that triggered the sub-agent.
export type PersistedSubAgentEvent = {
  seq: number;
  agent: string;
  parent_tool_call_id?: string;
  type: "thinking" | "text" | "tool_call" | "tool_result" | "error";
  content?: string;
  tool_call_id?: string;
  name?: string;
  args_json?: string;
  ok?: boolean;
  error?: string;
};

export type PersistedMessage = {
  seq: number;
  role: "user" | "assistant" | "tool" | "system";
  content: string;
  reasoning_content?: string;
  tools?: PersistedToolEvent[];
  sub_events?: PersistedSubAgentEvent[];
  created_at: string;
};

export async function listConversations(): Promise<ConversationItem[]> {
  const res = await fetch(`${API_BASE}/conversations`);
  if (!res.ok) throw new Error(`listConversations: ${res.status}`);
  const data = (await res.json()) as { conversations: ConversationItem[] };
  return data.conversations ?? [];
}

export async function listProjects(): Promise<ProjectItem[]> {
  const res = await fetch(`${API_BASE}/projects`);
  if (!res.ok) throw new Error(`listProjects: ${res.status}`);
  const data = (await res.json()) as { projects: ProjectItem[] };
  return data.projects ?? [];
}

export async function renameProject(id: string, name: string): Promise<void> {
  const res = await fetch(`${API_BASE}/projects/${encodeURIComponent(id)}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name }),
  });
  if (!res.ok && res.status !== 204) {
    throw new Error(`renameProject: ${res.status}`);
  }
}

export async function deleteProject(id: string): Promise<{ warning?: string }> {
  const res = await fetch(`${API_BASE}/projects/${encodeURIComponent(id)}`, {
    method: "DELETE",
  });
  if (res.status === 204) return {};
  if (res.status === 200) {
    return (await res.json()) as { warning?: string };
  }
  throw new Error(`deleteProject: ${res.status}`);
}

export async function openProjectInFinder(id: string): Promise<void> {
  const res = await fetch(
    `${API_BASE}/projects/${encodeURIComponent(id)}/open`,
    { method: "POST" },
  );
  if (!res.ok && res.status !== 204) {
    throw new Error(`openProjectInFinder: ${res.status}`);
  }
}

export async function listMessages(id: string): Promise<PersistedMessage[]> {
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(id)}/messages`,
  );
  if (!res.ok) throw new Error(`listMessages: ${res.status}`);
  const data = (await res.json()) as { messages: PersistedMessage[] };
  return data.messages ?? [];
}

export async function deleteConversation(id: string): Promise<void> {
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(id)}`,
    { method: "DELETE" },
  );
  if (!res.ok && res.status !== 204) {
    throw new Error(`deleteConversation: ${res.status}`);
  }
}

export async function postChat(
  id: string,
  message: string,
  signal: AbortSignal,
  opts?: { projectId?: string },
): Promise<Response> {
  const qs = opts?.projectId
    ? `?project_id=${encodeURIComponent(opts.projectId)}`
    : "";
  return fetch(`${API_BASE}/chat/${encodeURIComponent(id)}${qs}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ message }),
    signal,
  });
}

export async function resumeChat(
  id: string,
  signal: AbortSignal,
): Promise<Response | null> {
  const res = await fetch(`${API_BASE}/chat/${encodeURIComponent(id)}`, {
    method: "GET",
    signal,
  });
  if (res.status === 204) return null;
  if (!res.ok) throw new Error(`resumeChat: ${res.status}`);
  return res;
}

export async function cancelChat(id: string): Promise<void> {
  await fetch(`${API_BASE}/chat/${encodeURIComponent(id)}/cancel`, {
    method: "POST",
  }).catch(() => {});
}

export type WorkspaceTreeEntry = {
  path: string;
  name: string;
  is_dir: boolean;
  size?: number;
  modified_at: string;
};

export type WorkspaceMeta = {
  project_id: string;
  root_name: string;
};

export type WorkspaceTree = {
  workspace: WorkspaceMeta;
  entries: WorkspaceTreeEntry[];
  truncated?: boolean;
};

export type WorkspaceFileKind =
  | "markdown"
  | "text"
  | "image"
  | "binary"
  | "unsupported";

export type WorkspaceFile = {
  path: string;
  name: string;
  size: number;
  mime?: string;
  kind: WorkspaceFileKind;
  is_binary: boolean;
  content?: string;
  truncated?: boolean;
};

export async function fetchWorkspaceTree(
  conversationId: string,
  opts?: { projectId?: string },
  signal?: AbortSignal,
): Promise<WorkspaceTree> {
  const qs = opts?.projectId
    ? `?project_id=${encodeURIComponent(opts.projectId)}`
    : "";
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationId)}/workspace/tree${qs}`,
    { signal },
  );
  if (!res.ok) throw new Error(`fetchWorkspaceTree: ${res.status}`);
  return res.json();
}

export async function fetchWorkspaceFile(
  conversationId: string,
  path: string,
  opts?: { projectId?: string },
  signal?: AbortSignal,
): Promise<WorkspaceFile> {
  const params = new URLSearchParams({ path });
  if (opts?.projectId) params.set("project_id", opts.projectId);
  const res = await fetch(
    `${API_BASE}/conversations/${encodeURIComponent(conversationId)}/workspace/file?${params.toString()}`,
    { signal },
  );
  if (!res.ok) throw new Error(`fetchWorkspaceFile: ${res.status}`);
  return res.json();
}

export function workspaceDownloadURL(
  conversationId: string,
  path: string,
  opts?: { projectId?: string },
): string {
  const params = new URLSearchParams({ path });
  if (opts?.projectId) params.set("project_id", opts.projectId);
  return `${API_BASE}/conversations/${encodeURIComponent(conversationId)}/workspace/download?${params.toString()}`;
}

export function workspaceInlineURL(
  conversationId: string,
  path: string,
  opts?: { projectId?: string },
): string {
  const params = new URLSearchParams({ path });
  if (opts?.projectId) params.set("project_id", opts.projectId);
  return `${API_BASE}/conversations/${encodeURIComponent(conversationId)}/workspace/inline?${params.toString()}`;
}
