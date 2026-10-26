import { Component, DestroyRef, inject, signal } from '@angular/core';
import { NavigationEnd, Router, RouterLink, RouterLinkActive } from '@angular/router';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { PrefsService } from '../core/prefs.service';
import { PublicApi } from '../core/public-api.service';
import { SearchSession } from '../core/search-session';
import { Icon } from '../shared/ui/icon';

const NAV = [
  { href: '/', label: '搜索', exact: true },
  { href: '/contribute', label: '提交站点', exact: false },
  { href: '/settings', label: '设置', exact: false },
  { href: '/manage/whitelist', label: '管理后台', exact: false },
];

/**
 * 顶栏，两种形态（Google 式）：
 * - /search：左侧是紧凑搜索框（覆盖品牌区），导航仍在右侧；
 * - 其他页：品牌区 + 导航。
 * 表单自持查询词，随路由同步，回车提交回 /search 第 1 页。移动端导航交给底部 Tabbar。
 */
@Component({
  selector: 'vx-header',
  imports: [RouterLink, RouterLinkActive, Icon],
  template: `
    <header class="header">
      <div class="page page-wide header-inner" [class.header-inner-results]="isSearch()">
        @if (isSearch()) {
          <a routerLink="/" class="brand">
            <span class="logo-mark" aria-hidden="true"></span>
            voidxo
          </a>
          <form class="searchbar header-search" (submit)="submit($event)" role="search">
            <input
              class="searchbar-input"
              type="search"
              enterkeyhint="search"
              aria-label="搜索关键词"
              autocomplete="off"
              role="combobox"
              aria-autocomplete="list"
              aria-controls="header-suggest"
              [attr.aria-expanded]="sugs().length > 0"
              [value]="q()"
              (input)="onInput(inputEl.value)"
              (keydown)="onKey($event)"
              (compositionstart)="composing = true"
              (compositionend)="onCompositionEnd(inputEl.value)"
              (blur)="closeSoon()"
              #inputEl
            />
            <button type="submit" class="btn btn-solid" aria-label="搜索">
              <vx-icon name="search" [size]="16" />
            </button>
            @if (sugs().length > 0) {
              <div id="header-suggest" class="suggest-panel" role="listbox" aria-label="搜索建议">
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
        } @else {
          <a routerLink="/" class="brand">
            <span class="logo-mark" aria-hidden="true"></span>
            voidxo
            <span class="brand-sub">维空</span>
          </a>
        }
        <nav class="nav" aria-label="主导航">
          @for (n of nav; track n.href) {
            <a
              [routerLink]="n.href"
              routerLinkActive="is-active"
              [routerLinkActiveOptions]="{ exact: n.exact }"
              class="nav-link"
            >
              {{ n.label }}
            </a>
          }
        </nav>
      </div>
    </header>
  `,
})
export class Header {
  private readonly router = inject(Router);
  private readonly destroyRef = inject(DestroyRef);
  private readonly prefs = inject(PrefsService);
  private readonly session = inject(SearchSession);
  private readonly api = inject(PublicApi);

  protected readonly nav = NAV;
  protected readonly isSearch = signal(this.router.url.startsWith('/search'));
  protected readonly q = signal(this.paramQ(this.router.url));

  constructor() {
    this.router.events.pipe(takeUntilDestroyed(this.destroyRef)).subscribe((e) => {
      if (e instanceof NavigationEnd) {
        this.isSearch.set(e.urlAfterRedirects.startsWith('/search'));
        this.q.set(this.paramQ(e.urlAfterRedirects));
      }
    });
  }

  // ---- 搜索补全 ----
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
      // 迟到的响应要丢掉：用户可能已经改成别的词了
      if (seq !== this.sugSeq) return;
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

  /** 选中补全项。用 mousedown 触发并阻止默认行为，否则输入框先失焦、面板已经关了。 */
  protected pick(s: string, e: Event): void {
    e.preventDefault();
    this.q.set(s);
    this.go(s);
  }

  protected closeSoon(): void {
    // 等 mousedown 处理完再关，否则点在选项上会被当成「点了空白」
    setTimeout(() => this.sugs.set([]), 120);
  }

  private paramQ(url: string): string {
    return new URL(url, location.origin).searchParams.get('q') ?? '';
  }

  protected submit(event: Event): void {
    event.preventDefault();
    this.go(this.q().trim());
  }

  private go(v: string): void {
    this.sugs.set([]);
    if (!v) return;
    if (this.prefs.getRequest()) {
      void this.router.navigate(['/search'], { queryParams: { q: v, page: 1 } });
      return;
    }
    // POST 模式：查询只留在内存里，地址栏不带任何参数
    this.session.setQuery(v);
    void this.router.navigate(['/search']);
  }
}
