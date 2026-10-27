import { Component, computed, effect, inject, signal } from '@angular/core';
import { AdminApi } from '../../core/admin-api.service';
import { errMsg } from '../../core/http';
import { type BlacklistSite } from '../../core/api';
import { Icon } from '../../shared/ui/icon';
import { Modal } from '../../shared/ui/modal';
import { Pager } from '../../shared/ui/pager';
import { AdminToolbar } from './admin-toolbar';
import { Confirm } from '../../shared/ui/confirm';

const SIZE = 10;

/** 黑名单：命中域名的提交直接拒绝，爬虫永久跳过。移除后域名可重新提交。 */
@Component({
  selector: 'vx-blacklist-page',
  imports: [Icon, Modal, Pager, AdminToolbar, Confirm],
  template: `
    @if (msg()) {
      <div class="notice notice-error" style="margin-bottom: 12px">{{ msg() }}</div>
    }

    <vx-admin-toolbar
      title="黑名单"
      [count]="selected().length"
      [actions]="batchActions"
      (kwSearch)="search($event)"
    >
      <button type="button" class="btn btn-danger" (click)="open.set(true)">
        <vx-icon name="plus" [size]="14" />
        加入黑名单
      </button>
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
              <th style="width: 30%">域名</th>
              <th>理由</th>
              <th style="width: 12%" class="is-right">提交被拒</th>
              <th style="width: 12%" class="is-right">爬取拦截</th>
              <th class="is-right" style="width: 10%">操作</th>
            </tr>
          </thead>
          <tbody>
            @for (b of rows(); track b.id) {
              <tr [class.is-selected]="selected().includes(b.id)">
                <td class="table-check" data-nolabel>
                  <input
                    type="checkbox"
                    [attr.aria-label]="'选择 ' + b.domain"
                    [checked]="selected().includes(b.id)"
                    (change)="toggleOne(b.id)"
                  />
                </td>
                <td data-label="域名"><span class="cell-strong">{{ b.domain }}</span></td>
                <td data-label="理由">{{ b.reason || '—' }}</td>
                <td data-label="提交被拒" class="is-right"><span class="small mono">{{ b.reject_count }}</span></td>
                <td data-label="爬取拦截" class="is-right"><span class="small mono">{{ b.blocked_count }}</span></td>
                <td class="is-actions" data-nolabel>
                  <button type="button" class="btn btn-link btn-sm" style="color: var(--danger)" (click)="askRemove(b)">
                    移除
                  </button>
                </td>
              </tr>
            } @empty {
              <tr>
                <td colspan="6"><div class="empty">暂无数据</div></td>
              </tr>
            }
          </tbody>
        </table>
      </div>
    }

    <vx-pager [page]="page()" [pages]="pages()" [total]="total()" (pageChange)="goPage($event)" />

    @if (open()) {
      <vx-modal title="加入黑名单" [width]="420" (closed)="open.set(false)">
        <div class="form-grid">
          <label class="field">
            <span class="field-label">域名</span>
            <input
              class="input"
              type="text"
              autocapitalize="off"
              placeholder="spam.example"
              [value]="form().domain"
              (input)="patchDomain(domainEl.value)"
              #domainEl
            />
          </label>
          <label class="field">
            <span class="field-label">原因</span>
            <textarea
              class="input textarea"
              rows="2"
              [value]="form().reason"
              (input)="form.update((f) => ({ ...f, reason: reasonEl.value }))"
              #reasonEl
            ></textarea>
          </label>
        </div>
        <button type="button" footer class="btn" (click)="open.set(false)">取消</button>
        <button type="button" footer class="btn btn-danger" [disabled]="!form().domain" (click)="add()">
          确认
        </button>
      </vx-modal>
    }

    @if (confirmTarget()) {
      <vx-confirm
        title="移出黑名单"
        desc="移出后，该域名的内容将在下次抓取时重新进入索引。"
        confirmText="移出"
        [danger]="true"
        (confirmed)="remove()"
        (cancelled)="confirmTarget.set(null)"
      />
    }
  `,
})
export class BlacklistPage {
  private readonly api = inject(AdminApi);

  protected readonly rows = signal<BlacklistSite[]>([]);
  protected readonly total = signal(0);
  protected readonly loading = signal(false);
  protected readonly msg = signal('');
  protected readonly confirmTarget = signal<BlacklistSite | null>(null);
  protected readonly kw = signal('');
  protected readonly page = signal(1);
  protected readonly selected = signal<number[]>([]);
  protected readonly open = signal(false);
  protected readonly form = signal({ domain: '', reason: '' });

  protected readonly pages = computed(() => Math.max(1, Math.ceil(this.total() / SIZE)));
  protected readonly allChecked = computed(
    () => this.rows().length > 0 && this.rows().every((r) => this.selected().includes(r.id)),
  );

  protected readonly batchActions = [
    { label: '批量移除', danger: true, run: () => this.batchRemove() },
  ];

  constructor() {
    effect(() => {
      const kw = this.kw();
      const page = this.page();
      void this.load(kw, page);
    });
  }

  private async load(kw: string, page: number): Promise<void> {
    this.loading.set(true);
    try {
      const r = await this.api.listBlacklist(kw, page);
      this.rows.set(r.items ?? []);
      this.total.set(r.total);
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

  /** 删除前先确认：黑名单删掉后，被它挡住的内容下次抓取就会进来。 */
  protected askRemove(b: BlacklistSite): void {
    this.confirmTarget.set(b);
  }

  protected async remove(): Promise<void> {
    const b = this.confirmTarget();
    this.confirmTarget.set(null);
    if (!b) return;
    try {
      await this.api.removeBlacklist(b.id);
      await this.load(this.kw(), this.page());
    } catch (e) {
      this.msg.set(errMsg(e));
    }
  }

  private async batchRemove(): Promise<void> {
    for (const id of this.selected()) await this.api.removeBlacklist(id);
    await this.load(this.kw(), this.page());
  }

  protected patchDomain(v: string): void {
    this.form.update((f) => ({ ...f, domain: v.trim().toLowerCase() }));
  }

  protected async add(): Promise<void> {
    try {
      await this.api.addBlacklist({ domain: this.form().domain, reason: this.form().reason });
      this.open.set(false);
      this.form.set({ domain: '', reason: '' });
      await this.load(this.kw(), this.page());
    } catch (e) {
      this.msg.set(errMsg(e));
    }
  }
}
