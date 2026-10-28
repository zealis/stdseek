import { Component, computed, effect, inject, signal } from '@angular/core';
import { AdminApi } from '../../core/admin-api.service';
import { errMsg } from '../../core/http';
import { categoryText, labelText, type LabelFeedback } from '../../core/api';
import { dayTime, truncate } from '../../shared/format';
import { Pager } from '../../shared/ui/pager';
import { AdminToolbar } from './admin-toolbar';
import { Modal } from '../../shared/ui/modal';

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
  imports: [Pager, AdminToolbar, Modal],
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
              <th style="width: 16%">标签 / 分类</th>
              <th style="width: 10%">结论</th>
              <th>用户说明</th>
              <th style="width: 12%">时间</th>
              <th class="is-right" style="width: 10%">操作</th>
            </tr>
          </thead>
          <tbody>
            @for (it of rows(); track it.id) {
              <tr>
                <td data-label="页面">
                  <a [href]="it.url" target="_blank" rel="noreferrer" class="cell-strong">{{ it.domain }}</a>
                  <span class="faint small cell-ellipsis">{{ truncate(it.url, 64) }}</span>
                </td>
                <td data-label="标签 / 分类">
                  @if (it.kind === 1) {
                    <span class="chip">分类 · {{ categoryText(it.category) }}</span>
                    @if (it.suggest) {
                      <span class="faint small">→ {{ categoryText(it.suggest) }}</span>
                    }
                  } @else {
                    <span class="chip">{{ labelText(it.label) }}</span>
                  }
                </td>
                <td data-label="结论">
                  <span class="chip" [class]="it.verdict === 'false_positive' ? 'chip-danger' : 'chip-success'">
                    {{ it.verdict === 'false_positive' ? '误判' : '正确' }}
                  </span>
                </td>
                <td data-label="用户说明">
                  <span [class]="it.note ? '' : 'faint'">{{ it.note || '—' }}</span>
                </td>
                <td data-label="时间"><span class="small muted">{{ dayTime(it.updated_at) }}</span></td>
                <td class="is-actions" data-nolabel>
                  <button type="button" class="btn btn-link btn-sm" (click)="askEdit(it)">修改</button>
                </td>
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

    @if (editing()) {
      <vx-modal title="修改标签反馈" [width]="420" (closed)="editing.set(null)">
        <label class="field">
          <span>标签</span>
          <select class="input select"
            [value]="editLabel()" (change)="editLabel.set($any($event.target).value)">
            <option value="" [selected]="editLabel() === ''">无</option>
            @for (l of labelOptions; track l) {
              <option [value]="l" [selected]="l === editLabel()">{{ labelText(l) }}</option>
            }
          </select>
        </label>
        <label class="field">
          <span>结论</span>
          <select class="input select"
            [value]="editVerdict()" (change)="editVerdict.set($any($event.target).value)">
            <option value="false_positive" [selected]="editVerdict() === 'false_positive'">
              {{ editLabel() === '' ? '分类判错' : '标签误判' }}
            </option>
            <option value="confirmed" [selected]="editVerdict() === 'confirmed'">
              {{ editLabel() === '' ? '分类正确' : '标签正确' }}
            </option>
          </select>
        </label>
        @if (editErr()) { <p class="small" style="color: var(--danger)">{{ editErr() }}</p> }
        <div footer class="row" style="gap: 8px; justify-content: flex-end">
          <button type="button" class="btn" (click)="editing.set(null)">取消</button>
          <button type="button" class="btn btn-solid" [disabled]="editBusy()" (click)="saveEdit()">
            @if (editBusy()) { <span class="spinner"></span> } @else { 保存 }
          </button>
        </div>
      </vx-modal>
    }
  `,
})
export class LabelsPage {
  private readonly api = inject(AdminApi);

  protected readonly verdicts = VERDICTS;

  // ---- 修改标签 ----
  protected readonly labelOptions = ['ai_suspect', 'ad_heavy', 'personal', 'ugc_platform'];
  protected readonly editing = signal<LabelFeedback | null>(null);
  protected readonly editLabel = signal('ai_suspect');
  protected readonly editVerdict = signal('false_positive');
  protected readonly editBusy = signal(false);
  protected readonly editErr = signal('');

  protected askEdit(it: LabelFeedback): void {
    this.editing.set(it);
    this.editLabel.set(it.label);
    this.editVerdict.set(it.verdict);
    this.editErr.set('');
  }

  protected async saveEdit(): Promise<void> {
    const it = this.editing();
    if (!it) return;
    this.editBusy.set(true);
    this.editErr.set('');
    try {
      // 标签留空 = 标为「无」：这条反馈指向的是站点分类，不是某个标签。
      await this.api.updateLabelFeedback(
        it.id,
        this.editLabel(),
        this.editVerdict() as 'false_positive' | 'confirmed',
      );
      this.editing.set(null);
      await this.load(this.kw(), this.verdict(), this.page());
    } catch (e) {
      this.editErr.set(errMsg(e));
    } finally {
      this.editBusy.set(false);
    }
  }
  protected readonly labelText = labelText;
  protected readonly categoryText = categoryText;
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
