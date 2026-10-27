import { Component, inject, signal } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { AuthService, type CaptchaChallenge, authErrMsg } from '../../core/auth.service';
import { passkeySupported, serializeAssertion, toRequestOptions } from '../../core/webauthn';

/**
 * 登录页。两步验证码字段是条件出现的：先只让用户填账号密码，
 * 后端回「需要两步验证码」时才展开——否则每个用户都被要求填一个可能没开的字段。
 */
@Component({
  selector: 'vx-login-page',
  imports: [RouterLink],
  template: `
    <div class="auth-page">
      <div class="card auth-card">
        <h1 class="auth-title">登录</h1>
        <p class="auth-sub">用注册邮箱登录。管理员登录后可以进入管理后台。</p>

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
              autocomplete="current-password"
              required
              [value]="password()"
              (input)="password.set($any($event.target).value)"
            />
          </label>

          @if (cap()) {
            <label class="auth-field">
              <span>人机验证：{{ cap()!.a }} + {{ cap()!.b }} = ?</span>
              <input
                class="input mono"
                inputmode="numeric"
                autocomplete="off"
                [value]="capAnswer()"
                (input)="capAnswer.set($any($event.target).value)"
              />
            </label>
          }

          @if (needTotp()) {
            <label class="auth-field">
              <span>两步验证码</span>
              <input
                class="input mono"
                inputmode="numeric"
                maxlength="6"
                autocomplete="one-time-code"
                [value]="totp()"
                (input)="totp.set($any($event.target).value)"
              />
            </label>
          }

          @if (notice()) {
            <div class="notice notice-accent" style="margin-bottom: 12px">{{ notice() }}</div>
          }
          @if (error()) {
            <div class="notice notice-error" style="margin-bottom: 12px">{{ error() }}</div>
          }

          <button type="submit" class="btn btn-solid" style="width: 100%" [disabled]="busy()">
            @if (busy()) {
              <span class="spinner"></span>
            } @else {
              登录
            }
          </button>
        </form>

        @if (pkSupported) {
          <button
            type="button"
            class="btn btn-outline"
            style="width: 100%; margin-top: 8px"
            [disabled]="busy()"
            (click)="loginWithPasskey()"
          >
            用通行密钥登录
          </button>
        }

        <div class="auth-links">
          <a routerLink="/forgot">忘记密码</a>
          <a routerLink="/register">注册账号</a>
        </div>
      </div>
    </div>
  `,
})
export class LoginPage {
  private readonly auth = inject(AuthService);
  private readonly router = inject(Router);

  protected readonly email = signal('');
  protected readonly password = signal('');
  protected readonly totp = signal('');
  protected readonly needTotp = signal(false);
  protected readonly busy = signal(false);
  protected readonly error = signal('');
  protected readonly notice = signal('');
  protected readonly cap = signal<CaptchaChallenge | null>(null);
  protected readonly capAnswer = signal('');
  protected readonly pkSupported = passkeySupported();

  constructor() {
    // 邮箱验证链接会带 ?verify=ok / failed 回来
    const v = new URLSearchParams(location.search).get('verify');
    if (v === 'ok') this.notice.set('邮箱验证成功');
    else if (v === 'failed') this.error.set('验证链接无效或已过期');
    void this.loadCaptcha();
  }

  /** 后台开着 require_captcha 才会拿到题目，否则什么都不显示。 */
  private async loadCaptcha(): Promise<void> {
    try {
      const r = await this.auth.captcha();
      this.cap.set(r.enabled ? (r.challenge ?? null) : null);
      this.capAnswer.set('');
    } catch {
      this.cap.set(null);
    }
  }

  /**
   * 通行密钥登录。必须先有邮箱：服务端要靠它找出这个账号登记的密钥列表
   * （不是所有密钥都是 resident key，不能凭空发现）。
   */
  protected async loginWithPasskey(): Promise<void> {
    const email = this.email().trim();
    if (!email) {
      this.error.set('先填邮箱，再用通行密钥登录');
      return;
    }
    this.busy.set(true);
    this.error.set('');
    try {
      const { session, publicKey } = await this.auth.passkeyLoginBegin(email);
      const cred = (await navigator.credentials.get({
        publicKey: toRequestOptions(publicKey),
      })) as PublicKeyCredential | null;
      if (!cred) {
        this.error.set('没有拿到通行密钥响应');
        return;
      }
      const u = await this.auth.passkeyLoginFinish(session, serializeAssertion(cred));
      const redirect = new URLSearchParams(location.search).get('redirect');
      if (redirect && redirect.startsWith('/')) {
        void this.router.navigateByUrl(redirect);
        return;
      }
      void this.router.navigateByUrl(u.role === 'admin' ? '/manage/stats' : '/');
    } catch (e) {
      this.error.set(e instanceof Error ? e.message : '通行密钥登录失败');
    } finally {
      this.busy.set(false);
    }
  }

  protected async submit(e: Event): Promise<void> {
    e.preventDefault();
    if (this.busy()) return;
    this.busy.set(true);
    this.error.set('');
    try {
      const u = await this.auth.login(this.email().trim(), this.password(), this.totp(), {
        captcha: this.cap(),
        answer: this.capAnswer(),
      });
      const redirect = new URLSearchParams(location.search).get('redirect');
      if (redirect && redirect.startsWith('/')) {
        void this.router.navigateByUrl(redirect);
        return;
      }
      void this.router.navigateByUrl(u.role === 'admin' ? '/manage/stats' : '/');
    } catch (err) {
      const msg = authErrMsg(err);
      this.error.set(msg);
      // 后端说缺验证码时把字段展开，并让用户重试
      if (msg.includes('两步验证')) this.needTotp.set(true);
      // 验证码出错时换一道新题，避免用户对着同一道题反复试
      if (msg.includes('人机验证')) void this.loadCaptcha();
    } finally {
      this.busy.set(false);
    }
  }
}
