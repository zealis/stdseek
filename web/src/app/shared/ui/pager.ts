import { Component, computed, input, output } from '@angular/core';

/**
 * 分页：数字页码窗口（最多 5 页）+ 前后翻页纯文字钮——对齐原型 .vx-pager。
 * 当前页用原型强调色紫罗兰实色（--viz-1）；触屏按钮补到 40px 热区（全局 coarse 规则）。
 */
@Component({
  selector: 'vx-pager',
  template: `
    <div class="pager">
      @if (showTotal()) {
        <span class="pager-total">共 {{ total() }} 条</span>
      }
      <button
        type="button"
        class="pager-btn pager-flat"
        aria-label="上一页"
        [disabled]="page() <= 1"
        (click)="pageChange.emit(page() - 1)"
      >
        上一页
      </button>
      @for (p of pageWindow(); track p) {
        <button
          type="button"
          class="pager-btn"
          [class.is-cur]="p === page()"
          [attr.aria-current]="p === page() ? 'page' : null"
          (click)="pageChange.emit(p)"
        >
          {{ p }}
        </button>
      }
      <button
        type="button"
        class="pager-btn pager-flat"
        aria-label="下一页"
        [disabled]="page() >= pages()"
        (click)="pageChange.emit(page() + 1)"
      >
        下一页
      </button>
    </div>
  `,
})
export class Pager {
  readonly page = input.required<number>();
  readonly pages = input.required<number>();
  readonly total = input.required<number>();
  /** 后台列表显示「共 N 条」；结果页的总数已在计数行展示，不重复。 */
  readonly showTotal = input(false);

  readonly pageChange = output<number>();

  /** 当前页附近的 5 页窗口，起点向两头收敛。 */
  protected readonly pageWindow = computed(() => {
    const pages = this.pages();
    const page = this.page();
    const size = 5;
    const start = Math.max(1, Math.min(page - 2, pages - size + 1));
    const end = Math.min(pages, start + size - 1);
    return Array.from({ length: end - start + 1 }, (_, i) => start + i);
  });
}
