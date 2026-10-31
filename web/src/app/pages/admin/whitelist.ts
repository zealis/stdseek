import { Component, computed, effect, inject, signal } from '@angular/core';
import { AdminApi } from '../../core/admin-api.service';
import { errMsg } from '../../core/http';
import {
  CATEGORIES,
  CRAWL_INTERVALS,
  categoryInterval,
  categoryText,
  crawlStatusText,
  labelBadge,
  labelText,
  type WhitelistSite,
} from '../../core/api';
import type { IconName } from '../../shared/ui/icon';
import { durationText, fullTime } from '../../shared/format';
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
      <div class="notice" [class.notice-error]="!okMsg()" [class.notice-success]="okMsg()"
        style="margin-bottom: 12px">{{ msg() }}</div>
    }
    @if (aiBusy()) {
      <div class="notice" style="margin-bottom: 12px">
        <span class="spinner"></span>
        <span class="small" style="margin-left: 8px">正在让 AI 判定分类，一次最多 20 个站…</span>
      </div>
    }

    <vx-admin-toolbar
      title="白名单"
      [count]="selected().length"
      [actions]="batchActions"
      (kwSearch)="search($event)"
    >
      <!-- 分类筛选：站点数量一多，翻页找「开发工具」比逐页翻快得多。
           选项带计数，空分类（提交时填错）也能一眼看见。 -->
      <select
        class="input select"
        style="width: auto; min-width: 132px"
        aria-label="按分类筛选"
        [value]="cat()"
        (change)="pickCat(catEl.value)"
        #catEl
      >
        <option value="">全部分类</option>
        @for (c of categories; track c.value) {
          <option [value]="c.value" [selected]="c.value === cat()">
            {{ c.label }}{{ countOf(c.value) ? '（' + countOf(c.value) + '）' : '' }}
          </option>
        }
      </select>
      <span class="small muted">共 {{ total() }} 个</span>
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
              <th style="width: 24%">域名</th>
              <th style="width: 16%">分类</th>
              <th style="width: 8%" class="is-right">链接</th>
              <th style="width: 9%">状态</th>
              <th style="width: 21%">最后抓取</th>
              <th style="width: 10%" class="is-right">抓取间隔</th>
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
                <td data-label="分类">
                  <div class="row" style="gap: 6px; flex-wrap: wrap">
                    <span>{{ categoryText(w.category) }}</span>
                    @if (w.category_source === 'ai') {
                      <span class="chip chip-accent" title="AI 判定：每跨过 30 / 300 / +3000 篇会重判一次">
                        AI
                      </span>
                    } @else if (w.category_source === 'manual') {
                      <span class="chip" title="管理员手动设定：自动判定不会覆盖">手动</span>
                    }
                    @for (l of w.labels ?? []; track l) {
                      <span class="chip chip-ico" [title]="labelText(l)" [attr.aria-label]="labelText(l)" role="img">
                        @if (labelBadge(l); as b) {
                          <span class="chip-ad">{{ b }}</span>
                        } @else {
                          <vx-icon [name]="labelIcon(l)" [size]="13" />
                        }
                      </span>
                    }
                  </div>
                </td>
                <td data-label="链接" class="is-right">
                  <span class="small mono" [title]="'已入库 ' + (w.doc_count ?? 0) + ' 篇（不含被折叠的副本）'">
                    {{ w.doc_count ?? 0 }}
                  </span>
                </td>
                <td data-label="状态">
                  <span class="chip" [class.chip-accent]="w.enabled">{{ w.enabled ? '启用' : '禁用' }}</span>
                </td>
                <td data-label="最后抓取">
                  <div class="row" style="gap: 6px; flex-wrap: wrap">
                    <span class="small muted">
                      {{ w.last_crawled_at ? fullTime(w.last_crawled_at) : '—' }}
                    </span>
                    @if (w.last_status) {
                      <span
                        [class]="'chip ' + statusClass(w.last_status)"
                        [title]="w.last_error || crawlStatusText(w.last_status)"
                      >
                        {{ crawlStatusText(w.last_status) }}
                      </span>
                    }
                  </div>
                </td>
                <td data-label="抓取间隔" class="is-right">
                  <span class="small mono">{{ durationText(w.crawl_interval) }}</span>
                </td>
                <td class="is-actions" data-nolabel>
                  <button type="button" class="btn btn-link btn-sm" (click)="editRule(w)">规则</button>
                  <button type="button" class="btn btn-link btn-sm" (click)="toggleSite(w)">
                    {{ w.enabled ? '禁用' : '启用' }}
                  </button>
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
            <span class="field-label">抓取间隔</span>
            <select
              class="input select"
              [value]="form().crawl_interval"
              (change)="patchForm('interval', intervalEl.value)"
              #intervalEl
            >
              <option [value]="0" [selected]="form().crawl_interval === 0">
                跟随分类（{{ durationText(categoryInterval(form().category)) }}）
              </option>
              @for (iv of intervals; track iv.value) {
                <option [value]="iv.value" [selected]="iv.value === form().crawl_interval">
                  {{ iv.label }}
                </option>
              }
            </select>
          </label>
        </div>
        <button type="button" footer class="btn" (click)="open.set(false)">取消</button>
        <button type="button" footer class="btn btn-solid" [disabled]="!form().domain" (click)="add()">
          添加
        </button>
      </vx-modal>
    }

    @if (rule(); as r) {
      <vx-modal title="抓取规则" [width]="460" (closed)="rule.set(null)">
        <div class="form-grid">
          <label class="field">
            <span class="field-label">抓取入口</span>
            <input
              class="input mono"
              type="text"
              placeholder="/"
              [value]="r.seed_path"
              (input)="patchRule('seed_path', $any($event.target).value)"
            />
          </label>
          <label class="field">
            <span class="field-label">文章 URL 规则</span>
            <input
              class="input mono"
              type="text"
              placeholder="^/post/\d+$"
              [value]="r.article_pattern"
              (input)="patchRule('article_pattern', $any($event.target).value)"
            />
          </label>
        </div>
        <button type="button" footer class="btn" (click)="rule.set(null)">取消</button>
        <button type="button" footer class="btn btn-solid" (click)="saveRule()">保存</button>
      </vx-modal>
    }
  `,
})
export class WhitelistPage {
  private readonly api = inject(AdminApi);

  protected readonly categories = CATEGORIES;
  protected readonly intervals = CRAWL_INTERVALS;
  protected readonly categoryText = categoryText;
  protected readonly categoryInterval = categoryInterval;
  protected readonly labelText = labelText;
  protected readonly crawlStatusText = crawlStatusText;

  /** 抓取状态 → 徽标配色（与异常名单页同一套语义）。 */
  protected statusClass(status: string): string {
    switch (status) {
      case 'ok':
        return 'chip-success';
      case 'error':
        return 'chip-danger';
      case 'robots':
        return 'chip-warning';
      case 'no-document':
        return 'chip-accent';
      default:
        return '';
    }
  }

  /** 标签 → 图标（与结果卡同一套映射）。 */
  protected labelIcon(label: string): IconName {
    const m: Record<string, IconName> = {
      ai_suspect: 'bot',
      personal: 'user',
      ugc_platform: 'users',
    };
    return m[label] ?? 'tag';
  }
  protected readonly labelBadge = labelBadge;
  protected readonly fullTime = fullTime;
  protected readonly durationText = durationText;

  protected readonly rows = signal<WhitelistSite[]>([]);
  protected readonly total = signal(0);
  protected readonly loading = signal(false);
  protected readonly msg = signal('');
  protected readonly okMsg = signal(false);
  protected readonly kw = signal('');
  /** 分类筛选（空串 = 全部）。和关键词一起进请求，也一起决定 effect 的依赖。 */
  protected readonly cat = signal('');
  /** 各分类站点数（工具栏下拉的括号数字） */
  protected readonly catCounts = signal<Record<string, number>>({});
  protected readonly page = signal(1);
  protected readonly selected = signal<number[]>([]);
  protected readonly open = signal(false);
  /** crawl_interval 为 0 表示「跟随分类默认」，后端按分类换算（见 sitecat.IntervalFor）。 */
  protected readonly form = signal({ domain: '', category: 'official', crawl_interval: 0 });

  protected readonly pages = computed(() => Math.max(1, Math.ceil(this.total() / SIZE)));
  protected readonly allChecked = computed(
    () => this.rows().length > 0 && this.rows().every((r) => this.selected().includes(r.id)),
  );

  protected readonly batchActions = [
    { label: '批量启用', run: () => this.batchToggle(true) },
    { label: '批量禁用', danger: true, run: () => this.batchToggle(false) },
    { label: 'AI 识别分类', run: () => this.classify() },
    { label: '按分类重算间隔', run: () => this.resetIntervals(0) },
    { label: '间隔改为 1 天', run: () => this.resetIntervals(86400) },
  ];
  /** AI 打标进行中：模型调用按站点串行排队，一次可能十几秒。 */
  protected readonly aiBusy = signal(false);

  constructor() {
    effect(() => {
      const kw = this.kw();
      const cat = this.cat();
      const page = this.page();
      void this.load(kw, cat, page);
    });
  }

  /** 某分类的站点数；0 或未知都返回 0（下拉里不显示括号）。 */
  protected countOf(cat: string): number {
    return this.catCounts()[cat] ?? 0;
  }

  protected pickCat(cat: string): void {
    this.selected.set([]);
    this.cat.set(cat);
    this.page.set(1);
  }

  private async load(kw: string, cat: string, page: number): Promise<void> {
    this.loading.set(true);
    try {
      const r = await this.api.listWhitelist(kw, page, SIZE, cat);
      this.rows.set(r.items ?? []);
      this.total.set(r.total);
      this.catCounts.set(r.cat_counts ?? {});
    } catch (e) {
      this.msg.set(errMsg(e));
    } finally {
      this.loading.set(false);
    }
  }

  /**
   * 批量 AI 分类：一次最多 20 个（后端限制），超出的按批继续。
   * 分类写回站点后要重新拉列表——否则界面上看不到变化，
   * 用户会以为没生效而重复点。
   */
  protected async classify(): Promise<void> {
    const ids = this.selected();
    if (!ids.length || this.aiBusy()) return;
    this.aiBusy.set(true);
    this.msg.set('');
    this.okMsg.set(false);
    let ok = 0;
    let tagged = 0;
    const failures: string[] = [];
    try {
      for (let i = 0; i < ids.length; i += 20) {
        const r = await this.api.classifySites(ids.slice(i, i + 20));
        ok += r.ok;
        for (const x of r.results ?? []) {
          if (x.error) failures.push(`${x.domain}：${x.error}`);
          else tagged += x.labels?.length ?? 0;
        }
      }
      this.selected.set([]);
      await this.load(this.kw(), this.cat(), this.page());
      this.okMsg.set(failures.length === 0);
      // 分类与标签一起报：只报「识别了 N 个站」看不出标签有没有打上。
      const head = tagged > 0 ? `已识别 ${ok} 个站点，打上 ${tagged} 个标签` : `已识别 ${ok} 个站点（无标签命中）`;
      this.msg.set(
        failures.length === 0
          ? head
          : `${head}，失败 ${failures.length} 个：${failures.slice(0, 3).join('；')}`,
      );
    } catch (e) {
      this.okMsg.set(false);
      this.msg.set(errMsg(e));
    } finally {
      this.aiBusy.set(false);
    }
  }

  /**
   * 批量改抓取间隔：interval=0 按分类重算，>0 统一设为该值。
   * 「按分类重算」会让平台站/新闻站抓得更勤，所以必须同时提供退回默认值的入口，
   * 否则管理员发现太频繁时只能逐个站点重填。
   */
  protected async resetIntervals(interval: number): Promise<void> {
    const ids = this.selected();
    if (!ids.length) return;
    this.msg.set('');
    try {
      const r = await this.api.resetIntervals(ids, interval);
      this.selected.set([]);
      await this.load(this.kw(), this.cat(), this.page());
      this.okMsg.set(true);
      const what = interval === 0 ? '按分类重算' : `统一改为 ${this.durationText(interval)}`;
      this.msg.set(r.changed > 0 ? `${what}：${r.changed} 个站点的抓取间隔已更新` : `${what}：没有站点需要改动`);
    } catch (e) {
      this.okMsg.set(false);
      this.msg.set(errMsg(e));
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
      await this.load(this.kw(), this.cat(), this.page());
    } catch (e) {
      this.msg.set(errMsg(e));
    }
  }

  private async batchToggle(enabled: boolean): Promise<void> {
    for (const id of this.selected()) await this.api.toggleWhitelist(id, enabled);
    await this.load(this.kw(), this.cat(), this.page());
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

  /** 正在编辑抓取规则的站点（null = 弹窗关闭）。 */
  protected readonly rule = signal<{
    id: number;
    domain: string;
    seed_path: string;
    article_pattern: string;
  } | null>(null);

  protected editRule(w: WhitelistSite): void {
    this.rule.set({
      id: w.id,
      domain: w.domain,
      seed_path: w.seed_path || '/',
      article_pattern: w.article_pattern || '',
    });
  }

  protected patchRule(k: 'seed_path' | 'article_pattern', v: string): void {
    const cur = this.rule();
    if (!cur) return;
    this.rule.set({ ...cur, [k]: v });
  }

  protected async saveRule(): Promise<void> {
    const cur = this.rule();
    if (!cur) return;
    try {
      await this.api.setWhitelistRule(cur.id, cur.seed_path, cur.article_pattern);
      this.rule.set(null);
      this.okMsg.set(true);
      this.msg.set('抓取规则已保存，下一轮抓取生效');
      await this.load(this.kw(), this.cat(), this.page());
    } catch (e) {
      this.okMsg.set(false);
      this.msg.set(errMsg(e));
    }
  }

  protected async add(): Promise<void> {
    try {
      await this.api.addWhitelist({
        domain: this.form().domain,
        category: this.form().category,
        crawl_interval: this.form().crawl_interval,
      });
      this.open.set(false);
      this.form.set({ domain: '', category: 'official', crawl_interval: 0 });
      await this.load(this.kw(), this.cat(), this.page());
    } catch (e) {
      this.msg.set(errMsg(e));
    }
  }
}
