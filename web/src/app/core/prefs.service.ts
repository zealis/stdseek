import { Injectable, signal } from '@angular/core';
import { type Prefs } from './api';

const PREFS_KEY = 'stdseek.prefs';
const THEME_KEY = 'stdseek.theme';
const OVERVIEW_KEY = 'stdseek.pref.overview';

export type Theme = 'system' | 'light' | 'dark';

/**
 * 搜索偏好与主题。设置页编辑、结果页消费，存 localStorage。
 * 主题写到 <html data-theme>：'system' 时移除属性，交给 prefers-color-scheme。
 */
@Injectable({ providedIn: 'root' })
export class PrefsService {
  private readonly _prefs = signal<Prefs>({ exclude: [], cats: [], since: 0, getRequest: true });
  private readonly _theme = signal<Theme>('system');
  /** AI 概览开关（本机偏好，默认开；后台功能开关关闭时前端仍不显示入口）。 */
  private readonly _overview = signal(true);
  private loaded = false;

  readonly prefs = this._prefs.asReadonly();
  readonly theme = this._theme.asReadonly();
  readonly overview = this._overview.asReadonly();

  constructor() {
    this.load();
  }

  /** 纯 SPA 无 SSR：localStorage 在构造时即可用，读一次并存内存副本。 */
  load(): void {
    if (this.loaded) return;
    this.loaded = true;
    try {
      const raw = localStorage.getItem(PREFS_KEY);
      if (raw) {
        const p = JSON.parse(raw) as Partial<Prefs>;
        this._prefs.set({
          exclude: p.exclude ?? [],
          cats: p.cats ?? [],
          since: p.since ?? 0,
          getRequest: p.getRequest ?? true,
        });
      }
      const t = localStorage.getItem(THEME_KEY);
      if (t === 'light' || t === 'dark' || t === 'system') this._theme.set(t);
      if (localStorage.getItem(OVERVIEW_KEY) === '0') this._overview.set(false);
    } catch {
      /* 忽略损坏的本地存储 */
    }
    this.applyTheme(this._theme());
  }

  save(prefs: Prefs): void {
    try {
      localStorage.setItem(PREFS_KEY, JSON.stringify(prefs));
    } catch {
      /* 同上：写入失败不阻断使用 */
    }
    this._prefs.set({ ...prefs });
  }

  /** 是否用 GET 检索。历史数据没有这个字段时按 true 处理。 */
  getRequest(): boolean {
    return this._prefs().getRequest !== false;
  }

  setGetRequest(on: boolean): void {
    this.save({ ...this._prefs(), getRequest: on });
  }

  setOverview(on: boolean): void {
    try {
      localStorage.setItem(OVERVIEW_KEY, on ? '1' : '0');
    } catch {
      /* ignore */
    }
    this._overview.set(on);
  }

  setTheme(theme: Theme): void {
    try {
      localStorage.setItem(THEME_KEY, theme);
    } catch {
      /* ignore */
    }
    this._theme.set(theme);
    this.applyTheme(theme);
  }

  private applyTheme(theme: Theme): void {
    const root = document.documentElement;
    if (theme === 'system') {
      delete root.dataset['theme'];
      if (window.matchMedia('(prefers-color-scheme: dark)').matches) root.dataset['theme'] = 'dark';
    } else {
      root.dataset['theme'] = theme;
    }
  }
}
