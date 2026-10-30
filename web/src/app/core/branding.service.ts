import { Injectable, computed, inject, signal } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { lastValueFrom } from 'rxjs';
import type { Branding } from './api';

/** 默认站名，与 index.html 的 <title> 和后台 placeholder 保持一致。 */
export const DEFAULT_SITE_NAME = 'voidxo 维空';

/**
 * 当前站名。放在模块作用域而不是服务里：
 * AppTitleStrategy 要用它拼标题，而它由 Router 创建——
 * 如果反过来注入 BrandingService 会绕成环，直接从模块变量读最干净。
 */
let siteNameCache = DEFAULT_SITE_NAME;

export function currentSiteName(): string {
  return siteNameCache;
}

declare global {
  interface Window {
    /** 服务端写进 index.html 的站点标识（见 internal/api/branding.go）。 */
    __VOIDXO_BRANDING__?: Branding;
  }
}

/**
 * 服务端注入的那份标识。
 *
 * 为什么要读它：只靠 /api/branding 的话，网络慢时会先画出默认的 `</>` 章和默认站名，
 * 等接口回来再跳成自己的——越慢越明显。用注入值做初始值，第一次渲染就是对的。
 */
function readSeed(): Branding | null {
  const seed = typeof window === 'undefined' ? undefined : window.__VOIDXO_BRANDING__;
  if (!seed) return null;
  const name = (seed.name || '').trim() || DEFAULT_SITE_NAME;
  siteNameCache = name;
  return { ...seed, name };
}

/**
 * 站点标识（顶栏图标 / 标签页图标 / 站名）。
 *
 * 做成服务而不是让顶栏自己请求：后台改完要能立刻反映到顶栏与首屏，
 * 而不是「改完、看着没变、刷新一下才对」。
 */
@Injectable({ providedIn: 'root' })
export class BrandingService {
  private readonly http = inject(HttpClient);
  /** 当前标识；先用服务端注入的那份，null 才表示真的没有（按默认渲染）。 */
  readonly current = signal<Branding | null>(readSeed());
  private loading = false;

  /** 品牌字拆成主名与副标：「voidxo 维空」→ voidxo / 维空。 */
  readonly parts = computed(() => {
    const full = (this.current()?.name || DEFAULT_SITE_NAME).trim();
    const i = full.indexOf(' ');
    if (i < 0) return { main: full, sub: '' };
    return { main: full.slice(0, i), sub: full.slice(i + 1).trim() };
  });

  /** 首次加载；已经读过就直接返回（顶栏与首页都会调，不必发两次请求）。 */
  async load(): Promise<void> {
    if (this.current()) return;
    await this.fetch();
  }

  /** 后台改完站名后强制重读。 */
  async refresh(): Promise<void> {
    await this.fetch();
  }

  /** 后台保存成功后直接把新状态推给顶栏。 */
  apply(b: Branding): void {
    this.set(b);
  }

  private async fetch(): Promise<void> {
    if (this.loading) return;
    this.loading = true;
    try {
      this.set(await lastValueFrom(this.http.get<Branding>('/api/branding')));
    } catch {
      // 读不到就保持默认：一次请求失败不该让顶栏变成空白方块。
    } finally {
      this.loading = false;
    }
  }

  private set(b: Branding): void {
    const name = (b.name || '').trim() || DEFAULT_SITE_NAME;
    // 路由器可能在品牌名回来之前就把标题设成了默认名，这里补一次替换。
    if (name !== siteNameCache && document.title.includes(siteNameCache)) {
      document.title = document.title.split(siteNameCache).join(name);
    }
    siteNameCache = name;
    this.current.set({ ...b, name });
  }
}
