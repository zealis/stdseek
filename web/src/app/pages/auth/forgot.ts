import { Component, inject, signal } from '@angular/core';
import { RouterLink } from '@angular/router';
import { AuthService, authErrMsg } from '../../core/auth.service';

/**
 * 忘记密码。提交后无论邮箱是否存在都显示同一句话——
 * 后端也是这么做的，前端不能把它拆穿（否则这里就成了账号枚举器）。
 */
@Component({
  selector: 'vx-forgot-page',
  imports: [RouterLink],
  template: `
    <div class="auth-page">
      <div class="card auth-card">
        <h1 class="auth-title">找回密码</h1>
        <p class="auth-sub">输入注册邮箱，我们会发一封带重置链接的邮件。</p>

        @if (sent()) {
          <div class="notice notice-accent">{{ sent() }}</div>
          <p class="faint small" style="margin-top: 12px">
            没收到？检查垃圾邮件，或确认服务端已经配置 SMTP（未配置时邮件只会打印在服务端日志里）。
          </p>
        } @else {
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

            @if (error()) {
              <div class="notice notice-error" style="margin-bottom: 12px">{{ error() }}</div>
            }

            <button type="submit" class="btn btn-solid" style="width: 100%" [disabled]="busy()">
              @if (busy()) {
                <span class="spinner"></span>
              } @else {
                发送重置邮件
              }
            </button>
          </form>
        }

        <div class="auth-links">
          <a routerLink="/login">返回登录</a>
          <a routerLink="/register">注册账号</a>
        </div>
      </div>
    </div>
  `,
})
export class ForgotPage {
  private readonly auth = inject(AuthService);

  protected readonly email = signal('');
  protected readonly busy = signal(false);
  protected readonly error = signal('');
  protected readonly sent = signal('');

  protected async submit(e: Event): Promise<void> {
    e.preventDefault();
    if (this.busy()) return;
    this.busy.set(true);
    this.error.set('');
    try {
      const msg = await this.auth.forgot(this.email().trim());
      this.sent.set(msg);
    } catch (err) {
      this.error.set(authErrMsg(err));
    } finally {
      this.busy.set(false);
    }
  }
}
