import { Injectable } from '@angular/core';
import { type PreloadingStrategy, type Route } from '@angular/router';
import { type Observable, of, timer, mergeMap } from 'rxjs';

/**
 * 预加载策略：只预取访客会打开的页面，管理后台一律不预取。
 *
 * 为什么不用 `PreloadAllModules`：它会在首屏渲染完成后把所有懒加载路由一起下下来——
 * 实测这个站有 37 个 chunk / 210KB(gzip)，其中 21 个是后台页面（白名单、审核队列、
 * 账号管理、备份……）。访客一辈子不会进 /manage，这些字节纯属白下；
 * 更麻烦的是它们会和首页那次 `/api/status` 抢带宽与连接数（HTTP/1.1 每域只有 6 条），
 * 表现就是「首屏已经画出来了，但内容慢半拍才出来」。
 *
 * 剩下的访客页面（帮助、API 文档、用量、提交、隐私、账号）很小，值得预取，
 * 但延后 800ms：让首屏那次 API 先跑完，别和它抢。
 */
@Injectable({ providedIn: 'root' })
export class PreloadPublicOnly implements PreloadingStrategy {
  preload(route: Route, load: () => Observable<unknown>): Observable<unknown> {
    // 后台（含其下所有子路由）永不预取
    if (route.path?.startsWith('manage')) {
      return of(null);
    }
    return timer(800).pipe(mergeMap(() => load()));
  }
}
