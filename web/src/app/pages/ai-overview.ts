import { Component, computed, effect, inject, input, signal } from '@angular/core';
import { type OverviewResp } from '../core/api';
import { PrefsService } from '../core/prefs.service';
import { PublicApi } from '../core/public-api.service';

/**
 * AI 概览：搜索页顶部的一段回答。
 *
 * 是否生成由后端判定（只有提问式查询才生成）；前端每次搜索都问一次，
 * 普通关键词只拿到 question=false，不显示卡片、也不消耗模型调用。
 * 本机偏好（设置 → 搜索偏好 → AI 概览）关掉后连请求都不发。
 */
@Component({
  selector: 'vx-ai-overview',
  template: `
    @if (on() && q().trim() && (loading() || data() || failed())) {
      <section class="ai-overview" aria-live="polite">
        <div class="row wrap" style="gap: 8px; align-items: center">
          <span class="chip chip-accent">AI 概览</span>
          <span class="faint small">AI 生成，请核实重要信息</span>
          @if (loading()) {
            <span class="spinner"></span>
          }
        </div>
        @if (data(); as d) {
          <p class="ai-overview-text" [class.is-clamped]="!expanded() && long()">{{ d.answer }}</p>
          @if ((d.sources ?? []).length > 0) {
            <div class="row wrap ai-overview-sources">
              @for (s of d.sources; track s.id) {
                <a
                  class="chip"
                  [href]="s.url"
                  target="_blank"
                  rel="noreferrer"
                  [title]="s.title"
                >[{{ s.id }}] {{ s.domain }}</a>
              }
            </div>
          }
          @if (long()) {
            <button
              type="button"
              class="btn btn-text btn-sm"
              style="margin-top: 4px"
              (click)="expanded.set(!expanded())"
            >
              {{ expanded() ? '收起' : '显示更多' }}
            </button>
          }
        }
        @if (failed()) {
          <div class="row wrap" style="gap: 8px; align-items: center; margin-top: 6px">
            <span class="faint small">{{ failed() }}</span>
            <button type="button" class="btn btn-text btn-sm" (click)="retry()">重试</button>
          </div>
        }
      </section>
    }
  `,
})
export class AiOverview {
  private readonly api = inject(PublicApi);
  private readonly prefs = inject(PrefsService);

  readonly q = input('');
  /** 后台「AI 概览」功能开关；本机偏好再关一层。 */
  readonly enabled = input(false);

  protected readonly on = computed(() => this.enabled() && this.prefs.overview());
  protected readonly loading = signal(false);
  protected readonly data = signal<OverviewResp | null>(null);
  protected readonly failed = signal('');
  protected readonly expanded = signal(false);
  protected readonly long = computed(() => (this.data()?.answer?.length ?? 0) > 220);

  private seq = 0;
  private slowTimer: ReturnType<typeof setTimeout> | null = null;

  constructor() {
    effect(() => {
      const q = this.q().trim();
      const on = this.on();
      this.expanded.set(false);
      this.data.set(null);
      this.failed.set('');
      this.loading.set(false);
      if (this.slowTimer) {
        clearTimeout(this.slowTimer);
        this.slowTimer = null;
      }
      if (!q || !on) return;
      const seq = ++this.seq;
      // 非提问式查询后端会立刻回 question=false；延迟 250ms 再显示加载态，
      // 避免每次普通搜索都闪一下「生成中」。
      this.slowTimer = setTimeout(() => {
        if (seq === this.seq) this.loading.set(true);
      }, 250);
      void this.load(q, seq);
    });
  }

  protected retry(): void {
    const q = this.q().trim();
    if (!q || !this.on()) return;
    this.data.set(null);
    this.failed.set('');
    this.loading.set(true);
    const seq = ++this.seq;
    void this.load(q, seq);
  }

  private async load(q: string, seq: number): Promise<void> {
    try {
      const r = await this.api.overview(q);
      if (seq !== this.seq) return;
      if (r.question && r.answer) {
        this.data.set(r);
      } else if (r.question) {
        // 后端会给出可读原因（未配置接口 / 没有足够信息 / 模型暂时不可用）。
        this.failed.set(r.reason || 'AI 概览暂时不可用');
      }
    } catch {
      // 概览是增强项：失败不打断搜索，只在这张卡里给一个重试入口。
      if (seq === this.seq) this.failed.set('AI 概览暂时不可用');
    } finally {
      if (seq === this.seq) {
        this.loading.set(false);
        if (this.slowTimer) {
          clearTimeout(this.slowTimer);
          this.slowTimer = null;
        }
      }
    }
  }
}
