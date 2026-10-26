import { Component, inject, signal } from '@angular/core';
import {
  NavigationCancel,
  NavigationEnd,
  NavigationError,
  NavigationStart,
  Router,
  RouterLink,
  RouterOutlet,
} from '@angular/router';
import { Header } from './layout/header';
import { Tabbar } from './layout/tabbar';

@Component({
  selector: 'app-root',
  imports: [RouterOutlet, RouterLink, Header, Tabbar],
  template: `
    <div class="route-progress" [class.is-on]="navigating()" aria-hidden="true"></div>
    <vx-header />
    <main>
      <router-outlet />
    </main>
    <footer class="footer">
      <span>voidxo 维空</span>
      <span>无广告 不追踪</span>
      <a routerLink="/usage">用量</a>
      <a routerLink="/api-docs">API</a>
    </footer>
    <vx-tabbar />
  `,
})
export class App {
  private readonly router = inject(Router);

  /**
   * 路由切换时的顶部细进度条。
   *
   * 覆盖的是「点了搜索 → 目标页懒加载 chunk 还没下载完」这段空窗：结果页自己的骨架屏
   * 要等组件挂载后才出现，之前那几十到几百毫秒是纯白屏。NavigationEnd 在组件创建前
   * 触发，所以这条线和骨架屏基本是接力、不重叠。
   */
  protected readonly navigating = signal(false);

  constructor() {
    this.router.events.subscribe((e) => {
      if (e instanceof NavigationStart) {
        this.navigating.set(true);
        return;
      }
      if (e instanceof NavigationEnd || e instanceof NavigationCancel || e instanceof NavigationError) {
        this.navigating.set(false);
      }
    });
  }
}
