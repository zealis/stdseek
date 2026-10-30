import { Component, inject, signal } from '@angular/core';
import { AdminApi } from '../../core/admin-api.service';
import { errMsg } from '../../core/http';
import { precheckRejected, precheckStatus } from '../../core/precheck';
import {
  CATEGORIES,
  CRAWL_INTERVALS,
  categoryInterval,
  type PrecheckResult,
} from '../../core/api';
import { durationText } from '../../shared/format';
import { Icon } from '../../shared/ui/icon';

/**
 * 管理员直接提交站点进白名单（与用户提交审核队列区分）。
 * 提交前先预检：可达性、内容类型、robots、黑名单历史一屏看全再决定。
 */
@Component({
  selector: 'vx-submit-page',
  imports: [Icon],
  template: `
    <div class="card" style="max-width: 640px">
      <h2 class="section-title">提交站点</h2>

      <div class="form-grid" style="margin-top: 16px">
        <label class="field">
          <span class="field-label">域名</span>
          <div class="row wrap">
            <input
              class="input"
              type="text"
              autocapitalize="off"
              style="flex: 1; min-width: 200px"
              placeholder="go.dev"
              [value]="domain()"
              (input)="domain.set(domainEl.value.trim().toLowerCase()); pre.set(null)"
              #domainEl
            />
            <button
              type="button"
              class="btn"
              [disabled]="!domain() || checking()"
              (click)="doPrecheck()"
            >
              {{ checking() ? '预检中' : '预检' }}
            </button>
          </div>
        </label>

        @if (pre(); as p) {
          <div class="row wrap">
            <span class="chip" [class]="p.reachable ? 'chip-accent' : rejected(p) ? 'chip-warning' : 'chip-danger'">
              {{ statusText(p) }}
            </span>
            <span class="chip" [class.chip-accent]="p.is_html">
              {{ p.is_html ? 'HTML 页面' : '非 HTML（' + (p.content_type || '未知') + '）' }}
            </span>
            <span class="chip" [class]="p.robots_allowed ? 'chip-accent' : 'chip-warning'">
              {{ p.robots_found ? (p.robots_allowed ? 'robots 允许' : 'robots 禁止') : '无 robots.txt' }}
            </span>
            @if (blacklisted()) {
              <span class="chip chip-danger">命中黑名单历史</span>
            }
          </div>
        }

        @if (pre()?.error) {
          <div class="notice notice-error">{{ pre()!.error }}</div>
        }
        @if (pre() && !pre()!.reachable && !pre()!.error) {
          <div class="notice notice-warning">
            @if (rejected(pre()!)) {
              站点返回 {{ pre()!.status_code }}，是站点主动拒绝（可能需要登录，或屏蔽了爬虫 / 机房 IP），不是网络不通。加进白名单也会一篇都抓不到。
            } @else {
              站点不可达，加入白名单后爬虫会记录失败状态。
            }
          </div>
        }
        @if (pre() && !pre()!.robots_allowed) {
          <div class="notice notice-warning">robots.txt 禁止抓取首页，请确认后再加入。</div>
        }
        @if (pre()?.final_url && pre()!.final_url !== pre()!.url) {
          <p class="faint small">最终地址 {{ pre()!.final_url }}</p>
        }

        <label class="field">
          <span class="field-label">分类</span>
          <select
            class="input select"
            [value]="category()"
            (change)="category.set(catEl.value)"
            #catEl
          >
            @for (c of categories; track c.value) {
              <option [value]="c.value" [selected]="c.value === category()">{{ c.label }}</option>
            }
          </select>
        </label>
        <label class="field">
          <span class="field-label">抓取间隔</span>
          <!-- 「跟随分类」那一项会随上面的分类实时变（新闻 30 分钟、官方站 1 天…）。
               用户选过具体档位以后就不再被分类改动覆盖——显式选择要留住。 -->
          <select
            class="input select"
            [value]="interval()"
            (change)="interval.set(parseInterval(intervalEl.value))"
            #intervalEl
          >
            <option [value]="0" [selected]="interval() === 0">
              跟随分类（{{ durationText(categoryInterval(category())) }}）
            </option>
            @for (iv of intervals; track iv.value) {
              <option [value]="iv.value" [selected]="iv.value === interval()">{{ iv.label }}</option>
            }
          </select>
        </label>

        @if (err()) {
          <div class="notice notice-error">{{ err() }}</div>
        }
        @if (msg()) {
          <div class="notice notice-success">{{ msg() }}</div>
        }

        <div>
          <button type="button" class="btn btn-solid" [disabled]="!domain()" (click)="add()">
            <vx-icon name="check" [size]="16" />
            加入白名单
          </button>
        </div>
      </div>
    </div>
  `,
})
export class SubmitPage {
  private readonly api = inject(AdminApi);

  protected readonly categories = CATEGORIES;
  protected readonly intervals = CRAWL_INTERVALS;
  protected readonly categoryInterval = categoryInterval;
  protected readonly durationText = durationText;
  protected readonly statusText = precheckStatus;
  protected readonly rejected = precheckRejected;

  /** 0 表示「跟随分类」，后端按分类换算。 */
  protected parseInterval(v: string): number {
    const n = Number(v);
    return Number.isFinite(n) && n >= 0 ? Math.floor(n) : 0;
  }

  protected readonly domain = signal('');
  protected readonly category = signal('official');
  /** 0 = 跟随分类默认；后端按分类换算（见 sitecat.IntervalFor）。 */
  protected readonly interval = signal(0);
  protected readonly msg = signal('');
  protected readonly err = signal('');
  protected readonly pre = signal<PrecheckResult | null>(null);
  protected readonly blacklisted = signal(false);
  protected readonly checking = signal(false);

  protected async doPrecheck(): Promise<void> {
    this.err.set('');
    this.pre.set(null);
    this.blacklisted.set(false);
    this.checking.set(true);
    try {
      const r = await this.api.precheck(this.domain());
      this.pre.set(r.precheck);
      this.blacklisted.set(r.blacklisted);
    } catch (e) {
      this.err.set(errMsg(e));
    } finally {
      this.checking.set(false);
    }
  }

  protected async add(): Promise<void> {
    this.err.set('');
    this.msg.set('');
    try {
      await this.api.addWhitelist({
        domain: this.domain(),
        category: this.category(),
        crawl_interval: this.interval(),
      });
      this.msg.set(`${this.domain()} 已加入白名单，将作为爬虫种子。`);
      this.domain.set('');
      this.category.set('official');
      this.interval.set(0);
      this.pre.set(null);
      this.blacklisted.set(false);
    } catch (e) {
      this.err.set(errMsg(e));
    }
  }
}
