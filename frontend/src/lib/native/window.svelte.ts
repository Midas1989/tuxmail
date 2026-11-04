// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Desktop window decoration (Wails, cmd/tuxmail/desktopprefs.go): in a
// frameless window TuxMail draws its own title bar (TitleBar.svelte) with
// a drag region (--wails-draggable: drag) and the window buttons, which
// call the Wails runtime. The Go side is bound as window.go.main.Desktop.

export interface WindowInfo {
  platform: 'windows' | 'linux' | 'darwin' | string;
  frameless: boolean;
  nativeTitleBar: boolean;
  backdrop: boolean;
  logFile: string;
}

interface WailsRuntime {
  WindowMinimise?: () => void;
  WindowToggleMaximise?: () => void;
  WindowIsMaximised?: () => Promise<boolean>;
  Quit?: () => void;
}

interface DesktopBinding {
  WindowInfo?: () => Promise<WindowInfo>;
  SetNativeTitleBar?: (native: boolean) => Promise<void>;
}

function binding(): DesktopBinding | undefined {
  const w = globalThis as unknown as { go?: { main?: { Desktop?: DesktopBinding } } };
  return w.go?.main?.Desktop;
}

function rt(): WailsRuntime {
  return ((globalThis as unknown as { runtime?: WailsRuntime }).runtime ?? {}) as WailsRuntime;
}

class DesktopWindow {
  info = $state<WindowInfo | null>(null);
  maximised = $state(false);

  /** The app draws the title bar (frameless desktop window). */
  get custom(): boolean {
    return !!this.info?.frameless;
  }

  async init(root: HTMLElement = document.documentElement): Promise<void> {
    const b = binding();
    if (!b?.WindowInfo) return;
    try {
      this.info = await b.WindowInfo();
    } catch {
      return;
    }
    root.classList.toggle('tm-frameless', this.info.frameless);
    root.classList.toggle('tm-backdrop', this.info.backdrop);
    root.dataset.platform = this.info.platform;
    const sync = () => void rt().WindowIsMaximised?.().then((m) => (this.maximised = m));
    sync();
    addEventListener('resize', sync);
  }

  minimise(): void {
    rt().WindowMinimise?.();
  }

  toggleMaximise(): void {
    rt().WindowToggleMaximise?.();
    setTimeout(() => void rt().WindowIsMaximised?.().then((m) => (this.maximised = m)), 150);
  }

  close(): void {
    rt().Quit?.();
  }

  async setNativeTitleBar(native: boolean): Promise<void> {
    await binding()?.SetNativeTitleBar?.(native);
    if (this.info) this.info = { ...this.info, nativeTitleBar: native };
  }
}

export const desktopWindow = new DesktopWindow();
