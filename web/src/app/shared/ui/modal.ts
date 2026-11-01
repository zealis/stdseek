import {
  Component,
  DestroyRef,
  ElementRef,
  afterNextRender,
  inject,
  input,
  output,
  viewChild,
} from '@angular/core';
import { Icon } from './icon';
import { t } from '../../core/i18n';

/**
 * 弹层（DESIGN.md §10）：移动端底部弹层，≥640px 居中卡（形态切换在全局 CSS）。
 * 焦点管理：打开时移入弹层、Tab 圈定在弹层内、关闭后归还触发元素；
 * Esc 与点击 scrim 都能关闭。返回 false 的 backdrop 只负责通知，由调用方关状态。
 */
@Component({
  selector: 'vx-modal',
  imports: [Icon],
  template: `
    <div class="overlay" (click)="onScrim($event)">
      <div
        #dialog
        class="modal"
        role="dialog"
        aria-modal="true"
        [attr.aria-label]="title()"
        tabindex="-1"
        [style.max-width]="width() ? width() + 'px' : null"
        (click)="$event.stopPropagation()"
        (keydown)="onKeydown($event)"
      >
        <div class="modal-head">
          <h2 class="modal-title">{{ title() }}</h2>
          @if (desc()) {
            <p class="modal-desc">{{ desc() }}</p>
          }
          <button
            type="button"
            class="modal-x"
            [attr.aria-label]="t('common.close')"
            (click)="closed.emit()"
          >
            <vx-icon name="x" [size]="16" />
          </button>
        </div>
        <div class="modal-body">
          <ng-content />
        </div>
        <div class="modal-foot">
          <ng-content select="[footer]" />
        </div>
      </div>
    </div>
  `,
})
export class Modal {
  protected readonly t = t;
  readonly title = input.required<string>();
  readonly desc = input('');
  readonly width = input(0);

  readonly closed = output<void>();

  private readonly dialog = viewChild.required<ElementRef<HTMLElement>>('dialog');
  private restoreFocusTo: HTMLElement | null = null;

  constructor() {
    afterNextRender(() => {
      this.restoreFocusTo = document.activeElement as HTMLElement | null;
      this.dialog().nativeElement.focus();
    });
    inject(DestroyRef).onDestroy(() => {
      this.restoreFocusTo?.focus();
    });
  }

  onScrim(event: MouseEvent): void {
    if (event.target === event.currentTarget) this.closed.emit();
  }

  onKeydown(event: KeyboardEvent): void {
    if (event.key === 'Escape') {
      event.stopPropagation();
      this.closed.emit();
      return;
    }
    // Tab 圈定：焦点滑出弹层两端时绕回（简单实现，够用且不引依赖）
    if (event.key === 'Tab') {
      const root = this.dialog().nativeElement;
      const focusables = Array.from(
        root.querySelectorAll<HTMLElement>(
          'a[href], button:not(:disabled), input:not(:disabled), select, textarea, [tabindex]:not([tabindex="-1"])',
        ),
      ).filter((el) => el.offsetParent !== null);
      if (focusables.length === 0) return;
      const first = focusables[0];
      const last = focusables[focusables.length - 1];
      const active = document.activeElement;
      if (event.shiftKey && (active === first || active === root)) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && active === last) {
        event.preventDefault();
        first.focus();
      }
    }
  }
}
