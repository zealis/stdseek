import { Injectable } from '@angular/core';

/**
 * 搜索页的「意图预取」。
 *
 * 以前用 PreloadingStrategy + 800ms 定时器预取 /search。问题是定时器到点就开始下载，
 * Lighthouse 把这条依赖链算进关键路径（HTML → main.js → API → search chunk → 子 chunk），
 * 关键路径因此显示成 1.2 秒——而它其实不是首屏必需的东西。
 *
 * 改成「用户真的要搜了才下」：鼠标移到搜索框、键盘聚焦、或在输入框里打第一个字。
 * 真人从聚焦到按下回车至少几百毫秒，足够把 chunk 下完；而只看不搜的访客一字节都不用付。
 */
@Injectable({ providedIn: 'root' })
export class WarmSearch {
  private started = false;

  /** 幂等：多次触发只会真正下载一次。 */
  warm(): void {
    if (this.started) return;
    this.started = true;
    void import('../pages/search');
  }
}
