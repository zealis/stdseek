import { Component, computed, inject, signal } from '@angular/core';
import { AdminApi } from '../../core/admin-api.service';
import type { DimRow, UsageRow } from '../../core/api';
import { errMsg } from '../../core/http';

const EMPTY: UsageRow = {
  day: '',
  searches: 0,
  succeeded: 0,
  api: 0,
  hits: 0,
  crawls: 0,
  indexed: 0,
  summaries: 0,
};
const RANGES = [7, 30, 90];

/** 本地时区的日期键，与后端 usage.today() 一致（都按本地自然日）。 */
function dayKey(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

/**
 * 站点统计（后台）。
 *
 * 与前台 /usage 的区别：这里多出抓取、入库、原文摘要三个运营口径的指标，
 * 前台只放检索维度。两页共用 .stat-grid / .chart 这组全局样式。
 */
@Component({
  selector: 'vx-stats-page',
  template: `
    <div class="row-between" style="margin-bottom: 14px">
      <span class="page-title">站点统计</span>
      <div class="row" style="gap: 6px">
        @for (d of RANGES; track d) {
          <button type="button" class="chip" [class.is-active]="days() === d" (click)="load(d)">
            {{ d }} 天
          </button>
        }
      </div>
    </div>

    @if (error()) {
      <div class="notice notice-error" style="margin-bottom: 12px">{{ error() }}</div>
    }

    <div class="stat-grid">
      @for (s of stats(); track s.label) {
        <div class="card stat">
          <span class="faint small">{{ s.label }}</span>
          <strong class="stat-num mono">{{ s.value }}</strong>
          <span class="faint small">{{ s.hint }}</span>
        </div>
      }
    </div>

    <div class="rank-grid">
      <div class="card">
        <div class="section-title">API 调用来源</div>
        @if (origins().length === 0) {
          <p class="faint small" style="margin-top: 8px">这个时间窗内还没有外部调用</p>
        } @else {
          <div style="margin-top: 8px">
            @for (o of origins(); track o.name) {
              <div class="rank-row">
                <span class="rank-name mono" [title]="o.name">{{ o.name }}</span>
                <span class="rank-bar"><i [style.width.%]="pct(o.count, maxOrigin())"></i></span>
                <span class="rank-val mono">{{ o.count }}</span>
              </div>
            }
          </div>
        }
      </div>

      <div class="card">
        <div class="section-title">请求地区</div>
        @if (countries().length > 0) {
          <!-- 有历史数据就先展示：关掉 geo-header 不该让已有统计凭空消失 -->
          <div style="margin-top: 8px">
            @for (c of countries(); track c.name) {
              <div class="rank-row">
                <span class="rank-name mono">{{ c.name }}</span>
                <span class="rank-bar"><i [style.width.%]="pct(c.count, maxCountry())"></i></span>
                <span class="rank-val mono">{{ c.count }}</span>
              </div>
            }
          </div>
        } @else if (!geoHeader()) {
          <p class="faint small" style="margin-top: 8px">
            未启用：启动时加 <code class="mono">-geo-header CF-IPCountry</code>（部署在 CDN 后面时才有真实数据）
          </p>
        } @else {
          <p class="faint small" style="margin-top: 8px">这个时间窗内还没有地区数据</p>
        }
      </div>
    </div>

    <div class="card" style="margin-top: 12px">
      <div class="section-title">检索趋势</div>
      @if (loading()) {
        <div class="loading"><span class="spinner"></span></div>
      } @else {
        <div class="chart">
          @for (p of series(); track p.day) {
            <div class="chart-col" [attr.title]="tip(p)">
              <div
                class="chart-bar"
                [class.is-zero]="p.searches === 0"
                [style.height.%]="barPct(p.searches)"
              ></div>
            </div>
          }
        </div>
        <div class="chart-axis mono">
          <span>{{ series()[0]?.day }}</span>
          <span>今日</span>
        </div>
      }
    </div>

    <div class="table-wrap" style="margin-top: 12px">
      <table class="table">
        <thead>
          <tr>
            <th>日期</th>
            <th class="num">检索</th>
            <th class="num">成功</th>
            <th class="num">API</th>
            <th class="num">结果</th>
            <th class="num">抓取</th>
            <th class="num">入库</th>
            <th class="num">摘要</th>
          </tr>
        </thead>
        <tbody>
          @for (r of tableRows(); track r.day) {
            <tr>
              <td class="mono">{{ r.day }}</td>
              <td class="num mono">{{ r.searches }}</td>
              <td class="num mono">{{ r.succeeded }}</td>
              <td class="num mono">{{ r.api }}</td>
              <td class="num mono">{{ r.hits }}</td>
              <td class="num mono">{{ r.crawls }}</td>
              <td class="num mono">{{ r.indexed }}</td>
              <td class="num mono">{{ r.summaries }}</td>
            </tr>
          } @empty {
            <tr>
              <td colspan="8" style="text-align: center; padding: 28px 0">
                <span class="faint small">还没有用量记录</span>
              </td>
            </tr>
          }
        </tbody>
      </table>
    </div>
  `,
})
export class StatsPage {
  private readonly api = inject(AdminApi);

  protected readonly RANGES = RANGES;
  protected readonly days = signal(30);
  protected readonly rows = signal<UsageRow[]>([]);
  protected readonly origins = signal<DimRow[]>([]);
  protected readonly countries = signal<DimRow[]>([]);
  protected readonly geoHeader = signal('');
  protected readonly loading = signal(true);
  protected readonly error = signal('');

  constructor() {
    void this.load(this.days());
  }

  protected async load(days: number): Promise<void> {
    this.days.set(days);
    this.loading.set(true);
    this.error.set('');
    try {
      // 两个接口并行：排行与趋势互不依赖，串行只会让页面多等一个往返
      const [r, d] = await Promise.all([this.api.usage(days), this.api.usageDims(days)]);
      this.rows.set(r.rows ?? []);
      this.origins.set(d.origins ?? []);
      this.countries.set(d.countries ?? []);
      this.geoHeader.set(d.geo_header ?? '');
    } catch (e) {
      this.error.set(errMsg(e));
      this.rows.set([]);
      this.origins.set([]);
      this.countries.set([]);
    } finally {
      this.loading.set(false);
    }
  }

  /** 补齐缺失日期：后端只返回有记录的天，趋势图需要连续的横轴。 */
  protected readonly series = computed<UsageRow[]>(() => {
    const map = new Map(this.rows().map((r) => [r.day, r]));
    const n = this.days();
    const now = new Date();
    const out: UsageRow[] = [];
    for (let i = n - 1; i >= 0; i--) {
      const d = new Date(now.getFullYear(), now.getMonth(), now.getDate() - i);
      const key = dayKey(d);
      out.push(map.get(key) ?? { ...EMPTY, day: key });
    }
    return out;
  });

  private readonly maxSearches = computed(() =>
    Math.max(1, ...this.series().map((r) => r.searches)),
  );

  protected readonly stats = computed(() => {
    const t = this.series().at(-1) ?? EMPTY;
    return [
      { label: '今日检索', value: t.searches, hint: `成功 ${t.succeeded} 次 · API ${t.api} 次` },
      { label: '今日抓取', value: t.crawls, hint: '个页面' },
      { label: '今日入库', value: t.indexed, hint: '篇文档' },
      { label: '今日摘要', value: t.summaries, hint: '次生成' },
    ];
  });

  /** 表格按日期倒序：最近的一天在最上面。 */
  protected readonly tableRows = computed(() => [...this.rows()].reverse());

  protected readonly maxOrigin = computed(() =>
    Math.max(1, ...this.origins().map((o) => o.count)),
  );
  protected readonly maxCountry = computed(() =>
    Math.max(1, ...this.countries().map((c) => c.count)),
  );

  protected pct(v: number, max: number): number {
    return Math.max(3, Math.round((v / max) * 100));
  }

  protected barPct(v: number): number {
    if (v <= 0) return 2;
    return Math.max(4, Math.round((v / this.maxSearches()) * 100));
  }

  protected tip(p: UsageRow): string {
    return `${p.day}：请求 ${p.searches} · 成功 ${p.succeeded} · API ${p.api} · 结果 ${p.hits}`;
  }
}
