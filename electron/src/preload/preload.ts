/**
 * Preload — the ONLY renderer-facing bridge.
 *
 * Intentionally minimal: one function, one shape. Every additional surface
 * exposed here becomes a security decision (renderer runs untrusted 3rd-party
 * code someday, we don't want it able to poke Node APIs). If a new IPC route
 * is added, extend `electronAPI` explicitly — never expose `ipcRenderer` raw.
 */

import { contextBridge, ipcRenderer } from 'electron';

// Kept in sync with main.ts's PickedLocalFile. Two fields plus a bool —
// not worth a shared types package.
interface PickedLocalFile {
  path: string;
  name: string;
  isDirectory: boolean;
}

contextBridge.exposeInMainWorld('electronAPI', {
  pickFiles: (): Promise<PickedLocalFile[]> => ipcRenderer.invoke('pick-files'),
});
