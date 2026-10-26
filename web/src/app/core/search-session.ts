import { Injectable, signal } from '@angular/core';

/**
 * 内存里的当前检索状态，只在「POST 检索」模式下使用。
 *
 * 为什么需要它：GET 模式把 q/page 放在 URL 上，路由参数天然就是状态；POST 模式
 * 地址栏不带任何查询，组件就没有可绑定的来源了。这里用内存信号顶上。
 *
 * 代价是刷新会丢查询——这是 POST 模式的固有取舍，设置项里已写明。
 */
@Injectable({ providedIn: 'root' })
export class SearchSession {
  private readonly _q = signal('');
  private readonly _page = signal(1);

  readonly q = this._q.asReadonly();
  readonly page = this._page.asReadonly();

  /** 提交新查询：页码重置为 1。 */
  setQuery(q: string): void {
    this._q.set(q);
    this._page.set(1);
  }

  setPage(p: number): void {
    this._page.set(p);
  }
}
