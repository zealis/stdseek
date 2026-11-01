import { Component, inject, signal } from '@angular/core';
import { RouterLink } from '@angular/router';
import { AuthService, type CaptchaChallenge, authErrMsg } from '../../core/auth.service';
import { CaptchaDialog } from '../../shared/ui/captcha-dialog';
import { t } from '../../core/i18n';

/**
 * 找回密码。提交后无论邮箱是否存在都显示同一句话——
 * 后端也是这么做的，前端不能把它拆穿（否则这里就成了账号枚举器）。
 *
 * 人机验证走弹窗：这个接口能触发发信，不设防就是给别人当邮件轰炸机用；
 * 但正常用户一年用不到一次，没必要把验证控件常驻在页面上。
 */
@Component({
  selector: 'vx-forgot-page',
  imports: [RouterLink, CaptchaDialog],
  template: `
    <div class="auth-page">
      <div class="card auth-card">
        <h1 class="auth-title">{{ t('auth.forgot.title') }}</h1>
        <p class="auth-sub">{{ t('auth.forgot.sub') }}</p>

        @if (sent()) {
          <div class="notice notice-accent">{{ sent() }}</div>
          <p class="faint small" style="margin-top: 12px">
            {{ t('auth.forgot.sentHint') }}
          </p>
        } @else {
          <form (submit)="submit($event)">
            <label class="auth-field">
              <span>{{ t('auth.email') }}</span>
              <input
                class="input"
                type="email"
                autocomplete="username"
                required
                [value]="email()"
                (input)="email.set($any($event.target).value)"
              />
            </label>

            @if (error()) {
              <div class="notice notice-error" style="margin-bottom: 12px">{{ error() }}</div>
            }

            <button type="submit" class="btn btn-solid" style="width: 100%" [disabled]="busy()">
              @if (busy()) {
                <span class="spinner"></span>
              } @else {
                {{ t('auth.forgot.submit') }}
              }
            </button>
          </form>
        }

        <div class="auth-links">
          <a routerLink="/login">{{ t('auth.backToLogin') }}</a>
          @if (auth.userAuthOpen()) {
            <a routerLink="/register">{{ t('auth.register') }}</a>
          }
        </div>
      </div>

      @if (capOpen()) {
        <vx-captcha-dialog
          [provider]="capProvider()"
          [siteKey]="tsSiteKey()"
          [endpoint]="capEndpoint()"
          [challenge]="cap()"
          action="forgot"
          (solved)="onCaptchaSolved($event)"
          (closed)="capOpen.set(false)"
        />
      }
    </div>
  `,
})
export class ForgotPage {
  protected readonly auth = inject(AuthService);
  protected readonly t = t;

  protected readonly email = signal('');
  protected readonly busy = signal(false);
  protected readonly error = signal('');
  protected readonly sent = signal('');

  protected readonly capOpen = signal(false);
  protected readonly capEnabled = signal(false);
  protected readonly capProvider = signal('');
  protected readonly capEndpoint = signal('');
  protected readonly tsSiteKey = signal('');
  protected readonly cap = signal<CaptchaChallenge | null>(null);
  protected readonly capAnswer = signal('');
  protected readonly tsToken = signal('');

  constructor() {
    void this.loadCaptcha();
  }

  private async loadCaptcha(): Promise<void> {
    try {
      const r = await this.auth.captcha();
      this.capEnabled.set(!!r.enabled);
      if (!r.enabled) return;
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
    } catch {
      /* 拿不到就当不需要验证 */
    }
  }

  protected async submit(e: Event): Promise<void> {
    e.preventDefault();
    if (this.busy()) return;
    this.error.set('');
    if (this.capEnabled() && !this.capPassed()) {
      await this.openCaptcha();
      return;
    }
    await this.doSubmit();
  }

  private capPassed(): boolean {
    if (this.capProvider() === '' || this.capProvider() === 'math') {
      return this.capAnswer() !== '';
    }
    return this.tsToken() !== '';
  }

  private async openCaptcha(): Promise<void> {
    if (this.capProvider() === '' || this.capProvider() === 'math') {
      try {
        const r = await this.auth.captcha();
        this.cap.set(r.challenge ?? null);
      } catch {
        /* 忽略：后端会给提示 */
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
      const msg = await this.auth.forgot(this.email().trim(), {
        captcha: this.cap(),
        answer: this.capAnswer(),
        turnstileToken: this.tsToken(),
      });
      this.sent.set(msg);
    } catch (err) {
      const msg = authErrMsg(err);
      this.error.set(msg);
      if (msg.includes('人机验证')) {
        this.tsToken.set('');
        this.capAnswer.set('');
        void this.loadCaptcha();
      }
    } finally {
      this.busy.set(false);
    }
  }
}
