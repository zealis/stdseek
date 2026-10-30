import { Injectable, signal } from '@angular/core';

/**
 * Service Worker 的注册与「有新版本」提示。
 *
 * 为什么自己写而不上 `@angular/service-worker`：那套靠 ngsw.json 的 hashTable 判断更新，
 * 而这个站的 index.html 是服务端注入 SEO/站名/页脚代码的——运行时取到的 HTML 和构建时
 * 算出的哈希永远对不上，会被判成「每次访问都有新版本」。我们只缓存静态资源、
 * 不缓存 HTML，用一个 30 行的 SW 反而更准（见 internal/api/sw.js）。
 *
 * 更新策略是「提示后由用户刷新」：新 SW 装好会停在 waiting，直到用户点页脚那行字。
 * 自动 skipWaiting 的问题是用户可能正在输入搜索词，页面自己刷掉会很恼人。
 */
@Injectable({ providedIn: 'root' })
export class PwaService {
  /** 新版本已就绪、等用户点刷新。 */
  readonly updateReady = signal(false);

  /** 只有「用户点了刷新」引起的 controllerchange 才该 reload，首次安装不该。 */
  private reloading = false;

  register(): void {
    if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return;
    // ng serve 没有 /sw.js；真注册只会留一条 404，还容易让人以为构建坏了。
    if (location.port === '4200') return;

    void navigator.serviceWorker
      .register('/sw.js', { scope: '/' })
      .then((reg) => {
        this.watch(reg);
        // 单页应用长期挂着时浏览器不会主动查更新，回到前台补一次检查。
        document.addEventListener('visibilitychange', () => {
          if (document.visibilityState === 'visible') void reg.update().catch(() => undefined);
        });
      })
      .catch(() => {
        // HTTP 部署、隐私模式、企业策略禁用 SW：站点照常工作，不打扰用户。
      });

    navigator.serviceWorker.addEventListener('controllerchange', () => {
      if (this.reloading) location.reload();
    });
  }

  private watch(reg: ServiceWorkerRegistration): void {
    // 只有「页面已经被旧 SW 控制」时才提示：首次安装完不该跳出一句「有新版本」。
    const flag = (): void => {
      if (reg.waiting && navigator.serviceWorker.controller) this.updateReady.set(true);
    };

    flag();
    reg.addEventListener('updatefound', () => {
      const next = reg.installing;
      if (!next) return;
      next.addEventListener('statechange', () => {
        if (next.state === 'installed') flag();
      });
    });
  }

  /** 用户点了「刷新」：让等待中的 SW 接管，controllerchange 里再真正 reload。 */
  async applyUpdate(): Promise<void> {
    const reg = await navigator.serviceWorker.getRegistration();
    if (!reg?.waiting) {
      location.reload();
      return;
    }
    this.reloading = true;
    reg.waiting.postMessage({ type: 'VX_SKIP_WAITING' });
  }
}
