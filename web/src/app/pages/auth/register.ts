import { Component, inject, signal } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { AuthService, type CaptchaChallenge, authErrMsg } from '../../core/auth.service';
import { CaptchaDialog } from '../../shared/ui/captcha-dialog';

/** 注册页。站内第一个注册的账号会自动成为管理员（后端判定，前端只做提示）。 */
@Component({
  selector: 'vx-register-page',
  imports: [RouterLink, CaptchaDialog],
  template: `
    <div class="auth-page">
      <div class="card auth-card">
        <h1 class="auth-title">注册</h1>
        <p class="auth-sub">只需要邮箱和密码，登录后可管理自己的账号。</p>

        <form (submit)="submit($event)">
          <label class="auth-field">
            <span>邮箱</span>
            <input
              class="input"
              type="email"
              autocomplete="username"
              required
              [value]="email()"
              (input)="email.set($any($event.target).value)"
            />
          </label>

          <label class="auth-field">
            <span>密码</span>
            <input
              class="input"
              type="password"
              autocomplete="new-password"
              required
              [value]="password()"
              (input)="password.set($any($event.target).value)"
            />
          </label>

          <label class="auth-field">
            <span>确认密码</span>
            <input
              class="input"
              type="password"
              autocomplete="new-password"
              required
              [value]="confirm()"
              (input)="confirm.set($any($event.target).value)"
            />
          </label>

          <p class="faint small" style="margin: -2px 0 12px">至少 8 位，长度比复杂度更管用</p>

          @if (error()) {
            <div class="notice notice-error" style="margin-bottom: 12px">{{ error() }}</div>
          }

          <button type="submit" class="btn btn-solid" style="width: 100%" [disabled]="busy()">
            @if (busy()) {
              <span class="spinner"></span>
            } @else {
              注册
            }
          </button>
        </form>

        <div class="auth-links">
          <span></span>
          <a routerLink="/login">已有账号，去登录</a>
        </div>
      </div>

      @if (capOpen()) {
        <vx-captcha-dialog
          [provider]="capProvider()"
          [siteKey]="tsSiteKey()"
          [endpoint]="capEndpoint()"
          [challenge]="cap()"
          (solved)="onCaptchaSolved($event)"
          (closed)="capOpen.set(false)"
        />
      }
    </div>
  `,
})
export class RegisterPage {
  private readonly auth = inject(AuthService);
  private readonly router = inject(Router);

  protected readonly email = signal('');
  protected readonly password = signal('');
  protected readonly confirm = signal('');
  protected readonly busy = signal(false);
  protected readonly error = signal('');
  protected readonly cap = signal<CaptchaChallenge | null>(null);
  protected readonly capAnswer = signal('');
  protected readonly capOpen = signal(false);
  protected readonly capEnabled = signal(false);
  protected readonly tsSiteKey = signal('');
  protected readonly capProvider = signal('');
  protected readonly capEndpoint = signal('');
  protected readonly tsToken = signal('');

  constructor() {
    void this.loadCaptcha();
  }

  private async loadCaptcha(): Promise<void> {
    try {
      const r = await this.auth.captcha();
      this.capEnabled.set(!!r.enabled);
      if (!r.enabled) {
        this.cap.set(null);
        this.tsSiteKey.set('');
        return;
      }
      // 配了 Cloudflare 就渲染官方组件，否则回退内置算术题
      if (r.provider && r.provider !== 'math') {
        this.capProvider.set(r.provider);
        this.tsSiteKey.set(r.site_key ?? '');
        this.capEndpoint.set((r as { endpoint?: string }).endpoint ?? '');
        this.cap.set(null);
      } else {
        this.capProvider.set('math');
        this.cap.set(r.challenge ?? null);
        this.tsSiteKey.set('');
      }
      this.capAnswer.set('');
    } catch {
      this.cap.set(null);
    }
  }

  protected async submit(e: Event): Promise<void> {
    e.preventDefault();
    if (this.busy()) return;
    this.error.set('');
    if (this.password() !== this.confirm()) {
      this.error.set('两次输入的密码不一致');
      return;
    }
    if (this.capEnabled() && !this.capPassed()) {
      await this.openCaptcha();
      return;
    }
    await this.doSubmit();
  }

  private capPassed(): boolean {
    const p = this.capProvider();
    if (p === '' || p === 'math') return this.capAnswer() !== '';
    return this.tsToken() !== '';
  }

  private async openCaptcha(): Promise<void> {
    if (this.capProvider() === '' || this.capProvider() === 'math') {
      try {
        const r = await this.auth.captcha();
        this.cap.set(r.challenge ?? null);
      } catch {
        /* 忽略：让后端给提示 */
      }
      this.capAnswer.set('');
    } else {
      this.tsToken.set('');
    }
    this.capOpen.set(true);
  }

  protected onCaptchaSolved(r: { token?: string; answer?: string }): void {
    if (r.token) this.tsToken.set(r.token);
    if (r.answer) this.capAnswer.set(r.answer);
    this.capOpen.set(false);
    void this.doSubmit();
  }

  private async doSubmit(): Promise<void> {
    this.busy.set(true);
    try {
      const u = await this.auth.register(this.email().trim(), this.password(), {
        captcha: this.cap(),
        answer: this.capAnswer(),
        turnstileToken: this.tsToken(),
      });
      void this.router.navigateByUrl(u.role === 'admin' ? '/manage/stats' : '/');
    } catch (err) {
      const msg = authErrMsg(err);
      this.error.set(msg);
      if (msg.includes('人机验证')) {
        this.tsToken.set('');
        void this.loadCaptcha();
      }
    } finally {
      this.busy.set(false);
    }
  }
}
