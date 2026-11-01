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
import { PwaService } from './core/pwa.service';
import { AboutDialog } from './shared/ui/about-dialog';
import { t } from './core/i18n';

@Component({
  selector: 'app-root',
  imports: [RouterOutlet, RouterLink, Header, Tabbar, AboutDialog],
  template: `
    <div class="route-progress" [class.is-on]="navigating()" aria-hidden="true"></div>
    <vx-header />
    <main>
      <router-outlet />
    </main>
    <footer class="footer">
      <span>© {{ year }} StdSeek</span>
      <span>{{ t('footer.tagline') }}</span>
      <a routerLink="/help">{{ t('nav.help') }}</a>
      <a routerLink="/usage">{{ t('nav.usage') }}</a>
      <a routerLink="/api-docs">API</a>
      <a routerLink="/privacy">{{ t('nav.privacy') }}</a>
      <button type="button" class="footer-btn" (click)="aboutOpen.set(true)">
        {{ t('about.entry') }}
      </button>
      @if (pwa.updateReady()) {
        <button type="button" class="footer-update" (click)="pwa.applyUpdate()">
          有新版本 · 刷新
        </button>
      }
    </footer>

    <!-- 「关于」懒加载：它有一百多个版本的日志，放进主包等于给每个访客加下载量 -->
    @defer (when aboutOpen()) {
      <vx-about (closed)="aboutOpen.set(false)" />
    }

    <vx-tabbar />
  `,
})
export class App {
  private readonly router = inject(Router);
  protected readonly pwa = inject(PwaService);
  protected readonly t = t;
  /** 关于弹窗：点了才加载（见模板里的 @defer）。 */
  protected readonly aboutOpen = signal(false);

  /**
   * 路由切换时的顶部细进度条。
   *
   * 覆盖的是「点了搜索 → 目标页懒加载 chunk 还没下载完」这段空窗：结果页自己的骨架屏
   * 要等组件挂载后才出现，之前那几十到几百毫秒是纯白屏。NavigationEnd 在组件创建前
   * 触发，所以这条线和骨架屏基本是接力、不重叠。
   */
  protected readonly navigating = signal(false);

  /** 版权年份跟着运行时的时钟走，不写死——省得每年改一次。 */
  protected readonly year = new Date().getFullYear();

  constructor() {
    // 只在浏览器里注册；开发服务器、HTTP 部署、隐私模式下会自动跳过。
    this.pwa.register();
    this.router.events.subscribe((e) => {
      if (e instanceof NavigationStart) {
        this.navigating.set(true);
        return;
      }
      if (
        e instanceof NavigationEnd ||
        e instanceof NavigationCancel ||
        e instanceof NavigationError
      ) {
        this.navigating.set(false);
      }
    });
  }
}
