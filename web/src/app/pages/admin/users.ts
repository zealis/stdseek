import { Component, computed, inject, signal } from '@angular/core';
import { AdminApi } from '../../core/admin-api.service';
import { AuthService, type AuthUser, authErrMsg } from '../../core/auth.service';
import { Pager } from '../../shared/ui/pager';

const PAGE_SIZE = 20;

/**
 * 账号管理（后台）。
 *
 * 只做两件事：改角色、启停账号。删账号刻意不做——历史数据（谁提交了哪个站、
 * 谁改过白名单）挂在 uid 上，删了就会出现一堆指向空气的外键。
 *
 * 「最后一个管理员」和「不能禁用自己」的护栏在后端，这里只负责把错误文案显示出来。
 */
@Component({
  selector: 'vx-users-page',
  imports: [Pager],
  template: `
    <div class="row-between" style="margin-bottom: 14px">
      <div>
        <div class="page-title">账号</div>
        <p class="faint small" style="margin-top: 2px">共 {{ total() }} 个账号</p>
      </div>
      @if (error()) {
        <span class="small" style="color: var(--danger)">{{ error() }}</span>
      }
    </div>

    @if (loading()) {
      <div class="loading"><span class="spinner spinner-lg"></span></div>
    } @else {
      <div class="table-wrap">
        <table class="table">
          <thead>
            <tr>
              <th style="width: 34%">邮箱</th>
              <th style="width: 12%">角色</th>
              <th style="width: 12%">状态</th>
              <th style="width: 16%">注册时间</th>
              <th style="width: 16%">最后登录</th>
              <th class="is-right" style="width: 10%">操作</th>
            </tr>
          </thead>
          <tbody>
            @for (u of rows(); track u.id) {
              <tr>
                <td data-label="邮箱">
                  <span class="cell-strong mono">{{ u.email }}</span>
                  @if (u.id === me()?.id) {
                    <span class="chip" style="margin-left: 6px">你</span>
                  }
                </td>
                <td data-label="角色">
                  <span class="chip" [class.chip-accent]="u.role === 'admin'">
                    {{ u.role === 'admin' ? '管理员' : '普通用户' }}
                  </span>
                </td>
                <td data-label="状态">
                  <span class="chip" [class.chip-accent]="u.status === 1">
                    {{ u.status === 1 ? '正常' : '已禁用' }}
                  </span>
                </td>
                <td data-label="注册时间">
                  <span class="small muted">{{ date(u.created_at) }}</span>
                </td>
                <td data-label="最后登录">
                  <span class="small muted">{{ u.last_login ? date(u.last_login) : '—' }}</span>
                </td>
                <td class="is-actions" data-nolabel>
                  <button type="button" class="btn btn-link btn-sm" (click)="toggleRole(u)">
                    {{ u.role === 'admin' ? '降为普通' : '设为管理员' }}
                  </button>
                  <button type="button" class="btn btn-link btn-sm" (click)="toggleStatus(u)">
                    {{ u.status === 1 ? '禁用' : '启用' }}
                  </button>
                </td>
              </tr>
            } @empty {
              <tr>
                <td colspan="6"><div class="empty">还没有账号</div></td>
              </tr>
            }
          </tbody>
        </table>
      </div>
    }

    <vx-pager [page]="page()" [pages]="pages()" [total]="total()" (pageChange)="goPage($event)" />
  `,
})
export class UsersPage {
  private readonly api = inject(AdminApi);
  private readonly auth = inject(AuthService);

  protected readonly rows = signal<AuthUser[]>([]);
  protected readonly total = signal(0);
  protected readonly page = signal(1);
  protected readonly loading = signal(true);
  protected readonly error = signal('');
  protected readonly me = this.auth.user;
  protected readonly pages = computed(() => Math.max(1, Math.ceil(this.total() / PAGE_SIZE)));

  constructor() {
    void this.load();
  }

  private async load(): Promise<void> {
    this.loading.set(true);
    this.error.set('');
    try {
      const r = await this.api.listUsers(this.page(), PAGE_SIZE);
      this.rows.set(r.items ?? []);
      this.total.set(r.total ?? 0);
    } catch (e) {
      this.error.set(authErrMsg(e));
      this.rows.set([]);
    } finally {
      this.loading.set(false);
    }
  }

  protected goPage(p: number): void {
    this.page.set(p);
    void this.load();
  }

  protected async toggleRole(u: AuthUser): Promise<void> {
    const next = u.role === 'admin' ? 'member' : 'admin';
    try {
      await this.api.setUserRole(u.id, next);
      await this.load();
    } catch (e) {
      this.error.set(authErrMsg(e));
    }
  }

  protected async toggleStatus(u: AuthUser): Promise<void> {
    try {
      await this.api.setUserStatus(u.id, u.status === 1 ? 0 : 1);
      // 如果禁用的是自己，后端会拒绝；其它情况刷新列表即可
      await this.load();
      await this.auth.refresh();
    } catch (e) {
      this.error.set(authErrMsg(e));
    }
  }

  protected date(ts: number): string {
    if (!ts) return '—';
    const d = new Date(ts * 1000);
    const p = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
  }
}
