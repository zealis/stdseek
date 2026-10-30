import { Component, inject, signal } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { PublicApi } from '../core/public-api.service';
import { BrandingService } from '../core/branding.service';
import { PrefsService } from '../core/prefs.service';
import { SearchSession } from '../core/search-session';
import { type Status } from '../core/api';
import { Icon } from '../shared/ui/icon';

const EXAMPLES = ['crashloopbackoff', 'gse 分词', 'hamming distance'];

/**
 * 首页。运行状态挂载后取：首屏先把搜索框交出去，统计数字后到不阻塞输入。
 * 空索引是最常见的首部署状态——不给引导的话，用户第一反应是「这东西坏了」。
 */
@Component({
  selector: 'vx-home-page',
  imports: [Icon, RouterLink],
  template: `
    <div class="page hero">
      <h1 class="hero-title">
        <span class="hero-grad">{{ brandParts().main }}</span>
        @if (brandParts().sub) {
          <span class="brand-sub">{{ brandParts().sub }}</span>
        }
      </h1>

      <form
        class="searchbar hero-form"
        [class.is-filled]="q().trim() !== ''"
        (submit)="submit($event)"
        role="search"
      >
        <input
          class="searchbar-input"
          type="search"
          enterkeyhint="search"
          maxlength="256"
          placeholder="隐私持续保护中"
          aria-label="搜索关键词"
          autocomplete="off"
          autofocus
          role="combobox"
          aria-autocomplete="list"
          aria-controls="home-suggest"
          [attr.aria-expanded]="sugs().length > 0"
          [value]="q()"
          (input)="onInput(inputEl.value)"
          (keydown)="onKey($event)"
          (compositionstart)="composing = true"
          (compositionend)="onCompositionEnd(inputEl.value)"
          (blur)="closeSoon()"
          #inputEl
        />
        <button type="submit" class="btn btn-solid btn-lg">
          <vx-icon name="search" [size]="16" />
          搜索
        </button>
        @if (sugs().length > 0) {
          <div id="home-suggest" class="suggest-panel" role="listbox" aria-label="搜索建议">
            @for (s of sugs(); track s; let i = $index) {
              <button
                type="button"
                class="suggest-item"
                role="option"
                [class.is-active]="active() === i"
                [attr.aria-selected]="active() === i"
                (mouseenter)="active.set(i)"
                (mousedown)="pick(s, $event)"
              >
                {{ s }}
              </button>
            }
          </div>
        }
      </form>

      @if (empty()) {
        <div class="notice guide">
          <div class="notice-body">
            <div class="section-title">索引还是空的</div>
            <p class="muted" style="margin-top: 4px">还没有抓取任何内容，先录入站点再执行一次抓取。</p>
            <div class="row" style="margin-top: 10px">
              <a class="btn btn-solid btn-sm" routerLink="/manage/whitelist">去后台加站点</a>
              <span class="faint small">或启动时加 -crawl-once</span>
            </div>
          </div>
        </div>
      } @else {
        <div class="hero-examples">
          @for (s of examples; track s) {
            <a class="btn btn-ghost btn-sm" [routerLink]="'/search'" [queryParams]="{ q: s }">
              {{ s }}
            </a>
          }
        </div>
      }

    </div>
  `,
})
export class HomePage {
  private readonly api = inject(PublicApi);
  private readonly branding = inject(BrandingService);
  protected readonly brandParts = this.branding.parts;
  private readonly router = inject(Router);
  private readonly prefs = inject(PrefsService);
  private readonly session = inject(SearchSession);

  protected readonly examples = EXAMPLES;
  protected readonly q = signal('');
  protected readonly st = signal<Status | null>(null);

  // 空库时首页会显示引导卡片，提示去后台加站点或跑一次抓取
  protected readonly empty = signal(false);

  constructor() {
    void this.api.status().then((s) => {
      this.st.set(s);
      this.empty.set(s.documents === 0);
    });
  }

  protected submit(event: Event): void {
    event.preventDefault();
    this.go(this.q().trim());
  }

  private go(v: string): void {
    this.sugs.set([]);
    if (!v) return;
    if (this.prefs.getRequest()) {
      void this.router.navigate(['/search'], { queryParams: { q: v } });
      return;
    }
    this.session.setQuery(v);
    void this.router.navigate(['/search']);
  }

  // ---- 搜索补全（与 header 同一套行为，逐处实现是为了各自持有状态） ----
  protected readonly sugs = signal<string[]>([]);
  protected readonly active = signal(-1);
  protected composing = false;
  private sugTimer: ReturnType<typeof setTimeout> | null = null;
  private sugSeq = 0;

  protected onInput(v: string): void {
    this.q.set(v);
    // 输入法组合期间不请求：拼音还没上屏时，每个字母都是一次无意义的往返
    if (this.composing) return;
    this.scheduleSuggest(v);
  }

  protected onCompositionEnd(v: string): void {
    this.composing = false;
    this.q.set(v);
    this.scheduleSuggest(v);
  }

  private scheduleSuggest(raw: string): void {
    if (this.sugTimer) clearTimeout(this.sugTimer);
    const text = raw.trim();
    if (!text) {
      this.sugs.set([]);
      return;
    }
    this.sugTimer = setTimeout(() => void this.fetchSuggest(text), 200);
  }

  private async fetchSuggest(text: string): Promise<void> {
    const seq = ++this.sugSeq;
    try {
      const r = await this.api.suggest(text);
      if (seq !== this.sugSeq) return; // 迟到的响应直接丢掉
      this.sugs.set(r.suggestions ?? []);
      this.active.set(-1);
    } catch {
      if (seq === this.sugSeq) this.sugs.set([]);
    }
  }

  protected onKey(e: KeyboardEvent): void {
    const list = this.sugs();
    if (!list.length) return;
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      this.active.update((i) => (i + 1) % list.length);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      this.active.update((i) => (i <= 0 ? list.length - 1 : i - 1));
    } else if (e.key === 'Escape') {
      this.sugs.set([]);
    } else if (e.key === 'Enter' && this.active() >= 0) {
      e.preventDefault();
      this.go(list[this.active()]);
    }
  }

  /** 用 mousedown 而非 click：click 之前输入框已经失焦，面板先关了。 */
  protected pick(s: string, e: Event): void {
    e.preventDefault();
    this.q.set(s);
    this.go(s);
  }

  protected closeSoon(): void {
    setTimeout(() => this.sugs.set([]), 120);
  }
}
