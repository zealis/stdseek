import { Injectable, inject, signal } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { lastValueFrom } from 'rxjs';
import type { Branding } from './api';

/**
 * 站点标识（顶栏图标 / 标签页图标）。
 *
 * 做成服务而不是让顶栏自己请求：后台改完图标后要能立刻反映到顶栏，
 * 而不是「传上去了、看着没变、刷新一下才对」。
 */
@Injectable({ providedIn: 'root' })
export class BrandingService {
  private readonly http = inject(HttpClient);
  /** 当前图标；null 表示还没读到（按默认图标渲染）。 */
  readonly current = signal<Branding | null>(null);
  private loading = false;

  async load(): Promise<void> {
    if (this.loading || this.current()) return;
    this.loading = true;
    try {
      this.current.set(await lastValueFrom(this.http.get<Branding>('/api/branding')));
    } catch {
      // 读不到就保持默认图标：一次请求失败不该让顶栏变成空白方块。
    } finally {
      this.loading = false;
    }
  }

  /** 后台保存成功后直接把新状态推给顶栏。 */
  apply(b: Branding): void {
    this.current.set(b);
  }
}
