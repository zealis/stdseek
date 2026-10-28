import { Component, computed, effect, inject, signal } from '@angular/core';
import { AdminApi } from '../../core/admin-api.service';
import { errMsg } from '../../core/http';
import { ISSUE_KINDS, issueText, type CrawlIssue } from '../../core/api';
import { shortTime, sinceText } from '../../shared/format';
import { Pager } from '../../shared/ui/pager';
import { AdminToolbar } from './admin-toolbar';

const SIZE = 10;

/**
 * 抓取异常名单。
 *
 * 只回答一个问题：现在哪些白名单站点抓不出东西、为什么。
 * 三类原因对应三种完全不同的处理（等站方恢复 / 换抓取入口 / 调正文选择器），
 * 所以类型必须显式展示，不能只写「抓取失败」。
 * 恢复正常是自动的：抓取一旦有页入库，爬虫自己把这行删掉。
 */
@Component({
  selector: 'vx-issues-page',
  imports: [Pager, AdminToolbar],
  template: `
    @if (msg()) {
      <div
        class="notice"
        [class.notice-error]="!okMsg()"
        [class.notice-success]="okMsg()"
        style="margin-bottom: 12px"
      >
        <span>{{ msg() }}</span>
        @if (okMsg() && canRefresh()) {
          <button type="button" class="btn btn-link btn-sm" style="margin-left: 8px" (click)="reload()">
            刷新
          </button>
        }
      </div>
    }

    <vx-admin-toolbar
      title="异常名单"
      [count]="selected().length"
      [actions]="batchActions"
      placeholder="搜索域名或异常说明"
      (kwSearch)="search($event)"
    >
      <span class="row" style="gap: 6px">
        <span class="faint small">类型</span>
        <select
          class="input select"
          style="width: 168px; height: 32px; font-size: var(--fs-xs)"
          aria-label="按异常类型筛选"
          [value]="kind()"
          (change)="setKind(kindEl.value)"
          #kindEl
        >
          <option value="" [selected]="kind() === ''">全部（{{ counts()['all'] ?? 0 }}）</option>
          @for (k of kinds; track k.value) {
            <option [value]="k.value" [selected]="kind() === k.value">
              {{ k.label }}（{{ counts()[k.value] ?? 0 }}）
            </option>
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
              <th class="table-check">
                <input
                  type="checkbox"
                  aria-label="全选"
                  [checked]="allChecked()"
                  (change)="toggleAll(!allChecked())"
                />
              </th>
              <th style="width: 19%">域名</th>
              <th style="width: 17%">异常</th>
              <th style="width: 30%">说明</th>
              <th style="width: 12%">已持续</th>
              <th class="is-right" style="width: 15%">操作</th>
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
                <td data-label="域名"><span class="cell-strong">{{ it.domain }}</span></td>
                <td data-label="异常">
                  <div style="display: grid; gap: 2px; justify-items: start">
                    <span [class]="'chip ' + kindClass(it.kind)" [title]="kindHint(it.kind)">
                      {{ issueText(it.kind) }}
                    </span>
                    <span class="faint small" [title]="'连续失败 ' + it.occurrences + ' 次'">
                      失败 {{ it.occurrences }} 次
                    </span>
                  </div>
                </td>
                <td data-label="说明">
                  <span class="small cell-clamp" [title]="it.detail">{{ it.detail || '—' }}</span>
                </td>
                <td data-label="已持续">
                  <span
                    class="small muted"
                    [title]="'首次发现 ' + shortTime(it.first_seen_at) + '，最近一次 ' + shortTime(it.last_seen_at)"
                  >
                    {{ sinceText(it.first_seen_at) }}
                  </span>
                </td>
                <td class="is-actions" data-nolabel>
                  <button
                    type="button"
                    class="btn btn-link btn-sm"
                    title="立即重新抓取该站点"
                    [disabled]="busy()"
                    (click)="recrawl([it.domain])"
                  >
                    重抓
                  </button>
                  <button
                    type="button"
                    class="btn btn-link btn-sm"
                    title="从名单移除（下一轮抓取仍失败会自动回来）"
                    [disabled]="busy()"
                    (click)="clear([it.domain])"
                  >
                    移出
                  </button>
                </td>
              </tr>
            } @empty {
              <tr>
                <td colspan="6">
                  <div class="empty">
                    {{ kw() ? '没有匹配的站点' : '没有抓取异常的站点' }}
                  </div>
                </td>
              </tr>
            }
          </tbody>
        </table>
      </div>
    }

    <vx-pager [page]="page()" [pages]="pages()" [total]="total()" (pageChange)="goPage($event)" />
  `,
})
export class IssuesPage {
  private readonly api = inject(AdminApi);

  protected readonly kinds = ISSUE_KINDS;
  protected readonly issueText = issueText;
  protected readonly shortTime = shortTime;
  protected readonly sinceText = sinceText;

  protected readonly rows = signal<CrawlIssue[]>([]);
  protected readonly total = signal(0);
  protected readonly counts = signal<Record<string, number>>({});
  protected readonly loading = signal(false);
  protected readonly busy = signal(false);
  protected readonly msg = signal('');
  protected readonly okMsg = signal(false);
  protected readonly canRefresh = signal(false);
  protected readonly kw = signal('');
  protected readonly kind = signal('');
  protected readonly page = signal(1);
  protected readonly selected = signal<number[]>([]);

  protected readonly pages = computed(() => Math.max(1, Math.ceil(this.total() / SIZE)));
  protected readonly allChecked = computed(
    () => this.rows().length > 0 && this.rows().every((r) => this.selected().includes(r.id)),
  );

  protected readonly batchActions = [
    { label: '重新抓取', run: () => this.recrawl(this.selectedDomains()) },
    { label: '移出名单', run: () => this.clear(this.selectedDomains()) },
  ];

  constructor() {
    effect(() => {
      const kind = this.kind();
      const kw = this.kw();
      const page = this.page();
      void this.load(kind, kw, page);
    });
  }

  private async load(kind: string, kw: string, page: number): Promise<void> {
    this.loading.set(true);
    try {
      const r = await this.api.listCrawlIssues(kind, kw, page, SIZE);
      this.rows.set(r.items ?? []);
      this.total.set(r.total);
      this.counts.set(r.counts ?? {});
    } catch (e) {
      this.msg.set(errMsg(e));
      this.okMsg.set(false);
    } finally {
      this.loading.set(false);
    }
  }

  /** 手动刷新（重新抓取是异步的，抓没抓完只能靠用户主动看）。 */
  protected reload(): void {
    this.canRefresh.set(false);
    void this.load(this.kind(), this.kw(), this.page());
  }

  protected kindClass(kind: string): string {
    switch (kind) {
      case 'unreachable':
        return 'chip-danger';
      case 'robots':
        return 'chip-warning';
      default:
        return 'chip-accent';
    }
  }

  protected kindHint(kind: string): string {
    return ISSUE_KINDS.find((k) => k.value === kind)?.hint ?? '';
  }

  protected search(kw: string): void {
    this.kw.set(kw);
    this.page.set(1);
  }

  protected setKind(kind: string): void {
    this.kind.set(kind);
    this.selected.set([]);
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

  private selectedDomains(): string[] {
    const ids = this.selected();
    const picked = this.rows().filter((r) => ids.includes(r.id)).map((r) => r.domain);
    // 跨页保留勾选会带来「选了看不到的行」的歧义，这里只认当前页
    return picked;
  }

  protected async recrawl(domains: string[]): Promise<void> {
    if (!domains.length || this.busy()) return;
    this.busy.set(true);
    this.msg.set('');
    try {
      const r = await this.api.recrawlIssues(domains);
      this.okMsg.set(true);
      this.canRefresh.set(true);
      this.msg.set(`已开始重新抓取 ${r.count} 个站点（后台执行，单站约 30 秒）`);
    } catch (e) {
      this.okMsg.set(false);
      this.msg.set(errMsg(e));
    } finally {
      this.busy.set(false);
    }
  }

  protected async clear(domains: string[]): Promise<void> {
    if (!domains.length || this.busy()) return;
    this.busy.set(true);
    this.msg.set('');
    try {
      const r = await this.api.clearCrawlIssues(domains);
      this.selected.set([]);
      this.okMsg.set(true);
      this.canRefresh.set(false);
      this.msg.set(`已把 ${r.cleared} 个站点移出名单（下一轮抓取仍失败会自动回来）`);
      await this.load(this.kind(), this.kw(), this.page());
    } catch (e) {
      this.okMsg.set(false);
      this.msg.set(errMsg(e));
    } finally {
      this.busy.set(false);
    }
  }
}
