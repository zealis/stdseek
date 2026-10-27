import { AfterViewInit, Component, ElementRef, input, output, viewChild } from '@angular/core';

/**
 * 人机验证的第三方组件容器。按 provider 分派到各家 SDK。
 *
 * 为什么放一个组件里而不是四个：它们的对外契约完全一样——渲染一个验证控件、
 * 用户完成验证后回传 token。四个文件会有四份几乎相同的脚本加载逻辑。
 *
 * 注意：这些脚本都会把用户 IP 暴露给对应厂商，README 的「不追踪」承诺里已说明。
 */
@Component({
  selector: 'vx-captcha-widget',
  template: `<div #box class="cap-widget"></div>`,
  styles: `
    .cap-widget {
      margin-bottom: 12px;
      min-height: 65px;
    }
  `,
})
export class CaptchaWidget implements AfterViewInit {
  readonly provider = input.required<string>();
  readonly siteKey = input.required<string>();
  /** 只有 Cap 用：自托管实例地址 */
  readonly endpoint = input('');

  /** 验证通过时回传 token（Geetest 是 JSON 字符串，其余是原样 token）。 */
  readonly solved = output<string>();

  private readonly box = viewChild.required<ElementRef<HTMLElement>>('box');

  ngAfterViewInit(): void {
    void this.render();
  }

  private async render(): Promise<void> {
    switch (this.provider()) {
      case 'turnstile':
        return this.renderTurnstile();
      case 'geetest':
        return this.renderGeetest();
      case 'vaptcha':
        return this.renderVaptcha();
      case 'cap':
        return this.renderCap();
    }
  }

  private async renderTurnstile(): Promise<void> {
    await loadScript('https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit');
    const ts = (window as any).turnstile;
    if (!ts) return;
    ts.render(this.box().nativeElement, {
      sitekey: this.siteKey(),
      callback: (token: string) => this.solved.emit(token),
      'expired-callback': () => this.solved.emit(''),
      'error-callback': () => this.solved.emit(''),
      theme: 'auto',
    });
  }

  private async renderGeetest(): Promise<void> {
    await loadScript('https://static.geetest.com/v4/gt4.js');
    const init = (window as any).initGeetest4;
    if (!init) return;
    const box = this.box().nativeElement;
    init({ captchaId: this.siteKey(), product: 'popup', nativeButton: { width: '100%' } }, (obj: any) => {
      obj.appendTo(box);
      obj.onSuccess(() => {
        // 极验的回调给四个字段，服务端要校验签名，所以整包序列化后传过去；
        // captcha_id 也塞进去：服务端拼校验地址时要它。
        const v = obj.getValidate() ?? {};
        this.solved.emit(JSON.stringify({ ...v, captcha_id: this.siteKey() }));
      });
      obj.onError(() => this.solved.emit(''));
      obj.onClose(() => this.solved.emit(''));
    });
  }

  private async renderVaptcha(): Promise<void> {
    await loadScript('https://v-cn.vaptcha.com/v3.js');
    const factory = (window as any).vaptcha;
    if (typeof factory !== 'function') return;
    const box = this.box().nativeElement;
    try {
      const obj = await factory({
        vid: this.siteKey(),
        mode: 'popup',
        container: box,
        // 弹窗按钮的样式尽量贴近本站
        style: 'dark',
      });
      obj.render();
      obj.on('pass', () => this.solved.emit(obj.getToken()));
    } catch {
      // 域名未在上述厂商后台登记时这里会失败，页面上不会出现按钮
      this.solved.emit('');
    }
  }

  private async renderCap(): Promise<void> {
    await loadScript('https://cdn.jsdelivr.net/npm/@cap.js/widget');
    const el = document.createElement('cap-widget');
    el.setAttribute('data-cap-api-endpoint', this.endpoint().replace(/\/$/, '') + '/');
    el.addEventListener('solve', (e: Event) => {
      const detail = (e as CustomEvent<{ token: string }>).detail;
      this.solved.emit(detail?.token ?? '');
    });
    this.box().nativeElement.appendChild(el);
  }
}

/** 脚本只加载一次：登录页和注册页会先后进来，重复插入会触发 SDK 重复初始化。 */
const scripts = new Map<string, Promise<void>>();

function loadScript(src: string): Promise<void> {
  const cached = scripts.get(src);
  if (cached) return cached;
  const p = new Promise<void>((resolve) => {
    const s = document.createElement('script');
    s.src = src;
    s.async = true;
    s.defer = true;
    s.onload = () => resolve();
    // 加载失败也 resolve：调用方拿不到 SDK 就不渲染控件，
    // 提交时会被服务端拒绝并提示，比卡在 loading 好。
    s.onerror = () => resolve();
    document.head.appendChild(s);
  });
  scripts.set(src, p);
  return p;
}
