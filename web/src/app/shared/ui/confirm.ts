import { Component, input, output } from '@angular/core';
import { Modal } from './modal';

/**
 * 二次确认弹窗。
 *
 * 用在「一次点击就造成不可逆后果」的地方：退出登录、删除数据、清理缓存。
 * 目的不是防用户手滑（那是顺带），而是防「页面被诱导点击」——用户看到的确认框
 * 是他自己触发的，而不是某个看不见的 iframe。
 *
 * 文案由调用方给：`desc` 要写「会发生什么」，不要写「确定吗」。
 */
@Component({
  selector: 'vx-confirm',
  imports: [Modal],
  template: `
    <vx-modal [title]="title()" [width]="400" (closed)="cancelled.emit()">
      <p class="small" style="margin: 0">{{ desc() }}</p>

      <div footer class="row" style="gap: 8px; justify-content: flex-end">
        <button type="button" class="btn" (click)="cancelled.emit()">取消</button>
        <button
          type="button"
          class="btn"
          [class.btn-solid]="!danger()"
          [class.btn-danger]="danger()"
          (click)="confirmed.emit()"
        >
          {{ confirmText() }}
        </button>
      </div>
    </vx-modal>
  `,
})
export class Confirm {
  readonly title = input('确认操作');
  /** 说明「点了会发生什么」，不是「确定吗」 */
  readonly desc = input('');
  readonly confirmText = input('确认');
  readonly danger = input(false);

  readonly confirmed = output<void>();
  readonly cancelled = output<void>();
}
