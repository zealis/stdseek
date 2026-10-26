import { Component, computed, inject, signal } from '@angular/core';
import { PublicApi } from '../core/public-api.service';
import { errMsg } from '../core/http';
import type { PublicUsageRow } from '../core/api';

const EMPTY: PublicUsageRow = { day: '', searches: 0, succeeded: 0, api: 0, hits: 0 };
const RANGES = [7, 30, 90];

/** 与后端 usage.today() 一致：都按本地自然日切分。 */
function dayKey(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

/**
 * 每日用量（公开）：只有检索维度——请求数、成功搜索、返回结果、API 调用。
 * 抓取/入库/摘要带运营属性，只在后台 /manage/stats 出现。
 */
@Component({
  selector: 'vx-usage-page',
  template: `
    <div class="page" style="padding-top: 24px; padding-bottom: 48px">
      <h1 class="page-title">每日用量</h1>
      <p class="page-desc">本站检索请求按天统计，每 30 秒刷新一次。</p>

      @if (error()) {
        <div class="notice notice-error" style="margin-top: 12px">{{ error() }}</div>
      }

      <div class="stat-grid" style="margin-top: 16px">
        @for (s of stats(); track s.label) {
          <div class="card stat">
            <span class="faint small">{{ s.label }}</span>
            <strong class="stat-num mono">{{ s.value }}</strong>
            <span class="faint small">{{ s.hint }}</span>
          </div>
        }
      </div>

      <div class="card" style="margin-top: 12px">
        <div class="row-between">
          <span class="section-title">搜索请求趋势</span>
          <div class="row" style="gap: 6px">
            @for (d of RANGES; track d) {
              <button type="button" class="chip" [class.is-active]="days() === d" (click)="load(d)">
                {{ d }} 天
              </button>
            }
          </div>
        </div>
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
              <th class="num">搜索请求</th>
              <th class="num">成功搜索</th>
              <th class="num">API 请求</th>
              <th class="num">返回结果</th>
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
              </tr>
            } @empty {
              <tr>
                <td colspan="5" style="text-align: center; padding: 28px 0">
                  <span class="faint small">还没有用量记录</span>
                </td>
              </tr>
            }
          </tbody>
        </table>
      </div>
    </div>
  `,
})
export class UsagePage {
  private readonly api = inject(PublicApi);

  protected readonly RANGES = RANGES;
  protected readonly days = signal(30);
  protected readonly rows = signal<PublicUsageRow[]>([]);
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

  /** 补齐没有记录的日期，趋势图的横轴必须连续。 */
  protected readonly series = computed<PublicUsageRow[]>(() => {
    const map = new Map(this.rows().map((r) => [r.day, r]));
    const n = this.days();
    const now = new Date();
    const out: PublicUsageRow[] = [];
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
    const rate = t.searches > 0 ? Math.round((t.succeeded / t.searches) * 100) : 0;
    return [
      { label: '今日搜索请求', value: t.searches, hint: '次' },
      { label: '今日成功搜索', value: t.succeeded, hint: `命中率 ${rate}%` },
      { label: '今日返回结果', value: t.hits, hint: '条' },
      { label: '今日 API 请求', value: t.api, hint: '来自外部调用' },
    ];
  });

  /** 表格按日期倒序：最近一天在最上面。 */
  protected readonly tableRows = computed(() => [...this.rows()].reverse());

  protected barPct(v: number): number {
    if (v <= 0) return 2;
    return Math.max(4, Math.round((v / this.maxSearches()) * 100));
  }

  protected tip(p: PublicUsageRow): string {
    return `${p.day}：请求 ${p.searches} · 成功 ${p.succeeded} · 结果 ${p.hits} 条 · API ${p.api}`;
  }
}
