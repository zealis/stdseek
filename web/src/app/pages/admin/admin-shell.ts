import { Component, inject, signal } from '@angular/core';
import { RouterLink, RouterLinkActive, RouterOutlet } from '@angular/router';
import { AdminApi } from '../../core/admin-api.service';
import { AuthService } from '../../core/auth.service';
import { Confirm } from '../../shared/ui/confirm';
import { AdminTokenService } from '../../core/token.service';
import { Icon, type IconName } from '../../shared/ui/icon';

// 顺序按使用频率：待处理的队列在前，配置类的在后
const ADMIN_TABS: { path: string; label: string; icon: IconName }[] = [
  { path: '/manage/review', label: '审核队列', icon: 'list-checks' },
  { path: '/manage/whitelist', label: '白名单', icon: 'shield-check' },
  { path: '/manage/blacklist', label: '黑名单', icon: 'ban' },
  { path: '/manage/submit', label: '网站提交', icon: 'globe' },
  { path: '/manage/labels', label: '标签反馈', icon: 'tag' },
  { path: '/manage/features', label: '功能开关', icon: 'sliders' },
  { path: '/manage/stats', label: '站点统计', icon: 'chart' },
  { path: '/manage/users', label: '账号管理', icon: 'shield' },
{ path: '/manage/ip', label: 'IP 管理', icon: 'globe' },
];

/**
 * 后台骨架。桌面（≥860px）：208px 常驻侧边栏（图标 + 待审数徽标 + 退出）。
 * 移动端：导航收进抽屉——顶栏「菜单」钮从左滑出，遮罩 / Esc / 选中后关闭。
 * 所有 /admin 接口都要 X-Admin-Token，没有令牌时只显示登录卡；
 * 令牌是响应式的——在登录卡里输入令牌后骨架直接切到 outlet，不用整页刷新。
 */
@Component({
  selector: 'vx-admin-shell',
  imports: [RouterLink, RouterLinkActive, RouterOutlet, Icon, Confirm],
  host: { '(document:keydown.escape)': 'menuOpen.set(false)' },
  template: `
    @if (token() || auth.isAdmin()) {
      <div class="page page-wide" style="padding-top: 20px; padding-bottom: 48px">
        <div class="row-between" style="margin-bottom: 18px">
          <div class="row" style="gap: 8px">
            <button
              type="button"
              class="btn btn-outline btn-sm admin-menu"
              aria-label="打开后台菜单"
              [attr.aria-expanded]="menuOpen()"
              (click)="menuOpen.set(true)"
            >
              <vx-icon name="menu" [size]="16" />
            </button>
            <h1 class="page-title">管理后台</h1>
          </div>
        </div>

        @if (menuOpen()) {
          <div class="drawer-scrim" aria-hidden="true" (click)="menuOpen.set(false)"></div>
        }

        <div class="admin-grid">
          <nav class="admin-side" [class.is-open]="menuOpen()" aria-label="后台导航">
            @for (t of tabs; track t.path) {
              <a
                [routerLink]="t.path"
                routerLinkActive="is-active"
                class="side-item"
                (click)="menuOpen.set(false)"
              >
                <vx-icon [name]="t.icon" [size]="16" />
                {{ t.label }}
                @if (t.path === '/manage/review' && pending() > 0) {
                  <span class="side-count">{{ pending() }}</span>
                }
              </a>
            }
            <button type="button" class="side-item side-exit" (click)="askLogout()">
              <vx-icon name="x" [size]="16" />
              退出
            </button>
          </nav>
          <div class="admin-body">
            <router-outlet />
          </div>
        </div>
      </div>
    } @else {
      <div class="page" style="padding-top: 72px; padding-bottom: 48px">
        <div class="card" style="max-width: 420px; margin: 0 auto">
          <h1 class="page-title">管理后台需要令牌</h1>
          <p class="page-desc">令牌在服务端启动日志中，用 -admin-token 可固定。</p>
          <form class="form-grid" style="margin-top: 16px" (submit)="login($event)">
            <label class="field">
              <span class="field-label">管理员令牌</span>
              <input
                class="input"
                type="password"
                autocomplete="off"
                [value]="input()"
                (input)="input.set(inputEl.value)"
                #inputEl
              />
            </label>
            <div>
              <button type="submit" class="btn btn-solid" [disabled]="!input().trim()">
                <vx-icon name="shield" [size]="16" />
                进入后台
              </button>
            </div>
          </form>
        </div>
      </div>
    }

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
export class AdminShell {
  private readonly tokenService = inject(AdminTokenService);
  protected readonly auth = inject(AuthService);
  protected readonly confirmOpen = signal(false);
  private readonly api = inject(AdminApi);

  protected readonly tabs = ADMIN_TABS;
  protected readonly token = this.tokenService.token;
  protected readonly input = signal('');
  protected readonly menuOpen = signal(false);
  /** 审核队列待处理数：侧栏徽标用，取不到就显示 0（不阻塞导航）。 */
  protected readonly pending = signal(0);

  constructor() {
    this.api
      .listReview('pending', '', 1, 1)
      .then((r) => this.pending.set(r.total))
      .catch(() => this.pending.set(0));
  }

  protected login(event: Event): void {
    event.preventDefault();
    const v = this.input().trim();
    if (v) this.tokenService.set(v);
  }

  protected askLogout(): void {
    this.confirmOpen.set(true);
  }

  protected logout(): void {
    this.confirmOpen.set(false);
    this.menuOpen.set(false);
    // 两种入口都要清：只清 token 的话，账号会话还在，刷新又回到后台
    this.tokenService.clear();
    void this.auth.logout();
  }
}
