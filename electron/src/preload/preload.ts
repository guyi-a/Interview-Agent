/**
 * Preload — the ONLY renderer-facing bridge.
 *
 * Intentionally minimal: two functions exposed. Every additional surface
 * exposed here becomes a security decision (renderer runs untrusted 3rd-party
 * code someday, we don't want it able to poke Node APIs). If a new IPC route
 * is added, extend `electronAPI` explicitly — never expose `ipcRenderer` raw.
 */

import { contextBridge, ipcRenderer } from 'electron';

interface PickedLocalFile {
  path: string;
  name: string;
  isDirectory: boolean;
}

interface SavedPastedImage {
  path: string;
  name: string;
}

contextBridge.exposeInMainWorld('electronAPI', {
  pickFiles: (): Promise<PickedLocalFile[]> => ipcRenderer.invoke('pick-files'),

  // Persist an in-memory image (paste / drag-drop) to disk so the backend
  // can read it by absolute path. Returns the saved path + basename.
  savePastedImage: (
    bytes: Uint8Array,
    mimeType: string,
    suggestedName?: string,
  ): Promise<SavedPastedImage> =>
    ipcRenderer.invoke('save-pasted-image', { bytes, mimeType, suggestedName }),
});
