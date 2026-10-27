import { Component, computed, inject, signal } from '@angular/core';
import { AdminApi } from '../../core/admin-api.service';
import { AuthService, type AuthUser, authErrMsg } from '../../core/auth.service';
import { Pager } from '../../shared/ui/pager';
import { AdminToolbar, type BatchAction } from './admin-toolbar';
import { Modal } from '../../shared/ui/modal';
import { DeviceList, type DeviceRow } from '../../shared/ui/device-list';
import { type GeoInfo } from '../../core/geo-types';

const PAGE_SIZE = 20;

/**
 * 账号管理（后台）。
 *
 * 三件事：搜索、改角色、启停。删账号刻意不做——历史数据挂在 uid 上，
 * 删了会留下一堆指向空气的外键。
 *
 * 批量操作栏只在有选中项时出现：没有选中时它是纯噪音，还会把「搜索」挤出视线。
 * 护栏（最后一位管理员不能降级、不能禁用自己）在后端，这里只负责展示错误。
 */
@Component({
  selector: 'vx-users-page',
  imports: [Pager, AdminToolbar, Modal, DeviceList],
  template: `
    <vx-admin-toolbar
      title="账号管理"
      placeholder="搜索邮箱"
      [count]="selected().length"
      [actions]="batchActions"
      (kwSearch)="search($event)"
    >
      <span class="faint small">共 {{ total() }} 个</span>
    </vx-admin-toolbar>

    @if (error()) {
      <div class="notice notice-error" style="margin-bottom: 12px">{{ error() }}</div>
    }

    @if (loading()) {
      <div class="loading"><span class="spinner spinner-lg"></span></div>
    } @else {
      <div class="table-wrap">
        <table class="table">
          <thead>
            <tr>
              <th class="table-check">
                <input type="checkbox" aria-label="全选" [checked]="allChecked()" (change)="toggleAll()" />
              </th>
              <th style="width: 32%">邮箱</th>
              <th style="width: 12%">角色</th>
              <th style="width: 12%">状态</th>
              <th style="width: 16%">注册时间</th>
              <th style="width: 16%">最后登录</th>
              <th class="is-right" style="width: 10%">操作</th>
            </tr>
          </thead>
          <tbody>
            @for (u of rows(); track u.id) {
              <tr [class.is-selected]="selected().includes(u.id)">
                <td class="table-check" data-nolabel>
                  <input
                    type="checkbox"
                    [attr.aria-label]="'选择 ' + u.email"
                    [checked]="selected().includes(u.id)"
                    (change)="toggleOne(u.id)"
                  />
                </td>
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
                <td data-label="注册时间"><span class="small muted">{{ date(u.created_at) }}</span></td>
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
                  <button type="button" class="btn btn-link btn-sm" (click)="openDevices(u)">
                    设备
                  </button>
                </td>
              </tr>
            } @empty {
              <tr>
                <td colspan="7"><div class="empty">{{ kw() ? '没有匹配的账号' : '还没有账号' }}</div></td>
              </tr>
            }
          </tbody>
        </table>
      </div>
    }

    <vx-pager [page]="page()" [pages]="pages()" [total]="total()" (pageChange)="goPage($event)" />

    @if (deviceUser()) {
      <vx-modal [title]="deviceUser()!.email" [width]="560" (closed)="deviceUser.set(null)">
        <vx-device-list [items]="devices()" />

        @if (deviceIps().length > 1) {
          <p class="faint small" style="margin-top: 10px">
            该账号使用过 {{ deviceIps().length }} 个不同 IP
          </p>
        }

        @if (riskyIPs().length > 0) {
          <div class="section-title" style="margin-top: 14px">风险标记</div>
          @for (r of riskyIPs(); track r.ip) {
            <div class="row wrap" style="gap: 6px; margin-top: 6px; align-items: center">
              <span class="mono small">{{ r.ip }}</span>
              <span class="chip" [class.chip-danger]="(r.geo.score ?? 100) < 50">信誉分 {{ r.geo.score || '—' }}</span>
              @for (tag of riskTagsOf(r.geo.risk); track tag) {
                <span class="chip chip-danger">{{ tag }}</span>
              }
            </div>
          }
        }

        @if (sameIPAccounts().length > 0) {
          <div class="section-title" style="margin-top: 14px">同 IP 的其他账号</div>
          @for (a of sameIPAccounts(); track a.id) {
            <div class="row" style="gap: 6px; margin-top: 6px; align-items: center">
              <span class="mono small">{{ a.email }}</span>
              <span class="chip">{{ a.role === 'admin' ? '管理员' : '普通用户' }}</span>
              @if (a.status !== 1) { <span class="chip chip-danger">已禁用</span> }
            </div>
          }
        }
      </vx-modal>
    }
  `,
})
export class UsersPage {
  private readonly api = inject(AdminApi);
  private readonly auth = inject(AuthService);

  protected readonly rows = signal<AuthUser[]>([]);
  protected readonly total = signal(0);
  protected readonly page = signal(1);
  protected readonly kw = signal('');
  protected readonly selected = signal<number[]>([]);
  protected readonly deviceUser = signal<AuthUser | null>(null);
  protected readonly devices = signal<DeviceRow[]>([]);
  protected readonly deviceGeo = signal<Record<string, GeoInfo>>({});
  protected readonly sameIPAccounts = signal<AuthUser[]>([]);
  protected readonly loading = signal(true);
  protected readonly error = signal('');
  protected readonly me = this.auth.user;
  protected readonly pages = computed(() => Math.max(1, Math.ceil(this.total() / PAGE_SIZE)));
  /** 批量动作交给统一工具栏渲染：它在 count>0 时才出现，样式与其他页一致。 */
  protected readonly batchActions: BatchAction[] = [
    { label: '设为管理员', run: () => this.batch('role', 'admin') },
    { label: '设为普通用户', run: () => this.batch('role', 'member') },
    { label: '启用', run: () => this.batch('status', '1') },
    { label: '禁用', danger: true, run: () => this.batch('status', '0') },
  ];

  protected readonly allChecked = computed(
    () => this.rows().length > 0 && this.rows().every((u) => this.selected().includes(u.id)),
  );

  constructor() {
    void this.load();
  }

  private async load(): Promise<void> {
    this.loading.set(true);
    this.error.set('');
    try {
      const r = await this.api.listUsers(this.page(), PAGE_SIZE, this.kw().trim());
      this.rows.set(r.items ?? []);
      this.total.set(r.total ?? 0);
      // 换页/换搜索时清空选择：残留的 id 会让批量操作打到看不见的行上
      this.selected.set([]);
    } catch (e) {
      this.error.set(authErrMsg(e));
      this.rows.set([]);
    } finally {
      this.loading.set(false);
    }
  }

  protected search(kw: string): void {
    this.kw.set(kw);
    this.page.set(1);
    void this.load();
  }

  protected goPage(p: number): void {
    this.page.set(p);
    void this.load();
  }

  protected toggleAll(): void {
    this.selected.set(this.allChecked() ? [] : this.rows().map((u) => u.id));
  }

  protected toggleOne(id: number): void {
    this.selected.update((list) =>
      list.includes(id) ? list.filter((x) => x !== id) : [...list, id],
    );
  }

  protected async toggleRole(u: AuthUser): Promise<void> {
    try {
      await this.api.setUserRole(u.id, u.role === 'admin' ? 'member' : 'admin');
      await this.load();
    } catch (e) {
      this.error.set(authErrMsg(e));
    }
  }

  protected async toggleStatus(u: AuthUser): Promise<void> {
    try {
      await this.api.setUserStatus(u.id, u.status === 1 ? 0 : 1);
      await this.load();
      await this.auth.refresh();
    } catch (e) {
      this.error.set(authErrMsg(e));
    }
  }

  protected async batch(action: 'role' | 'status', value: string): Promise<void> {
    const ids = this.selected();
    if (ids.length === 0) return;
    try {
      await this.api.batchUsers(ids, action, value);
      await this.load();
      await this.auth.refresh();
    } catch (e) {
      this.error.set(authErrMsg(e));
    }
  }

  /** 打开某账号的登录设备 + 同 IP 的其他账号（用户画像）。 */
  protected async openDevices(u: AuthUser): Promise<void> {
    this.deviceUser.set(u);
    this.devices.set([]);
    this.deviceGeo.set({});
    this.sameIPAccounts.set([]);
    try {
      const r = await this.api.userSessions(u.id);
      this.devices.set(r.items ?? []);
      // 每条记录的 geo 后端已从缓存补过；把同 IP 的其他账号也拉出来
      const ips = this.deviceIps();
      if (ips.length > 0) {
        const accs = await this.api.accountsByIP(ips[0]);
        this.sameIPAccounts.set((accs.items ?? []).filter((a) => a.id !== u.id));
      }
    } catch {
      /* 读不到就空着 */
    }
  }

  /** 该账号用过的不同 IP（去重），用于快速判断是否多个地址登录。 */
  /** 有信誉数据的 IP（去重）。信誉分低或有风险标记的会以醒目方式显示。 */
  protected riskyIPs(): { ip: string; geo: GeoInfo }[] {
    const map = new Map<string, { ip: string; geo: GeoInfo }>();
    for (const d of this.devices()) {
      if (d.geo && (d.geo.score > 0 || d.geo.risk !== '[]')) {
        map.set(d.ip, { ip: d.ip, geo: d.geo });
      }
    }
    return Array.from(map.values());
  }

  protected riskTagsOf(risk: string): string[] {
    try {
      const arr = JSON.parse(risk || '[]');
      return Array.isArray(arr) ? arr : [];
    } catch {
      return [];
    }
  }

  protected deviceIps(): string[] {
    return Array.from(new Set(this.devices().map((d) => d.ip).filter(Boolean)));
  }

  protected geoOf(d: DeviceRow): GeoInfo | null {
    return d.geo ?? null;
  }

  protected date(ts: number): string {
    if (!ts) return '—';
    const d = new Date(ts * 1000);
    const p = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
  }
}
