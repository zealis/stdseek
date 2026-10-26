import { inject } from '@angular/core';
import { ApplicationConfig, provideBrowserGlobalErrorListeners } from '@angular/core';
import { provideHttpClient, withFetch, withInterceptors } from '@angular/common/http';
import {
  PreloadAllModules,
  provideRouter,
  withComponentInputBinding,
  withInMemoryScrolling,
  withPreloading,
  TitleStrategy,
  type RouterStateSnapshot,
} from '@angular/router';
import { Title } from '@angular/platform-browser';
import { routes } from './app.routes';
import { adminTokenInterceptor } from './core/http';

const SITE = 'voidxo 维空';

/** 搜索页把查询词放进标题：从搜索结果切标签页时标签本身要能区分。 */
export class AppTitleStrategy extends TitleStrategy {
  private readonly title = inject(Title);

  override updateTitle(snapshot: RouterStateSnapshot): void {
    const routeTitle = this.buildTitle(snapshot);
    const q = new URL(snapshot.url, location.origin).searchParams.get('q');
    if (q) {
      this.title.setTitle(`${q} - ${SITE}`);
      return;
    }
    this.title.setTitle(routeTitle ? `${routeTitle} - ${SITE}` : SITE);
  }
}

export const appConfig: ApplicationConfig = {
  providers: [
    provideBrowserGlobalErrorListeners(),
    provideRouter(
      routes,
      withComponentInputBinding(),
      withInMemoryScrolling({ scrollPositionRestoration: 'top' }),
      // 首屏之后空闲时把所有懒加载 chunk 拉下来：路由一共就几个、chunk 各 2~4KB，
      // 换页时不用再等下载。这是 SPA 版的「悬停预取」，比按需预取更省事也更快。
      withPreloading(PreloadAllModules),
    ),
    { provide: TitleStrategy, useClass: AppTitleStrategy },
    provideHttpClient(withFetch(), withInterceptors([adminTokenInterceptor])),
  ],
};
