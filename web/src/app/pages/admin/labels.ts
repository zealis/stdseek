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

/** 用户反馈：标签与分类的误判数据用于调整规则；「修改」改的是链接本身，反馈记录保持原样。 */
@Component({
  selector: 'vx-labels-page',
  imports: [Pager, AdminToolbar, Modal],
  template: `
    @if (msg()) {
      <div class="notice notice-error" style="margin-bottom: 12px">{{ msg() }}</div>
    }
    @if (ok()) {
      <div class="notice notice-success" style="margin-bottom: 12px">{{ ok() }}</div>
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
              <th style="width: 24%">页面</th>
              <th style="width: 14%">反馈内容</th>
              <th style="width: 14%">链接当前</th>
              <th style="width: 9%">结论</th>
              <th>用户说明</th>
              <th style="width: 11%">时间</th>
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
                <td data-label="反馈内容">
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
                <td data-label="链接当前">
                  @if (it.kind === 1) {
                    @if (it.site_category) {
                      <span class="chip">{{ categoryText(it.site_category) }}</span>
                    } @else {
                      <span class="faint small">未知</span>
                    }
                  } @else if (it.page_labels.length > 0) {
                    <div class="row wrap" style="gap: 4px">
                      @for (l of it.page_labels; track l) {
                        <span class="chip">{{ labelText(l) }}</span>
                      }
                    </div>
                  } @else {
                    <span class="faint small">无标签</span>
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
                <td colspan="7"><div class="empty">暂无数据</div></td>
              </tr>
            }
          </tbody>
        </table>
      </div>
    }

    <vx-pager [page]="page()" [pages]="pages()" [total]="total()" (pageChange)="goPage($event)" />

    @if (editing()) {
      <!-- 「修改」改的是被反馈的那个链接，不是这条反馈记录：
           标签反馈 → 改这一页的页级标签；分类反馈 → 改这个站点的分类。
           反馈是「用户说过什么」的历史，改它等于篡改证据。 -->
      <vx-modal title="修改链接" [width]="460" (closed)="editing.set(null)">
        <div class="stack" style="gap: 4px; margin-bottom: 14px">
          <span class="cell-strong">{{ editing()!.domain }}</span>
          <span class="faint small cell-ellipsis">{{ editing()!.url }}</span>
        </div>

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
        } @else {
          <div class="field">
            <span>这一页的标签</span>
            <div class="row wrap" style="gap: 14px">
              @for (l of labelOptions; track l) {
                <label class="fb-check">
                  <input type="checkbox" [checked]="editLabels().includes(l)"
                    (change)="toggleEditLabel(l, $any($event.target).checked)" />
                  <span class="small">{{ labelText(l) }}</span>
                </label>
              }
            </div>
          </div>
        }

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
  styles: `
    .fb-check {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      cursor: pointer;
    }

    .fb-check input {
      accent-color: var(--brand-solid);
      width: 16px;
      height: 16px;
    }
  `,
})
export class LabelsPage {
  private readonly api = inject(AdminApi);

  protected readonly verdicts = VERDICTS;

  // ---- 修改链接（不是改反馈记录本身） ----
  protected readonly labelOptions = ['ai_suspect', 'ad_heavy', 'personal', 'ugc_platform'];
  protected readonly categories = CATEGORIES;
  protected readonly editing = signal<LabelFeedback | null>(null);
  /** 这条反馈指向什么：0 标签 / 1 分类。决定改链接的哪一面。 */
  protected readonly editKind = computed(() => (this.editing()?.kind === 1 ? 1 : 0));
  /** 这一页当前的页级标签；一个都不勾 = 这一页没有标签。 */
  protected readonly editLabels = signal<string[]>([]);
  /** 这个站点当前的分类。 */
  protected readonly editCategory = signal('');
  protected readonly editBusy = signal(false);
  protected readonly editErr = signal('');

  /** 标签可以清空（这一页本来就不该有标签），分类不能——空分类等于没改。 */
  protected readonly editValid = computed(
    () => this.editKind() === 0 || this.editCategory() !== '',
  );

  protected askEdit(it: LabelFeedback): void {
    this.editing.set(it);
    this.ok.set('');
    this.editLabels.set([...(it.page_labels ?? [])]);
    // 用站点**当前**分类做初值：反馈里的 category 是反馈那一刻的快照，
    // 拿它回填会把中间 AI 重判的结果覆盖回去。
    this.editCategory.set(it.site_category || it.category || '');
    this.editErr.set('');
  }

  protected toggleEditLabel(label: string, on: boolean): void {
    this.editLabels.update((list) =>
      on ? (list.includes(label) ? list : [...list, label]) : list.filter((x) => x !== label),
    );
  }

  protected async saveEdit(): Promise<void> {
    const it = this.editing();
    if (!it || !this.editValid()) return;
    this.editBusy.set(true);
    this.editErr.set('');
    this.ok.set('');
    try {
      await this.api.fixLink(
        it.kind === 1
          ? { url: it.url, kind: 1, category: this.editCategory() }
          : { url: it.url, kind: 0, labels: this.editLabels() },
      );
      this.ok.set(
        it.kind === 1
          ? `已修改链接：站点分类 → ${categoryText(this.editCategory())}`
          : this.editLabels().length > 0
            ? `已修改链接：页级标签 → ${this.editLabels().map(labelText).join('、')}`
            : '已修改链接：页级标签 → 无',
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
  protected readonly ok = signal('');
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
    this.ok.set('');
  }

  protected goPage(p: number): void {
    this.page.set(p);
  }
}
