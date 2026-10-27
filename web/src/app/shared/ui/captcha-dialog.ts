import { Component, input, output, signal } from '@angular/core';
import { Modal } from './modal';
import { CaptchaWidget } from './captcha-widget';
import type { CaptchaChallenge } from '../../core/auth.service';

/**
 * 人机验证弹窗。
 *
 * 验证不常驻表单：用户点「登录 / 注册 / 发送邮件」时才弹出来，通过后自动继续提交。
 * 常驻的验证组件会把表单撑长、把注意力从主操作上拉走，而绝大多数正常用户
 * 只想快点填完点提交。
 *
 * 两种形态：
 *   - math：弹窗里出一道题，答完 emit 答案
 *   - turnstile / geetest / vaptcha / cap：渲染对应 SDK，通过后 emit token
 */
@Component({
  selector: 'vx-captcha-dialog',
  imports: [Modal, CaptchaWidget],
  template: `
    <vx-modal title="人机验证" [width]="420" (closed)="closed.emit()">
      @if (provider() === 'math') {
        <label class="field">
          <span>{{ challenge()?.a }} + {{ challenge()?.b }} = ?</span>
          <input
            class="input mono"
            inputmode="numeric"
            autocomplete="off"
            autofocus
            [value]="answer()"
            (input)="answer.set($any($event.target).value)"
            (keydown.enter)="submitMath()"
          />
        </label>
        <div class="row" style="margin-top: 14px">
          <button type="button" class="btn btn-solid btn-sm" (click)="submitMath()">继续</button>
        </div>
      } @else {
        <vx-captcha-widget
          [provider]="provider()"
          [siteKey]="siteKey()"
          [endpoint]="endpoint()"
          [action]="action()"
          (solved)="solved.emit({ token: $event })"
        />
      }
    </vx-modal>
  `,
})
export class CaptchaDialog {
  readonly provider = input('math');
  readonly siteKey = input('');
  readonly endpoint = input('');
  readonly challenge = input<CaptchaChallenge | null>(null);
  /** 传给第三方 SDK 的动作名，需与后端校验值一致。 */
  readonly action = input('');

  /** token 是第三方 SDK 的凭证；answer 是算术题的答案（二者只会有其一）。 */
  readonly solved = output<{ token?: string; answer?: string }>();
  readonly closed = output<void>();

  protected readonly answer = signal('');

  protected submitMath(): void {
    const v = this.answer().trim();
    if (!v) return;
    this.solved.emit({ answer: v });
  }
}
