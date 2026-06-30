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

export type PersistedMessage = {
  seq: number;
  role: "user" | "assistant" | "tool" | "system";
  content: string;
  reasoning_content?: string;
  tools?: PersistedToolEvent[];
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
): Promise<Response> {
  return fetch(`${API_BASE}/chat/${encodeURIComponent(id)}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ message }),
    signal,
  });
}

export async function cancelChat(id: string): Promise<void> {
  await fetch(`${API_BASE}/chat/${encodeURIComponent(id)}/cancel`, {
    method: "POST",
  }).catch(() => {});
}
