import { Component, computed, inject, signal } from '@angular/core';
import { AdminApi } from '../../core/admin-api.service';
import type { UsageRow } from '../../core/api';
import { errMsg } from '../../core/http';

const EMPTY: UsageRow = { day: '', searches: 0, hits: 0, crawls: 0, indexed: 0, summaries: 0 };

const RANGES = [7, 30, 90];

/** 本地时区的日期键，与后端 usage.today() 保持一致（都用本地自然日）。 */
function dayKey(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

/**
 * 每日用量：检索 / 抓取 / 入库 / 原文摘要的按天累计。
 *
 * 数据只在后台可见——抓取页数与入库量是运营口径，公开出去只会变成
 * 「你今天怎么只抓了 3 页」的质问材料。图表用纯 CSS 柱状条，不引图表库：
 * 三十根柱子不值得为此多 100KB。
 */
@Component({
  selector: 'vx-usage-page',
  template: `
    <div class="row-between" style="margin-bottom: 14px">
      <span class="page-title">每日用量</span>
      <div class="row" style="gap: 6px">
        @for (d of RANGES; track d) {
          <button
            type="button"
            class="chip"
            [class.is-active]="days() === d"
            (click)="load(d)"
          >
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

    <div class="card" style="margin-top: 12px">
      <div class="section-title">检索趋势</div>
      @if (loading()) {
        <div class="loading"><span class="spinner"></span></div>
      } @else {
        <div class="chart">
          @for (p of series(); track p.day) {
            <div class="chart-col" [attr.title]="p.day + '：' + p.searches + ' 次检索'">
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
            <th class="num">返回结果</th>
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
              <td class="num mono">{{ r.hits }}</td>
              <td class="num mono">{{ r.crawls }}</td>
              <td class="num mono">{{ r.indexed }}</td>
              <td class="num mono">{{ r.summaries }}</td>
            </tr>
          } @empty {
            <tr>
              <td colspan="6" style="text-align: center; padding: 28px 0">
                <span class="faint small">还没有用量记录</span>
              </td>
            </tr>
          }
        </tbody>
      </table>
    </div>
  `,
  styles: `
    .stat-grid {
      display: grid;
      grid-template-columns: repeat(4, 1fr);
      gap: 12px;
    }

    .stat {
      display: flex;
      flex-direction: column;
      gap: 2px;
    }

    .stat-num {
      font-size: 24px;
      font-weight: 600;
      line-height: 1.2;
      color: var(--text-1);
    }

    .chart {
      display: flex;
      align-items: flex-end;
      gap: 3px;
      height: 120px;
      margin-top: 14px;
    }

    .chart-col {
      flex: 1;
      display: flex;
      align-items: flex-end;
      height: 100%;
    }

    .chart-bar {
      width: 100%;
      min-height: 2px;
      border-radius: 2px 2px 0 0;
      background: var(--brand);
    }

    .chart-bar.is-zero {
      background: var(--line);
    }

    .chart-axis {
      display: flex;
      justify-content: space-between;
      margin-top: 6px;
      color: var(--text-3);
      font-size: var(--fs-xs);
    }

    .num {
      text-align: right;
    }

    @media (max-width: 767.98px) {
      .stat-grid {
        grid-template-columns: repeat(2, 1fr);
      }
    }
  `,
})
export class UsagePage {
  private readonly api = inject(AdminApi);

  protected readonly RANGES = RANGES;
  protected readonly days = signal(30);
  protected readonly rows = signal<UsageRow[]>([]);
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
      const r = await this.api.usage(days);
      this.rows.set(r.rows ?? []);
    } catch (e) {
      this.error.set(errMsg(e));
      this.rows.set([]);
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
      { label: '今日检索', value: t.searches, hint: `返回 ${t.hits} 条结果` },
      { label: '今日抓取', value: t.crawls, hint: '个页面' },
      { label: '今日入库', value: t.indexed, hint: '篇文档' },
      { label: '今日摘要', value: t.summaries, hint: '次生成' },
    ];
  });

  /** 表格按日期倒序：最近的一天在最上面。 */
  protected readonly tableRows = computed(() => [...this.rows()].reverse());

  protected barPct(v: number): number {
    if (v <= 0) return 2;
    return Math.max(4, Math.round((v / this.maxSearches()) * 100));
  }
}
