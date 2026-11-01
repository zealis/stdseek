import { Component, DestroyRef, computed, inject, signal } from '@angular/core';
import { NavigationEnd, Router, RouterLink, RouterLinkActive } from '@angular/router';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { PrefsService } from '../core/prefs.service';
import { AuthService } from '../core/auth.service';
import { Confirm } from '../shared/ui/confirm';
import { PublicApi } from '../core/public-api.service';
import { SearchSession } from '../core/search-session';
import { Icon, type IconName } from '../shared/ui/icon';
import { BrandingService } from '../core/branding.service';
import { AdminTokenService } from '../core/token.service';
import { currentLocale, t, toggleLocale } from '../core/i18n';

/**
 * 顶栏导航。桌面端（≥640px）只画图标，文字退到 aria-label 与 title：
 * 图标按钮没有可见文字，这两样是它唯一的「名字」——读屏和悬停提示都指着它们，
 * 加导航项时漏写就是一个无名链接。移动端这条导航整条收起，交给底部 Tabbar。
 */
const NAV: { href: string; labelKey: string; icon: IconName; exact: boolean }[] = [
  { href: '/', labelKey: 'nav.search', icon: 'search', exact: true },
  { href: '/contribute', labelKey: 'nav.contribute', icon: 'send', exact: false },
  { href: '/settings', labelKey: 'nav.settings', icon: 'sliders', exact: false },
];

/**
 * 顶栏，两种形态（Google 式）：
 * - /search：左侧是紧凑搜索框（覆盖品牌区），导航仍在右侧；
 * - 其他页：品牌区 + 导航。
 * 表单自持查询词，随路由同步，回车提交回 /search 第 1 页。移动端导航交给底部 Tabbar。
 */
@Component({
  selector: 'vx-header',
  imports: [RouterLink, RouterLinkActive, Icon, Confirm],
  template: `
    <header class="header">
      <div class="page page-wide header-inner" [class.header-inner-results]="isSearch()">
        @if (isSearch()) {
          <a routerLink="/" class="brand">
            @if (brand()?.has_icon && !iconFailed()) {
              <img
                class="logo-mark logo-mark-img"
                [src]="brand()!.icon_url"
                alt=""
                width="22"
                height="22"
                (error)="iconFailed.set(true)"
              />
            } @else {
              <span class="logo-mark" aria-hidden="true"></span>
            }
            {{ brandParts().main }}
          </a>
          <form
            class="searchbar header-search"
            [class.is-filled]="q().trim() !== ''"
            (submit)="submit($event)"
            role="search"
          >
            <input
              class="searchbar-input"
              type="search"
              enterkeyhint="search"
              maxlength="256"
              [attr.aria-label]="t('search.placeholder')"
              autocomplete="off"
              role="combobox"
              aria-autocomplete="list"
              aria-controls="header-suggest"
              [attr.aria-expanded]="sugs().length > 0"
              [value]="q()"
              (focus)="onFocus(inputEl.value)"
              (input)="onInput(inputEl.value)"
              (keydown)="onKey($event)"
              (compositionstart)="composing = true"
              (compositionend)="onCompositionEnd(inputEl.value)"
              (blur)="closeSoon()"
              #inputEl
            />
            <button type="submit" class="btn btn-solid" [attr.aria-label]="t('search.button')">
              <vx-icon name="search" [size]="16" />
            </button>
            @if (sugs().length > 0) {
              <div
                id="header-suggest"
                class="suggest-panel"
                role="listbox"
                [attr.aria-label]="t('search.suggest')"
              >
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
            @if (brand()?.has_icon && !iconFailed()) {
              <img
                class="logo-mark logo-mark-img"
                [src]="brand()!.icon_url"
                alt=""
                width="22"
                height="22"
                (error)="iconFailed.set(true)"
              />
            } @else {
              <span class="logo-mark" aria-hidden="true"></span>
            }
            {{ brandParts().main }}
            @if (brandParts().sub) {
              <span class="brand-sub">{{ brandParts().sub }}</span>
            }
          </a>
        }
        <nav class="nav" aria-label="主导航">
          @for (n of nav(); track n.href) {
            <a
              [routerLink]="n.href"
              routerLinkActive="is-active"
              [routerLinkActiveOptions]="{ exact: n.exact }"
              class="nav-link"
              [attr.aria-label]="t(n.labelKey)"
              [attr.title]="t(n.labelKey)"
            >
              <vx-icon [name]="n.icon" [size]="16" />
            </a>
          }
          <!-- 语言开关：地球图标，两态切换。图标按钮没有文字，tooltip 直接写
               「切到哪种语言」而不是「切换语言」——后者点了才知道会发生什么。 -->
          <button
            type="button"
            class="nav-link nav-link-btn"
            [attr.aria-label]="t('nav.lang')"
            [attr.title]="locale() === 'zh-CN' ? 'English' : '中文'"
            (click)="switchLang()"
          >
            <vx-icon name="globe" [size]="16" />
          </button>
          @if (auth.isAdmin() || adminToken.token()) {
            <a
              routerLink="/manage/stats"
              routerLinkActive="is-active"
              class="nav-link"
              [attr.aria-label]="t('nav.admin')"
              [attr.title]="t('nav.admin')"
            >
              <vx-icon name="chart" [size]="16" />
            </a>
          }
          @if (auth.loggedIn()) {
            <button
              type="button"
              class="nav-link nav-link-btn"
              [attr.aria-label]="t('nav.logout')"
              [attr.title]="t('nav.logout')"
              (click)="askLogout()"
            >
              <vx-icon name="log-out" [size]="16" />
            </button>
          } @else if (auth.ready()) {
            <a
              routerLink="/login"
              routerLinkActive="is-active"
              class="nav-link"
              [attr.aria-label]="t('nav.login')"
              [attr.title]="t('nav.login')"
            >
              <vx-icon name="user" [size]="16" />
            </a>
          }
        </nav>
      </div>
    </header>

    @if (confirmOpen()) {
      <vx-confirm
        title="退出登录"
        desc="退出后需要重新输入账号密码才能进入后台。"
        confirmText="退出"
        [danger]="true"
        (confirmed)="logout()"
        (cancelled)="confirmOpen.set(false)"
      />
    }
  `,
})
export class Header {
  private readonly router = inject(Router);
  private readonly destroyRef = inject(DestroyRef);
  private readonly prefs = inject(PrefsService);
  private readonly session = inject(SearchSession);
  private readonly api = inject(PublicApi);
  private readonly branding = inject(BrandingService);
  protected readonly auth = inject(AuthService);
  /**
   * 后台令牌（localStorage）。
   *
   * 「管理后台」入口的判定要看它，不能只看 auth.isAdmin()：这台机器的管理员
   * 常常是「输一次令牌进后台」，并没有用管理员账号登录前台。只看登录态的话，
   * 宽屏（≥640 时底部 Tabbar 整条隐藏）上就找不到任何后台入口。
   */
  protected readonly adminToken = inject(AdminTokenService);
  /** 后台换过图标 / 站名后这里会立刻跟着变（见 BrandingService）。 */
  protected readonly brand = this.branding.current;
  protected readonly brandParts = this.branding.parts;
  /** 图标加载失败（老 iOS 拿到没有尺寸的 SVG 会这样）：退回默认章，别给用户看破图标。 */
  protected readonly iconFailed = signal(false);
  protected readonly confirmOpen = signal(false);

  protected readonly isSearch = signal(this.router.url.startsWith('/search'));
  /**
   * 结果页去掉「搜索」这一项：搜索框就在顶栏里，再来一个放大镜图标就是两个
   * 一样的图标并排（截图里看着像画重了）。回首页走左侧品牌名。
   */
  protected readonly nav = computed(() =>
    this.isSearch() ? NAV.filter((n) => n.href !== '/') : NAV,
  );

  /** 文案与当前语言：模板里直接调 `t()`，语言一变这些读取会重算。 */
  protected readonly t = t;
  protected readonly locale = currentLocale;
  protected switchLang(): void {
    void toggleLocale();
  }
  protected readonly q = signal(this.paramQ(this.router.url));

  constructor() {
    void this.branding.load();
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

  private blurTimer?: ReturnType<typeof setTimeout>;

  protected closeSoon(): void {
    // 等 mousedown 处理完再关，否则点在选项上会被当成「点了空白」
    clearTimeout(this.blurTimer);
    this.blurTimer = setTimeout(() => this.sugs.set([]), 120);
  }

  /**
   * 点回输入框时也要出联想词。
   *
   * 只监听 input 的话，第二次想在原词上接着改时列表是空的——用户会以为联想坏了
   * （「再点一下就没了」）。这里按当前已有关键词补一次，且先取消失焦时的延迟关闭，
   * 否则 120ms 后那次清空会把刚拿到的结果又抹掉。
   */
  protected onFocus(v: string): void {
    clearTimeout(this.blurTimer);
    const text = v.trim();
    if (!text || this.sugs().length > 0) return;
    void this.fetchSuggest(text);
  }

  private paramQ(url: string): string {
    return new URL(url, location.origin).searchParams.get('q') ?? '';
  }

  protected submit(event: Event): void {
    event.preventDefault();
    this.go(this.q().trim());
  }

  protected askLogout(): void {
    this.confirmOpen.set(true);
  }

  protected logout(): void {
    this.confirmOpen.set(false);
    void this.auth.logout().then(() => this.router.navigateByUrl('/'));
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
