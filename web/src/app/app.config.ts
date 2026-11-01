import { Injectable, effect, inject, signal } from '@angular/core';
import { ApplicationConfig, provideBrowserGlobalErrorListeners } from '@angular/core';
import { provideHttpClient, withFetch, withInterceptors } from '@angular/common/http';
import {
  provideRouter,
  withComponentInputBinding,
  withRouterConfig,
  withInMemoryScrolling,
  TitleStrategy,
  type RouterStateSnapshot,
} from '@angular/router';
import { Title } from '@angular/platform-browser';
import { routes } from './app.routes';
import { adminTokenInterceptor } from './core/http';
import { currentSiteName } from './core/branding.service';
import { t } from './core/i18n';

/**
 * 搜索页把查询词放进标题：从搜索结果切标签页时标签本身要能区分。
 *
 * 标题文案本身是字典 key（路由表里写 `title.search` 这种），在这里翻——
 * 这样切换语言时标签页标题跟着变，不用重新导航。
 *
 * **必须带 `@Injectable()`**：`useClass` 指向一个没有 DI 元数据的类时，
 * Angular 不会报错，而是继续用默认实现——表现就是「这段代码明明在产物里，
 * 标题却完全没带上站名」。这个坑排查成本很高（页面上没有任何迹象）。
 */
@Injectable({ providedIn: 'root' })
export class AppTitleStrategy extends TitleStrategy {
  private readonly title = inject(Title);

  /** 最近一次导航的标题 key 与搜索词。用 signal 存，是为了让下面的 effect 能重算。 */
  private readonly current = signal<{ key: string; q: string }>({ key: '', q: '' });

  constructor() {
    super();
    // 用 effect 而不是在 updateTitle 里直接 setTitle：切换语言不触发导航，
    // 直接设的话标题会停在旧语言，直到下次点页面。
    effect(() => {
      const { key, q } = this.current();
      // 站名取自后台「站点信息」，改完立刻生效（见 BrandingService）。
      const site = currentSiteName();
      if (q) {
        this.title.setTitle(`${q} - ${site}`);
        return;
      }
      const label = key ? t(key) : '';
      this.title.setTitle(label ? `${label} - ${site}` : site);
    });
  }

  override updateTitle(snapshot: RouterStateSnapshot): void {
    this.current.set({
      key: this.buildTitle(snapshot) ?? '',
      q: new URL(snapshot.url, location.origin).searchParams.get('q') ?? '',
    });
  }
}

export const appConfig: ApplicationConfig = {
  providers: [
    provideBrowserGlobalErrorListeners(),
    // 必须带 @Injectable()，原因见 AppTitleStrategy 的类注释
    { provide: TitleStrategy, useClass: AppTitleStrategy },
    provideRouter(
      routes,
      withComponentInputBinding(),
      withInMemoryScrolling({ scrollPositionRestoration: 'top' }),
      // 点当前页的导航项（比如站在 /contribute 又点一次「提交站点」）默认会被直接忽略，
      // 页面上什么都不会发生。reload 让这种情况也走一遍导航，
      // 「进入提交页就弹须知」才不会漏掉这条路径。
      withRouterConfig({ onSameUrlNavigation: 'reload' }),
    ),
    provideHttpClient(withFetch(), withInterceptors([adminTokenInterceptor])),
  ],
};
