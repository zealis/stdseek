import { Component, computed, effect, inject, signal } from '@angular/core';
import { AdminApi } from '../../core/admin-api.service';
import { errMsg } from '../../core/http';
import { CATEGORIES, categoryText, labelText, type LabelFeedback } from '../../core/api';
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

/** 用户反馈：标签与分类的误判数据用于调整规则，不会立刻撤销单条页面的标签。 */
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

    <vx-admin-toolbar title="用户反馈" (kwSearch)="search($event)">
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
                    <span class="chip">
                      {{ it.category ? '分类 · ' + categoryText(it.category) : '分类' }}
                    </span>
                    @if (it.suggest) {
                      <span class="faint small">→ {{ categoryText(it.suggest) }}</span>
                    }
                  } @else {
                    <span class="chip">{{ labelText(it.label) }}</span>
                    @if (it.no_label) {
                      <span class="faint small">→ 无</span>
                    }
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
      <!-- 改什么由这条反馈本身决定：标签反馈只改标签，分类反馈只改分类。
           把「无」放进标签下拉会让改标签错误时突然被要求填分类——
           反馈指向什么是事实，不该由下拉框顺手改写。 -->
      <vx-modal title="修改反馈" [width]="460" (closed)="editing.set(null)">
        @if (editKind() === 1) {
          <label class="field">
            <span>站点分类</span>
            <select class="input select"
              [value]="editCategory()" (change)="editCategory.set($any($event.target).value)">
              <option value="" [selected]="editCategory() === ''">请选择</option>
              @for (c of categories; track c.value) {
                <option [value]="c.value" [selected]="c.value === editCategory()">{{ c.label }}</option>
              }
            </select>
          </label>
          <label class="field">
            <span>认为应该是</span>
            <select class="input select"
              [value]="editSuggest()" (change)="editSuggest.set($any($event.target).value)">
              <option value="" [selected]="editSuggest() === ''">未指定</option>
              @for (c of categories; track c.value) {
                <option [value]="c.value" [selected]="c.value === editSuggest()">{{ c.label }}</option>
              }
            </select>
          </label>
        } @else {
          <label class="field">
            <span>标签</span>
            <select class="input select"
              [value]="editLabel()" (change)="pickLabel($any($event.target).value)">
              <option value="" [selected]="editLabel() === ''">无</option>
              @for (l of labelOptions; track l) {
                <option [value]="l" [selected]="l === editLabel()">{{ labelText(l) }}</option>
              }
            </select>
          </label>
        }
        <label class="field">
          <span>结论</span>
          <select class="input select"
            [value]="editVerdict()" (change)="editVerdict.set($any($event.target).value)">
            <option value="false_positive" [selected]="editVerdict() === 'false_positive'">
              {{ editKind() === 1 ? '分类判错' : '标签误判' }}
            </option>
            <!-- 「标签=无」表示该页不该有任何标签，与「判定正确」自相矛盾，直接不给选。 -->
            <option value="confirmed" [selected]="editVerdict() === 'confirmed'"
              [disabled]="editKind() === 0 && editLabel() === ''">
              {{ editKind() === 1 ? '分类正确' : '标签正确' }}
            </option>
          </select>
        </label>
        @if (editErr()) { <p class="small" style="color: var(--danger)">{{ editErr() }}</p> }
        <div footer class="row" style="gap: 8px; justify-content: flex-end">
          <button type="button" class="btn" (click)="editing.set(null)">取消</button>
          <button type="button" class="btn btn-solid" [disabled]="editBusy() || !editValid()"
            (click)="saveEdit()">
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

  // ---- 修改反馈 ----
  protected readonly labelOptions = ['ai_suspect', 'ad_heavy', 'personal', 'ugc_platform'];
  protected readonly categories = CATEGORIES;
  protected readonly editing = signal<LabelFeedback | null>(null);
  /** 这条反馈指向什么：0 标签 / 1 分类。由数据决定，弹窗里不提供切换。 */
  protected readonly editKind = computed(() => (this.editing()?.kind === 1 ? 1 : 0));
  /** 空串 = 「无」：该页面不该有任何标签，也没有别的标签可换。 */
  protected readonly editLabel = signal('ai_suspect');
  protected readonly editCategory = signal('');
  protected readonly editSuggest = signal('');
  protected readonly editVerdict = signal('false_positive');
  protected readonly editBusy = signal(false);
  protected readonly editErr = signal('');

  /** 分类反馈必须说清是哪个分类——空分类的反馈只剩一个 URL，看了也没用。 */
  protected readonly editValid = computed(
    () => this.editKind() === 0 || this.editCategory() !== '',
  );

  /** 选「无」时把结论一并钉到误判：两个选项自相矛盾，不该让用户先看到矛盾再被纠正。 */
  protected pickLabel(v: string): void {
    this.editLabel.set(v);
    if (v === '') this.editVerdict.set('false_positive');
  }

  protected askEdit(it: LabelFeedback): void {
    this.editing.set(it);
    this.editLabel.set(it.no_label ? '' : it.label || 'ai_suspect');
    this.editCategory.set(it.category ?? '');
    this.editSuggest.set(it.suggest ?? '');
    this.editVerdict.set(it.verdict);
    this.editErr.set('');
  }

  protected async saveEdit(): Promise<void> {
    const it = this.editing();
    if (!it || !this.editValid()) return;
    // 标签选「无」时表单值是空的，但落库要保留用户报的那个标签：
    // 它是误判率统计的归属，抹掉就等于这个数据点从该标签的计数里消失。
    const noLabel = this.editKind() === 0 && this.editLabel() === '';
    const label = noLabel ? it.label : this.editLabel();
    const verdict = noLabel ? ('false_positive' as const) : (this.editVerdict() as 'false_positive' | 'confirmed');
    this.editBusy.set(true);
    this.editErr.set('');
    try {
      await this.api.updateLabelFeedback(it.id, {
        kind: this.editKind(),
        label,
        no_label: noLabel,
        category: this.editCategory(),
        suggest: this.editSuggest(),
        verdict,
      });
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
