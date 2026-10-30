import { Injectable, inject } from '@angular/core';
import { ApplicationConfig, provideBrowserGlobalErrorListeners } from '@angular/core';
import { provideHttpClient, withFetch, withInterceptors } from '@angular/common/http';
import {
  provideRouter,
  withComponentInputBinding,
  withRouterConfig,
  withInMemoryScrolling,
  withPreloading,
  TitleStrategy,
  type RouterStateSnapshot,
} from '@angular/router';
import { Title } from '@angular/platform-browser';
import { routes } from './app.routes';
import { adminTokenInterceptor } from './core/http';
import { currentSiteName } from './core/branding.service';
import { PreloadPublicOnly } from './core/preload';

/**
 * 搜索页把查询词放进标题：从搜索结果切标签页时标签本身要能区分。
 *
 * **必须带 `@Injectable()`**：`useClass` 指向一个没有 DI 元数据的类时，
 * Angular 不会报错，而是继续用默认实现——表现就是「这段代码明明在产物里，
 * 标题却完全没带上站名」。这个坑排查成本很高（页面上没有任何迹象）。
 */
@Injectable({ providedIn: 'root' })
export class AppTitleStrategy extends TitleStrategy {
  private readonly title = inject(Title);

  override updateTitle(snapshot: RouterStateSnapshot): void {
    const routeTitle = this.buildTitle(snapshot);
    // 站名取自后台「站点信息」，改完立刻生效（见 BrandingService）。
    const site = currentSiteName();
    const q = new URL(snapshot.url, location.origin).searchParams.get('q');
    if (q) {
      this.title.setTitle(`${q} - ${site}`);
      return;
    }
    this.title.setTitle(routeTitle ? `${routeTitle} - ${site}` : site);
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
      // 只预取搜索页，其余全部按需。原来用 PreloadAllModules——
      // 那时「路由一共就几个」，后来后台长到 15 个页面、预算涨到 210KB(gzip)，
      // 首访把整个后台都拖下来了。原因与取舍见 core/preload.ts。
      withPreloading(PreloadPublicOnly),
    ),
    provideHttpClient(withFetch(), withInterceptors([adminTokenInterceptor])),
  ],
};
