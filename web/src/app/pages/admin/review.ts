import { Component, computed, effect, inject, signal } from '@angular/core';
import { AdminApi } from '../../core/admin-api.service';
import { errMsg } from '../../core/http';
import { categoryText, type QueuePrecheck, type ReviewItem } from '../../core/api';
import { fullTime, shortTime } from '../../shared/format';
import { Modal } from '../../shared/ui/modal';
import { Pager } from '../../shared/ui/pager';
import { AdminToolbar } from './admin-toolbar';

const SIZE = 10;

const STATUS_OPTIONS = [
  { value: 'pending', label: '待审核' },
  { value: 'approved', label: '已通过' },
  { value: 'rejected', label: '已拒绝' },
  { value: '', label: '全部' },
];

const STATUS_TEXT: Record<string, string> = {
  pending: '待审核',
  approved: '已通过',
  rejected: '已拒绝',
};

// 队列里的 precheck 是历史 JSON：旧条目可能只有 blacklist_history，解析失败按「未预检」处理。
function parsePrecheck(raw: string): QueuePrecheck | null {
  if (!raw) return null;
  try {
    const v: unknown = JSON.parse(raw);
    return typeof v === 'object' && v !== null ? (v as QueuePrecheck) : null;
  } catch {
    return null;
  }
}

// 行内只展示一条最该被看到的结论，完整字段在详情弹窗
function badgeOf(p: QueuePrecheck): { label: string; tone: string } {
  if (p.blacklist_history) return { label: '黑名单历史', tone: 'chip-danger' };
  if (p.error) return { label: '预检失败', tone: 'chip-warning' };
  if (p.reachable === false) {
    return { label: p.status_code ? `不可达 ${p.status_code}` : '不可达', tone: 'chip-warning' };
  }
  if (p.robots_allowed === false) return { label: 'robots 禁止', tone: 'chip-warning' };
  if (p.reachable) return { label: `可达 ${p.status_code ?? ''}`.trim(), tone: 'chip-accent' };
  return { label: '已预检', tone: '' };
}

function robotsText(p: QueuePrecheck): string {
  if (p.robots_found === false) return '无 robots.txt（视为允许）';
  if (p.robots_allowed === false) return '禁止抓取';
  if (p.robots_allowed) return '允许';
  return '—';
}

/**
 * 审核队列。提交时自动预检并随条目保存，可随时重跑；通过进白名单，拒绝进黑名单。
 * 行内「预检」徽标可点开完整详情。
 */
@Component({
  selector: 'vx-review-page',
  imports: [Modal, Pager, AdminToolbar],
  template: `
    @if (msg()) {
      <div class="notice notice-error" style="margin-bottom: 12px">{{ msg() }}</div>
    }

    <vx-admin-toolbar
      title="审核队列"
      [count]="selected().length"
      [actions]="batchActions"
      (kwSearch)="search($event)"
    >
      <select
        class="input select"
        style="width: 110px; height: 32px; font-size: var(--fs-xs)"
        aria-label="按状态筛选"
        [value]="status()"
        (change)="status.set(selEl.value); page.set(1)"
        #selEl
      >
        @for (s of statusOptions; track s.value) {
          <option [value]="s.value" [selected]="s.value === status()">{{ s.label }}</option>
        }
      </select>
    </vx-admin-toolbar>

    @if (loading()) {
      <div class="loading"><span class="spinner spinner-lg"></span></div>
    } @else {
      <div class="table-wrap">
        <table class="table">
          <thead>
            <tr>
              <th class="table-check">
                <input
                  type="checkbox"
                  aria-label="全选"
                  [checked]="allChecked()"
                  (change)="toggleAll(!allChecked())"
                />
              </th>
              <th style="width: 16%">域名</th>
              <th style="width: 18%">提交地址</th>
              <th style="width: 20%">理由</th>
              <th style="width: 10%">预检</th>
              <th style="width: 8%">状态</th>
              <th style="width: 10%">提交时间</th>
              <th class="is-right" style="width: 18%">操作</th>
            </tr>
          </thead>
          <tbody>
            @for (it of rows(); track it.id) {
              <tr [class.is-selected]="selected().includes(it.id)">
                <td class="table-check" data-nolabel>
                  <input
                    type="checkbox"
                    [attr.aria-label]="'选择 ' + it.domain"
                    [checked]="selected().includes(it.id)"
                    (change)="toggleOne(it.id)"
                  />
                </td>
                <td data-label="域名">
                  <div class="cell-strong cell-ellipsis">{{ it.domain }}</div>
                  <span class="faint small">{{ categoryText(it.category) }}</span>
                </td>
                <td data-label="提交地址">
                  <span class="faint small cell-ellipsis" [title]="it.url">{{ it.url }}</span>
                </td>
                <td data-label="理由"><span class="cell-clamp">{{ it.reason || '—' }}</span></td>
                <td data-label="预检">
                  @if (precheckOf(it); as badge) {
                    <button type="button" class="chip {{ badge.tone }}" (click)="detailId.set(it.id)">
                      {{ badge.label }}
                    </button>
                  } @else {
                    <span class="faint small">未预检</span>
                  }
                </td>
                <td data-label="状态">
                  <span class="chip" [class]="statusChip(it.status)">{{ STATUS_TEXT[it.status] ?? it.status }}</span>
                </td>
                <td data-label="提交时间"><span class="small muted">{{ shortTime(it.created_at) }}</span></td>
                <td class="is-actions" data-nolabel>
                  <span class="row" style="gap: 2px; justify-content: flex-end">
                    <button type="button" class="btn btn-link btn-sm" [disabled]="it.status !== 'pending'" (click)="approve(it)">
                      通过
                    </button>
                    <button
                      type="button"
                      class="btn btn-link btn-sm"
                      style="color: var(--danger)"
                      [disabled]="it.status !== 'pending'"
                      (click)="rejectId.set(it.id)"
                    >
                      拒绝
                    </button>
                    <button type="button" class="btn btn-link btn-sm" [disabled]="checking() === it.id" (click)="rerun(it)">
                      {{ checking() === it.id ? '预检中' : '预检' }}
                    </button>
                  </span>
                </td>
              </tr>
            } @empty {
              <tr>
                <td colspan="8"><div class="empty">暂无数据</div></td>
              </tr>
            }
          </tbody>
        </table>
      </div>
    }

    <vx-pager [page]="page()" [pages]="pages()" [total]="total()" (pageChange)="goPage($event)" />

    @if (detail(); as d) {
      <vx-modal title="预检详情" [width]="520" (closed)="detailId.set(null)">
        @if (precheckOf(d); as badge) {
          <div style="margin-bottom: 8px"><span class="chip {{ badge.tone }}">{{ badge.label }}</span></div>
        }
        <div class="kv"><span class="kv-key">域名</span><span class="kv-val">{{ d.domain }}</span></div>
        <div class="kv"><span class="kv-key">提交地址</span><span class="kv-val">{{ d.url }}</span></div>
        <div class="kv"><span class="kv-key">最终地址</span><span class="kv-val">{{ parsedDetail()?.final_url || '—' }}</span></div>
        <div class="kv">
          <span class="kv-key">可达性</span>
          <span class="kv-val">{{ parsedDetail() ? (parsedDetail()!.reachable ? '可达' : '不可达') : '未预检' }}</span>
        </div>
        <div class="kv"><span class="kv-key">状态码</span><span class="kv-val">{{ parsedDetail()?.status_code || '—' }}</span></div>
        <div class="kv"><span class="kv-key">内容类型</span><span class="kv-val">{{ parsedDetail()?.content_type || '—' }}</span></div>
        <div class="kv">
          <span class="kv-key">HTML 页面</span>
          <span class="kv-val">{{ parsedDetail() ? (parsedDetail()!.is_html ? '是' : '否') : '—' }}</span>
        </div>
        <div class="kv">
          <span class="kv-key">robots</span>
          <span class="kv-val">{{ parsedDetail() ? robotsText(parsedDetail()!) : '—' }}</span>
        </div>
        <div class="kv">
          <span class="kv-key">黑名单历史</span>
          <span class="kv-val">{{ parsedDetail()?.blacklist_history ? '命中，该域名曾被拒绝' : '无' }}</span>
        </div>
        <div class="kv">
          <span class="kv-key">检查时间</span>
          <span class="kv-val">{{ fullTime(parsedDetail()?.checked_at ?? d.precheck_at ?? 0) }}</span>
        </div>
        @if (parsedDetail()?.error) {
          <div class="kv">
            <span class="kv-key">错误</span>
            <span class="kv-val" style="color: var(--danger)">{{ parsedDetail()!.error }}</span>
          </div>
        }
        <button type="button" footer class="btn" [disabled]="checking() === d.id" (click)="rerun(d)">
          {{ checking() === d.id ? '预检中' : '重新预检' }}
        </button>
        <button type="button" footer class="btn btn-solid" (click)="detailId.set(null)">关闭</button>
      </vx-modal>
    }

    @if (rejectId() !== null && rejectItem(); as r) {
      <vx-modal
        title="拒绝提交"
        [width]="420"
        (closed)="rejectId.set(null); reason.set('')"
      >
        <label class="field">
          <span class="field-label">拒绝原因</span>
          <textarea
            class="input textarea"
            rows="2"
            [value]="reason()"
            (input)="reason.set(reasonEl.value)"
            #reasonEl
          ></textarea>
        </label>
        <p class="faint small" style="margin-top: 8px">拒绝后域名会进入黑名单，爬虫将永久跳过。</p>
        <button type="button" footer class="btn" (click)="rejectId.set(null); reason.set('')">取消</button>
        <button type="button" footer class="btn btn-danger" (click)="confirmReject()">确认拒绝</button>
      </vx-modal>
    }
  `,
})
export class ReviewPage {
  private readonly api = inject(AdminApi);

  protected readonly statusOptions = STATUS_OPTIONS;
  protected readonly STATUS_TEXT = STATUS_TEXT;
  protected readonly categoryText = categoryText;
  protected readonly shortTime = shortTime;
  protected readonly fullTime = fullTime;
  protected readonly robotsText = robotsText;

  protected readonly rows = signal<ReviewItem[]>([]);
  protected readonly total = signal(0);
  protected readonly loading = signal(false);
  protected readonly msg = signal('');
  protected readonly kw = signal('');
  protected readonly status = signal('pending');
  protected readonly page = signal(1);
  protected readonly selected = signal<number[]>([]);
  protected readonly checking = signal<number | null>(null);
  protected readonly rejectId = signal<number | null>(null);
  protected readonly reason = signal('');
  protected readonly detailId = signal<number | null>(null);

  protected readonly pages = computed(() => Math.max(1, Math.ceil(this.total() / SIZE)));
  protected readonly allChecked = computed(
    () => this.rows().length > 0 && this.rows().every((r) => this.selected().includes(r.id)),
  );

  protected readonly detail = computed(
    () => this.rows().find((r) => r.id === this.detailId()) ?? null,
  );
  protected readonly rejectItem = computed(
    () => this.rows().find((r) => r.id === this.rejectId()) ?? null,
  );
  protected readonly parsedDetail = computed(() =>
    this.detail() ? parsePrecheck(this.detail()!.precheck) : null,
  );

  protected readonly batchActions = [
    { label: '批量通过', run: () => this.batchApprove() },
  ];

  constructor() {
    effect(() => {
      const kw = this.kw();
      const status = this.status();
      const page = this.page();
      void this.load(kw, status, page);
    });
  }

  private async load(kw: string, status: string, page: number): Promise<void> {
    this.loading.set(true);
    try {
      const r = await this.api.listReview(status, kw, page);
      this.rows.set(r.items ?? []);
      this.total.set(r.total);
    } catch (e) {
      this.msg.set(errMsg(e));
    } finally {
      this.loading.set(false);
    }
  }

  protected precheckOf(it: ReviewItem): { label: string; tone: string } | null {
    const pre = parsePrecheck(it.precheck);
    return pre ? badgeOf(pre) : null;
  }

  protected statusChip(status: string): string {
    return status === 'approved' ? 'chip-success' : status === 'rejected' ? 'chip-danger' : '';
  }

  protected search(kw: string): void {
    this.kw.set(kw);
    this.page.set(1);
  }

  protected goPage(p: number): void {
    this.selected.set([]);
    this.page.set(p);
  }

  protected toggleOne(id: number): void {
    this.selected.update((list) =>
      list.includes(id) ? list.filter((x) => x !== id) : [...list, id],
    );
  }

  protected toggleAll(all: boolean): void {
    this.selected.set(all ? this.rows().map((r) => r.id) : []);
  }

  protected async approve(it: ReviewItem): Promise<void> {
    try {
      await this.api.approveReview(it.id);
      await this.load(this.kw(), this.status(), this.page());
    } catch (e) {
      this.msg.set(errMsg(e));
    }
  }

  private async batchApprove(): Promise<void> {
    for (const id of this.selected()) {
      try {
        await this.api.approveReview(id);
      } catch {
        /* 已审核的条目跳过 */
      }
    }
    await this.load(this.kw(), this.status(), this.page());
  }

  // 重新预检：结果写回队列，同时更新列表行（详情弹窗直接从列表取最新行）
  protected async rerun(it: ReviewItem): Promise<void> {
    this.checking.set(it.id);
    try {
      const r = await this.api.precheckReview(it.id);
      const pre = r.precheck as QueuePrecheck;
      const at = pre.checked_at ?? Math.floor(Date.now() / 1000);
      this.rows.update((list) =>
        list.map((x) => (x.id === it.id ? { ...x, precheck: JSON.stringify(pre), precheck_at: at } : x)),
      );
    } catch (e) {
      this.msg.set(errMsg(e));
    } finally {
      this.checking.set(null);
    }
  }

  protected async confirmReject(): Promise<void> {
    const id = this.rejectId();
    if (id === null) return;
    try {
      await this.api.rejectReview(id, this.reason());
      this.rejectId.set(null);
      this.reason.set('');
      await this.load(this.kw(), this.status(), this.page());
    } catch (e) {
      this.msg.set(errMsg(e));
    }
  }
}
