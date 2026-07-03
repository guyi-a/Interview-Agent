import { create } from "zustand";
import { persist } from "zustand/middleware";

interface State {
  panelOpen: boolean;
  previewPath: string | null;
  previewWidth: number;
  switcherOpen: boolean;
  filesVersion: number;
  setPanelOpen: (open: boolean) => void;
  togglePanel: () => void;
  openFile: (path: string) => void;
  closePreview: () => void;
  resetConversationState: () => void;
  setPreviewWidth: (width: number) => void;
  toggleSwitcher: () => void;
  closeSwitcher: () => void;
  refreshFiles: () => void;
}

const PREVIEW_MIN_WIDTH = 360;
const PREVIEW_MAX_WIDTH = 760;

export const useWorkspaceStore = create<State>()(
  persist(
    (set) => ({
      panelOpen: false,
      previewPath: null,
      previewWidth: 520,
      switcherOpen: false,
      filesVersion: 0,
      setPanelOpen: (open) => set({ panelOpen: open }),
      togglePanel: () => set((s) => ({ panelOpen: !s.panelOpen })),
      openFile: (path) =>
        set({ panelOpen: true, previewPath: path, switcherOpen: false }),
      closePreview: () => set({ previewPath: null, switcherOpen: false }),
      resetConversationState: () =>
        set((s) => ({
          previewPath: null,
          switcherOpen: false,
          filesVersion: s.filesVersion + 1,
        })),
      setPreviewWidth: (width) =>
        set({
          previewWidth: Math.max(
            PREVIEW_MIN_WIDTH,
            Math.min(PREVIEW_MAX_WIDTH, width),
          ),
      }),
      toggleSwitcher: () => set((s) => ({ switcherOpen: !s.switcherOpen })),
      closeSwitcher: () => set({ switcherOpen: false }),
      refreshFiles: () => set((s) => ({ filesVersion: s.filesVersion + 1 })),
    }),
    {
      name: "workspace-panel",
      partialize: (s) => ({ panelOpen: s.panelOpen, previewWidth: s.previewWidth }),
    },
  ),
);
