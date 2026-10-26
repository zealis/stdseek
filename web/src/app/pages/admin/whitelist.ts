import { Component, computed, effect, inject, signal } from '@angular/core';
import { AdminApi } from '../../core/admin-api.service';
import { errMsg } from '../../core/http';
import { CATEGORIES, categoryText, type WhitelistSite } from '../../core/api';
import { fullTime, hours } from '../../shared/format';
import { Icon } from '../../shared/ui/icon';
import { Modal } from '../../shared/ui/modal';
import { Pager } from '../../shared/ui/pager';
import { AdminToolbar } from './admin-toolbar';

const SIZE = 10;

/** 白名单管理：站点是爬虫种子。启用/禁用即时生效，批量操作走循环调用。 */
@Component({
  selector: 'vx-whitelist-page',
  imports: [Icon, Modal, Pager, AdminToolbar],
  template: `
    @if (msg()) {
      <div class="notice notice-error" style="margin-bottom: 12px">{{ msg() }}</div>
    }

    <vx-admin-toolbar
      title="白名单"
      [count]="selected().length"
      [actions]="batchActions"
      (kwSearch)="search($event)"
    >
      <button type="button" class="btn btn-solid" (click)="open.set(true)">
        <vx-icon name="plus" [size]="14" />
        添加站点
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
              <th style="width: 26%">域名</th>
              <th style="width: 12%">分类</th>
              <th style="width: 10%">状态</th>
              <th style="width: 24%">最后抓取</th>
              <th style="width: 12%" class="is-right">抓取间隔</th>
              <th class="is-right" style="width: 12%">操作</th>
            </tr>
          </thead>
          <tbody>
            @for (w of rows(); track w.id) {
              <tr [class.is-selected]="selected().includes(w.id)">
                <td class="table-check" data-nolabel>
                  <input
                    type="checkbox"
                    [attr.aria-label]="'选择 ' + w.domain"
                    [checked]="selected().includes(w.id)"
                    (change)="toggleOne(w.id)"
                  />
                </td>
                <td data-label="域名"><span class="cell-strong">{{ w.domain }}</span></td>
                <td data-label="分类">{{ categoryText(w.category) }}</td>
                <td data-label="状态">
                  <span class="chip" [class.chip-accent]="w.enabled">{{ w.enabled ? '启用' : '禁用' }}</span>
                </td>
                <td data-label="最后抓取">
                  <span class="small muted">
                    {{ w.last_crawled_at ? fullTime(w.last_crawled_at) : '—' }}{{ w.last_status ? ' ' + w.last_status : '' }}
                  </span>
                </td>
                <td data-label="抓取间隔" class="is-right"><span class="small mono">{{ hours(w.crawl_interval) }}</span></td>
                <td class="is-actions" data-nolabel>
                  <button type="button" class="btn btn-link btn-sm" (click)="toggleSite(w)">
                    {{ w.enabled ? '禁用' : '启用' }}
                  </button>
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

    @if (open()) {
      <vx-modal title="添加白名单站点" [width]="420" (closed)="open.set(false)">
        <div class="form-grid">
          <label class="field">
            <span class="field-label">域名</span>
            <input
              class="input"
              type="text"
              autocapitalize="off"
              placeholder="go.dev"
              [value]="form().domain"
              (input)="patchForm('domain', domainEl.value)"
              #domainEl
            />
          </label>
          <label class="field">
            <span class="field-label">分类</span>
            <select
              class="input select"
              [value]="form().category"
              (change)="patchForm('category', catEl.value)"
              #catEl
            >
              @for (c of categories; track c.value) {
                <option [value]="c.value" [selected]="c.value === form().category">{{ c.label }}</option>
              }
            </select>
          </label>
          <label class="field">
            <span class="field-label">抓取间隔（秒）</span>
            <input
              class="input"
              type="number"
              inputmode="numeric"
              [value]="form().crawl_interval"
              (input)="patchForm('interval', intervalEl.value)"
              #intervalEl
            />
          </label>
        </div>
        <button type="button" footer class="btn" (click)="open.set(false)">取消</button>
        <button type="button" footer class="btn btn-solid" [disabled]="!form().domain" (click)="add()">
          添加
        </button>
      </vx-modal>
    }
  `,
})
export class WhitelistPage {
  private readonly api = inject(AdminApi);

  protected readonly categories = CATEGORIES;
  protected readonly categoryText = categoryText;
  protected readonly fullTime = fullTime;
  protected readonly hours = hours;

  protected readonly rows = signal<WhitelistSite[]>([]);
  protected readonly total = signal(0);
  protected readonly loading = signal(false);
  protected readonly msg = signal('');
  protected readonly kw = signal('');
  protected readonly page = signal(1);
  protected readonly selected = signal<number[]>([]);
  protected readonly open = signal(false);
  protected readonly form = signal({ domain: '', category: 'official', crawl_interval: 86400 });

  protected readonly pages = computed(() => Math.max(1, Math.ceil(this.total() / SIZE)));
  protected readonly allChecked = computed(
    () => this.rows().length > 0 && this.rows().every((r) => this.selected().includes(r.id)),
  );

  protected readonly batchActions = [
    { label: '批量启用', run: () => this.batchToggle(true) },
    { label: '批量禁用', danger: true, run: () => this.batchToggle(false) },
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
      const r = await this.api.listWhitelist(kw, page);
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

  protected async toggleSite(w: WhitelistSite): Promise<void> {
    try {
      await this.api.toggleWhitelist(w.id, !w.enabled);
      await this.load(this.kw(), this.page());
    } catch (e) {
      this.msg.set(errMsg(e));
    }
  }

  private async batchToggle(enabled: boolean): Promise<void> {
    for (const id of this.selected()) await this.api.toggleWhitelist(id, enabled);
    await this.load(this.kw(), this.page());
  }

  protected patchForm(key: 'domain' | 'category' | 'interval', value: string): void {
    this.form.update((f) =>
      key === 'domain'
        ? { ...f, domain: value.trim().toLowerCase() }
        : key === 'category'
          ? { ...f, category: value }
          : { ...f, crawl_interval: Number(value) || 86400 },
    );
  }

  protected async add(): Promise<void> {
    try {
      await this.api.addWhitelist({
        domain: this.form().domain,
        category: this.form().category,
        crawl_interval: this.form().crawl_interval,
      });
      this.open.set(false);
      this.form.set({ domain: '', category: 'official', crawl_interval: 86400 });
      await this.load(this.kw(), this.page());
    } catch (e) {
      this.msg.set(errMsg(e));
    }
  }
}
