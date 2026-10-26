import { Component, DestroyRef, inject, signal } from '@angular/core';
import { NavigationEnd, Router, RouterLink, RouterLinkActive } from '@angular/router';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
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
              [value]="q()"
              (input)="q.set(inputEl.value)"
              #inputEl
            />
            <button type="submit" class="btn btn-solid" aria-label="搜索">
              <vx-icon name="search" [size]="16" />
            </button>
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

  private paramQ(url: string): string {
    return new URL(url, location.origin).searchParams.get('q') ?? '';
  }

  protected submit(event: Event): void {
    event.preventDefault();
    const v = this.q().trim();
    if (v) void this.router.navigate(['/search'], { queryParams: { q: v, page: 1 } });
  }
}
