import { Component, input, output, signal } from '@angular/core';

export type BatchAction = { label: string; danger?: boolean; run: () => void | Promise<void> };

/**
 * 后台列表的统一工具条：标题 + 关键词搜索 + 批量操作 + 额外内容（筛选/新建按钮）。
 * 各页只提供差异部分，不重复搭一遍。
 */
@Component({
  selector: 'vx-admin-toolbar',
  template: `
    <div class="toolbar">
      <span class="toolbar-title">{{ title() }}</span>
      <form class="row" style="gap: 4px" (submit)="search($event)">
        <input
          class="input"
          type="search"
          placeholder="搜索域名或关键词"
          aria-label="搜索域名或关键词"
          [value]="kw()"
          (input)="kw.set(kwEl.value)"
          #kwEl
        />
        <button type="submit" class="btn btn-outline">搜索</button>
      </form>
      @if (actions().length > 0 && count() > 0) {
        <span class="batch">
          <button
            type="button"
            class="btn btn-outline"
            (click)="open.set(!open())"
          >
            批量操作（{{ count() }}）
          </button>
          @if (open()) {
            <span class="batch-menu">
              @for (a of actions(); track a.label) {
                <button
                  type="button"
                  class="batch-item"
                  [class.is-danger]="a.danger"
                  (click)="open.set(false); void a.run()"
                >
                  {{ a.label }}
                </button>
              }
            </span>
          }
        </span>
      }
      <ng-content />
    </div>
  `,
})
export class AdminToolbar {
  readonly title = input.required<string>();
  readonly count = input(0);
  readonly actions = input<BatchAction[]>([]);

  /** 回车或点搜索时触发，参数为关键词。 */
  readonly kwSearch = output<string>();

  protected readonly kw = signal('');
  protected readonly open = signal(false);

  protected search(event: Event): void {
    event.preventDefault();
    this.kwSearch.emit(this.kw().trim());
  }
}
