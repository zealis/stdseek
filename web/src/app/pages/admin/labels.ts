import { Component, computed, effect, inject, signal } from '@angular/core';
import { AdminApi } from '../../core/admin-api.service';
import { errMsg } from '../../core/http';
import { labelText, type LabelFeedback } from '../../core/api';
import { dayTime, truncate } from '../../shared/format';
import { Pager } from '../../shared/ui/pager';
import { AdminToolbar } from './admin-toolbar';

const SIZE = 10;

// 标签误判反馈列表。规划书红线要求概率性标签「可解释 + 可反馈」，
// 这个页面的作用是评估规则精确率：某个标签误判数一直涨，就该收紧它的判定条件。
const VERDICTS = [
  { value: '', label: '全部' },
  { value: 'false_positive', label: '用户判为误判' },
  { value: 'confirmed', label: '用户确认正确' },
];

/** 标签反馈：误判数据用于调整规则，不会立刻撤销单条页面的标签。 */
@Component({
  selector: 'vx-labels-page',
  imports: [Pager, AdminToolbar],
  template: `
    @if (msg()) {
      <div class="notice notice-error" style="margin-bottom: 12px">{{ msg() }}</div>
    }

    @if (summary().length > 0) {
      <div class="row wrap" style="margin-bottom: 12px">
        <span class="faint small">累计误判</span>
        @for (s of summary(); track s.label) {
          <span class="chip chip-accent">{{ labelText(s.label) }} {{ s.count }}</span>
        }
      </div>
    }

    <vx-admin-toolbar title="标签反馈" (kwSearch)="search($event)">
      <span class="row" style="gap: 6px">
        <span class="faint small">结论</span>
        <select
          class="input select"
          style="width: 150px; height: 32px; font-size: var(--fs-xs)"
          aria-label="按结论筛选"
          [value]="verdict()"
          (change)="verdict.set(selEl.value); page.set(1)"
          #selEl
        >
          @for (v of verdicts; track v.value) {
            <option [value]="v.value" [selected]="v.value === verdict()">{{ v.label }}</option>
          }
        </select>
      </span>
    </vx-admin-toolbar>

    @if (loading()) {
      <div class="loading"><span class="spinner spinner-lg"></span></div>
    } @else {
      <div class="table-wrap">
        <table class="table">
          <thead>
            <tr>
              <th style="width: 28%">页面</th>
              <th style="width: 12%">标签</th>
              <th style="width: 10%">结论</th>
              <th>用户说明</th>
              <th style="width: 12%">时间</th>
            </tr>
          </thead>
          <tbody>
            @for (it of rows(); track it.id) {
              <tr>
                <td data-label="页面">
                  <a [href]="it.url" target="_blank" rel="noreferrer" class="cell-strong">{{ it.domain }}</a>
                  <span class="faint small cell-ellipsis">{{ truncate(it.url, 64) }}</span>
                </td>
                <td data-label="标签"><span class="chip">{{ labelText(it.label) }}</span></td>
                <td data-label="结论">
                  <span class="chip" [class]="it.verdict === 'false_positive' ? 'chip-danger' : 'chip-success'">
                    {{ it.verdict === 'false_positive' ? '误判' : '正确' }}
                  </span>
                </td>
                <td data-label="用户说明">
                  <span [class]="it.note ? '' : 'faint'">{{ it.note || '—' }}</span>
                </td>
                <td data-label="时间"><span class="small muted">{{ dayTime(it.updated_at) }}</span></td>
              </tr>
            } @empty {
              <tr>
                <td colspan="5"><div class="empty">暂无数据</div></td>
              </tr>
            }
          </tbody>
        </table>
      </div>
    }

    <vx-pager [page]="page()" [pages]="pages()" [total]="total()" (pageChange)="goPage($event)" />
  `,
})
export class LabelsPage {
  private readonly api = inject(AdminApi);

  protected readonly verdicts = VERDICTS;
  protected readonly labelText = labelText;
  protected readonly dayTime = dayTime;
  protected readonly truncate = truncate;

  protected readonly rows = signal<LabelFeedback[]>([]);
  protected readonly total = signal(0);
  protected readonly byLabel = signal<Record<string, number>>({});
  protected readonly loading = signal(false);
  protected readonly msg = signal('');
  protected readonly kw = signal('');
  protected readonly verdict = signal('false_positive');
  protected readonly page = signal(1);

  protected readonly pages = computed(() => Math.max(1, Math.ceil(this.total() / SIZE)));

  protected readonly summary = computed(() =>
    Object.entries(this.byLabel())
      .sort((a, b) => b[1] - a[1])
      .map(([label, count]) => ({ label, count })),
  );

  constructor() {
    effect(() => {
      const kw = this.kw();
      const verdict = this.verdict();
      const page = this.page();
      void this.load(kw, verdict, page);
    });
  }

  private async load(kw: string, verdict: string, page: number): Promise<void> {
    this.loading.set(true);
    try {
      const r = await this.api.listLabelFeedback(kw, verdict, page);
      this.rows.set(r.items ?? []);
      this.total.set(r.total);
      this.byLabel.set(r.by_label ?? {});
    } catch (e) {
      this.msg.set(errMsg(e));
    } finally {
      this.loading.set(false);
    }
  }

  protected search(kw: string): void {
    this.kw.set(kw);
    this.page.set(1);
  }

  protected goPage(p: number): void {
    this.page.set(p);
  }
}
