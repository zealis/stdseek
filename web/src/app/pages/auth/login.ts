import { Component, inject, signal } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { AuthService, authErrMsg } from '../../core/auth.service';

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

  constructor() {
    // 邮箱验证链接会带 ?verify=ok / failed 回来
    const v = new URLSearchParams(location.search).get('verify');
    if (v === 'ok') this.notice.set('邮箱验证成功');
    else if (v === 'failed') this.error.set('验证链接无效或已过期');
  }

  protected async submit(e: Event): Promise<void> {
    e.preventDefault();
    if (this.busy()) return;
    this.busy.set(true);
    this.error.set('');
    try {
      const u = await this.auth.login(this.email().trim(), this.password(), this.totp());
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
    } finally {
      this.busy.set(false);
    }
  }
}
