import { create } from "zustand";
import { listProjects, type ProjectItem } from "@/lib/api";

interface ProjectStore {
  items: ProjectItem[];
  loading: boolean;
  refresh: () => Promise<void>;
}

export const useProjectStore = create<ProjectStore>((set) => ({
  items: [],
  loading: false,
  refresh: async () => {
    set({ loading: true });
    try {
      const items = await listProjects();
      set({ items, loading: false });
    } catch (err) {
      console.error("[projects] refresh failed:", err);
      set({ loading: false });
    }
  },
}));
