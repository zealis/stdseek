import { AfterViewInit, Component, ElementRef, input, output, viewChild } from '@angular/core';

declare global {
  interface Window {
    turnstile?: {
      render: (el: HTMLElement, opts: Record<string, unknown>) => string;
      reset: (id: string) => void;
      remove: (id: string) => void;
    };
  }
}

let scriptPromise: Promise<void> | null = null;

/**
 * 按需加载 Turnstile 脚本（只加载一次，多个页面共用同一个 Promise）。
 *
 * explicit 而不是 implicit：SPA 里组件是动态插入的，implicit 模式只在首屏扫描
 * `.cf-turnstile`，后插入的 div 不会被处理。
 */
function loadScript(): Promise<void> {
  if (window.turnstile) return Promise.resolve();
  if (scriptPromise) return scriptPromise;
  scriptPromise = new Promise<void>((resolve, reject) => {
    const s = document.createElement('script');
    s.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit';
    s.async = true;
    s.defer = true;
    s.onload = () => resolve();
    s.onerror = () => reject(new Error('Turnstile 脚本加载失败'));
    document.head.appendChild(s);
  });
  return scriptPromise;
}

/**
 * Cloudflare Turnstile 的人机验证组件。
 *
 * 只在服务端配了 site key 时才会被渲染（见登录/注册页的 provider 判断）。
 * 注意：这是本站唯一一个会向第三方发起请求的组件——加载它的脚本与 iframe
 * 会把用户 IP 告诉 Cloudflare。README 的「无追踪」承诺里已经写明这个例外。
 */
@Component({
  selector: 'vx-turnstile',
  template: `<div #box class="turnstile-box"></div>`,
  styles: `
    .turnstile-box {
      margin-bottom: 12px;
      min-height: 65px;
    }
  `,
})
export class TurnstileBox implements AfterViewInit {
  readonly siteKey = input.required<string>();
  /** 验证通过时回传 token；过期或失败回传空串。 */
  readonly solved = output<string>();

  private readonly box = viewChild.required<ElementRef<HTMLElement>>('box');
  private widgetId: string | null = null;

  ngAfterViewInit(): void {
    void this.render();
  }

  private async render(): Promise<void> {
    try {
      await loadScript();
    } catch {
      return; // 脚本加载不出来时静默：提交会因为缺 token 被后端拒绝，用户会看到提示
    }
    const ts = window.turnstile;
    if (!ts) return;
    this.widgetId = ts.render(this.box().nativeElement, {
      sitekey: this.siteKey(),
      callback: (token: string) => this.solved.emit(token),
      'expired-callback': () => this.solved.emit(''),
      'error-callback': () => this.solved.emit(''),
      theme: 'auto',
    });
  }

  /** 提交失败后重新验证（token 是一次性的）。 */
  reset(): void {
    if (this.widgetId && window.turnstile) {
      window.turnstile.reset(this.widgetId);
      this.solved.emit('');
    }
  }
}
