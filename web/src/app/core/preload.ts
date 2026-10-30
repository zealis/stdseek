import { Injectable } from '@angular/core';
import { type PreloadingStrategy, type Route } from '@angular/router';
import { type Observable, of, timer, mergeMap } from 'rxjs';

/**
 * 预加载策略：只预取「从落地页直接会去的那一个页面」，其余一律按需。
 *
 * 为什么不用 `PreloadAllModules`：它会在首屏渲染完成后把所有懒加载路由一起下下来。
 * 实测本站 36 个 chunk / 210KB(gzip)，其中 12 个是管理后台（61KB）——
 * 访客一辈子不会进 /manage，这些字节在手机流量上纯属白烧，
 * 还要和首页那次 `/api/status` 抢连接（HTTP/1.1 每域只有 6 条）。
 *
 * 为什么只留 `/search`：首页上真正指向别处的只有搜索这一条路，
 * 帮助 / API 文档 / 用量 / 隐私这些都在页脚，首访点进去的概率很低；
 * 而预取它们要额外 45KB。少预取一个页面，代价只是「点进去等一次往返」
 * （chunk 各 2~7KB），比让每个访客先下 45KB 划算。
 *
 * 800ms 的延迟是为了让首屏那次 API 先跑完——预取跟着抢带宽，
 * 表现就是「页面画出来了，数字慢半拍」。
 */
@Injectable({ providedIn: 'root' })
export class PreloadPublicOnly implements PreloadingStrategy {
  /** 唯一值得预取的路径：首页的搜索框提交后就去这里。 */
  private static readonly WARM = new Set(['search']);

  preload(route: Route, load: () => Observable<unknown>): Observable<unknown> {
    if (!PreloadPublicOnly.WARM.has(route.path ?? '')) {
      return of(null);
    }
    return timer(800).pipe(mergeMap(() => load()));
  }
}
