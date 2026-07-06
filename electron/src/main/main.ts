/**
 * Interview-Agent Electron main process (dev-only shell).
 *
 * Chromium window pointed at the Vite dev server on :5173. Frontend still
 * hits the Go backend directly on :9001 — no proxy, no API-base injection.
 *
 * IPC surface (kept intentionally tiny — see preload):
 *   'pick-files' → PickedLocalFile[]
 *     Native macOS file/folder picker. Returns { path, name, isDirectory }
 *     for each selected entry. isDirectory is determined here (main) via
 *     fs.stat so the renderer never touches Node APIs.
 *
 * Explicitly NOT doing: spawning the Go backend, port probing, packaging,
 * auto-update, generic IPC bridge.
 *
 * Run:
 *   cd electron && pnpm install     # postinstall patches macOS bundle id
 *   pnpm start                       # tsc + electron .   (backend + Vite must be up)
 *   # or from repo root:  ./dev.sh   (starts backend + Vite + Electron)
 */

import { app, BrowserWindow, dialog, ipcMain } from 'electron';
import { stat } from 'node:fs/promises';
import path from 'node:path';

const DEV_RENDERER_URL = 'http://localhost:5173';

app.setName('Interview Agent');

// Structured result the renderer will see. Kept as an interface for the
// preload's TS side (which shares this shape via manual duplication —
// nothing worth a shared package for two fields).
interface PickedLocalFile {
  path: string;
  name: string;
  isDirectory: boolean;
}

function registerIpc(): void {
  ipcMain.handle('pick-files', async (): Promise<PickedLocalFile[]> => {
    const res = await dialog.showOpenDialog({
      // Mixed file/folder selection with multi-select. No filters — filters
      // interact poorly with openDirectory on macOS (folders vanish from
      // the dialog when a file filter is active) and users can eyeball
      // what they're picking.
      properties: ['openFile', 'openDirectory', 'multiSelections'],
    });
    if (res.canceled || res.filePaths.length === 0) return [];

    return Promise.all(
      res.filePaths.map(async (p) => {
        let isDirectory = false;
        try {
          const s = await stat(p);
          isDirectory = s.isDirectory();
        } catch {
          // stat failure is rare (the OS just handed us this path) but not
          // worth aborting the whole selection over. Treat as file.
        }
        return {
          path: p,
          name: path.basename(p),
          isDirectory,
        };
      }),
    );
  });
}

function createWindow(): void {
  const preloadPath = path.join(__dirname, '../preload/preload.js');

  const win = new BrowserWindow({
    width: 1440,
    height: 900,
    minWidth: 1024,
    minHeight: 640,
    // Fully frameless — no title bar, no traffic lights. Close with ⌘W,
    // minimise with ⌘M, hide with ⌘H. Window is still movable because the
    // sidebar top strip is marked .drag-region in the renderer CSS.
    frame: false,
    webPreferences: {
      // Modern secure defaults + a minimal preload that exposes exactly
      // one function (pickFiles). Do NOT expand the preload into a generic
      // IPC bridge — every additional surface is a security decision.
      contextIsolation: true,
      nodeIntegration: false,
      preload: preloadPath,
      devTools: true,
    },
  });

  win.loadURL(DEV_RENDERER_URL);

  // DevTools is auto-opened by default because this build is dev-only, but
  // it steals half the window and gets old fast for casual use. Set
  // INTERVIEW_ELECTRON_DEVTOOLS=0 (or false/off/no) to launch without it.
  if (shouldOpenDevTools()) {
    win.webContents.openDevTools();
  }
}

function shouldOpenDevTools(): boolean {
  const raw = (process.env.INTERVIEW_ELECTRON_DEVTOOLS ?? '').trim().toLowerCase();
  if (raw === '') return true;
  return !['0', 'false', 'off', 'no'].includes(raw);
}

app.whenReady().then(() => {
  registerIpc();
  createWindow();

  app.on('activate', () => {
    if (BrowserWindow.getAllWindows().length === 0) {
      createWindow();
    }
  });
});

// Dev shell: closing the last window ends the session on every platform.
app.on('window-all-closed', () => {
  app.quit();
});
