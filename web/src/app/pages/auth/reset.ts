import { Component, inject, input, signal } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { AuthService, authErrMsg } from '../../core/auth.service';

/** 重置密码。token 从邮件链接的 ?token= 来（withComponentInputBinding 直接绑成 input）。 */
@Component({
  selector: 'vx-reset-page',
  imports: [RouterLink],
  template: `
    <div class="auth-page">
      <div class="card auth-card">
        <h1 class="auth-title">设置新密码</h1>

        @if (!token()) {
          <div class="notice notice-error">链接缺少参数，请从邮件里重新打开。</div>
          <div class="auth-links">
            <a routerLink="/forgot">重新发送邮件</a>
          </div>
        } @else if (done()) {
          <div class="notice notice-accent">{{ done() }}</div>
          <div class="auth-links">
            <span></span>
            <a routerLink="/login">去登录</a>
          </div>
        } @else {
          <p class="auth-sub">至少 8 位。设置成功后所有设备都需要重新登录。</p>
          <form (submit)="submit($event)">
            <label class="auth-field">
              <span>新密码</span>
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
              <span>确认新密码</span>
              <input
                class="input"
                type="password"
                autocomplete="new-password"
                required
                [value]="confirm()"
                (input)="confirm.set($any($event.target).value)"
              />
            </label>

            @if (error()) {
              <div class="notice notice-error" style="margin-bottom: 12px">{{ error() }}</div>
            }

            <button type="submit" class="btn btn-solid" style="width: 100%" [disabled]="busy()">
              @if (busy()) {
                <span class="spinner"></span>
              } @else {
                保存新密码
              }
            </button>
          </form>
        }
      </div>
    </div>
  `,
})
export class ResetPage {
  private readonly auth = inject(AuthService);
  private readonly router = inject(Router);

  /** 由路由 ?token= 注入 */
  readonly token = input('');

  protected readonly password = signal('');
  protected readonly confirm = signal('');
  protected readonly busy = signal(false);
  protected readonly error = signal('');
  protected readonly done = signal('');

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
      const msg = await this.auth.reset(this.token(), this.password());
      this.done.set(msg);
      // 3 秒后自动去登录页，省一次点击
      setTimeout(() => void this.router.navigateByUrl('/login'), 3000);
    } catch (err) {
      this.error.set(authErrMsg(err));
    } finally {
      this.busy.set(false);
    }
  }
}
