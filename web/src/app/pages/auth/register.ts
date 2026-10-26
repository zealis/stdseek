import { Component, inject, signal } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { AuthService, authErrMsg } from '../../core/auth.service';

/** 注册页。站内第一个注册的账号会自动成为管理员（后端判定，前端只做提示）。 */
@Component({
  selector: 'vx-register-page',
  imports: [RouterLink],
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

  protected async submit(e: Event): Promise<void> {
    e.preventDefault();
    if (this.busy()) return;
    this.error.set('');
    if (this.password() !== this.confirm()) {
      this.error.set('两次输入的密码不一致');
      return;
    }
    this.busy.set(true);
    try {
      const u = await this.auth.register(this.email().trim(), this.password());
      void this.router.navigateByUrl(u.role === 'admin' ? '/manage/stats' : '/');
    } catch (err) {
      this.error.set(authErrMsg(err));
    } finally {
      this.busy.set(false);
    }
  }
}
