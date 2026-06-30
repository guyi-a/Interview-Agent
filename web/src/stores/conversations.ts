import { create } from "zustand";
import {
  deleteConversation,
  listConversations,
  type ConversationItem,
} from "@/lib/api";

interface ConversationStore {
  items: ConversationItem[];
  loading: boolean;
  refresh: () => Promise<void>;
  remove: (id: string) => Promise<void>;
  touch: (id: string, title?: string) => void;
}

export const useConversationStore = create<ConversationStore>((set, get) => ({
  items: [],
  loading: false,

  refresh: async () => {
    set({ loading: true });
    try {
      const items = await listConversations();
      set({ items, loading: false });
    } catch (err) {
      console.error("[conversations] refresh failed:", err);
      set({ loading: false });
    }
  },

  remove: async (id) => {
    const prev = get().items;
    set({ items: prev.filter((c) => c.id !== id) });
    try {
      await deleteConversation(id);
    } catch (err) {
      console.error("[conversations] delete failed:", err);
      set({ items: prev });
    }
  },

  touch: (id, title) => {
    const now = new Date().toISOString();
    const existing = get().items.find((c) => c.id === id);
    if (existing) {
      const updated = {
        ...existing,
        updated_at: now,
        ...(title !== undefined ? { title } : {}),
      };
      set({
        items: [updated, ...get().items.filter((c) => c.id !== id)],
      });
    } else {
      set({
        items: [
          { id, title: title ?? "", updated_at: now },
          ...get().items,
        ],
      });
    }
  },
}));
