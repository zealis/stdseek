import { Component, computed, inject, signal } from '@angular/core';
import { PublicApi } from '../core/public-api.service';
import { errMsg } from '../core/http';
import type { PublicUsageRow } from '../core/api';
import { t } from '../core/i18n';

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
      <h1 class="page-title">{{ t('usage.title') }}</h1>
      <p class="page-desc">{{ t('usage.desc') }}</p>

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
          <span class="section-title">{{ t('usage.trend') }}</span>
          <div class="row" style="gap: 6px">
            @for (d of RANGES; track d) {
              <button type="button" class="chip" [class.is-active]="days() === d" (click)="load(d)">
                {{ t('usage.days', { n: d }) }}
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
            <span>{{ t('usage.today') }}</span>
          </div>
        }
      </div>

      <div class="table-wrap" style="margin-top: 12px">
        <table class="table">
          <thead>
            <tr>
              <th>{{ t('usage.th.date') }}</th>
              <th class="num">{{ t('usage.th.searches') }}</th>
              <th class="num">{{ t('usage.th.succeeded') }}</th>
              <th class="num">{{ t('usage.th.api') }}</th>
              <th class="num">{{ t('usage.th.hits') }}</th>
            </tr>
          </thead>
          <tbody>
            @for (r of tableRows(); track r.day) {
              <tr>
                <td class="mono" [attr.data-label]="t('usage.th.date')">{{ r.day }}</td>
                <td class="num mono" [attr.data-label]="t('usage.th.searches')">
                  {{ r.searches }}
                </td>
                <td class="num mono" [attr.data-label]="t('usage.th.succeeded')">
                  {{ r.succeeded }}
                </td>
                <td class="num mono" [attr.data-label]="t('usage.th.api')">{{ r.api }}</td>
                <td class="num mono" [attr.data-label]="t('usage.th.hits')">{{ r.hits }}</td>
              </tr>
            } @empty {
              <tr>
                <td colspan="5" style="text-align: center; padding: 28px 0">
                  <span class="faint small">{{ t('usage.empty') }}</span>
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
  protected readonly t = t;
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
    // 变量不能再叫 t：会遮住字典函数（模板里已经踩过三次）。
    const today = this.series().at(-1) ?? EMPTY;
    const rate = today.searches > 0 ? Math.round((today.succeeded / today.searches) * 100) : 0;
    // 在 computed 里读 t()：语言一变它自己重算，模板不用改。
    return [
      { label: t('usage.stat.searches'), value: today.searches, hint: t('usage.unit.times') },
      { label: t('usage.stat.succeeded'), value: today.succeeded, hint: t('usage.rate', { rate }) },
      { label: t('usage.stat.hits'), value: today.hits, hint: t('usage.unit.hits') },
      { label: t('usage.stat.api'), value: today.api, hint: t('usage.fromApi') },
    ];
  });

  /** 表格按日期倒序：最近一天在最上面。 */
  protected readonly tableRows = computed(() => [...this.rows()].reverse());

  protected barPct(v: number): number {
    if (v <= 0) return 2;
    return Math.max(4, Math.round((v / this.maxSearches()) * 100));
  }

  protected tip(p: PublicUsageRow): string {
    return t('usage.tip', {
      day: p.day,
      searches: p.searches,
      succeeded: p.succeeded,
      hits: p.hits,
      api: p.api,
    });
  }
}
