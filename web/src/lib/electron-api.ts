/**
 * Electron renderer-side bridge. Mirrors the shape exposed by
 * electron/src/preload/preload.ts — must stay in sync.
 *
 * The whole app runs inside the Electron shell (see dev.sh), so `electronAPI`
 * is assumed to exist. No browser-mode fallback.
 */

export interface PickedLocalFile {
  path: string;
  name: string;
  isDirectory: boolean;
}

export interface ElectronAPI {
  pickFiles: () => Promise<PickedLocalFile[]>;
}

declare global {
  interface Window {
    electronAPI: ElectronAPI;
  }
}

export const electronAPI: ElectronAPI = window.electronAPI;
