import { Component, input } from '@angular/core';
import { parseUA } from '../../core/ua';

export type DeviceRow = {
  id: number;
  ip: string;
  ua: string;
  kind: string;
  created_at: number;
  current: boolean;
};

/**
 * 最近登录的设备列表。前台「设置 → 账号」与后台「账号管理 → 详情」共用。
 *
 * 只展示「系统 + 浏览器 + IP + 时间」四件事：这已经足够让用户判断
 * 「这条记录是不是我」，再多的 UA 细节只会变成噪音。
 */
@Component({
  selector: 'vx-device-list',
  template: `
    <div class="dev">
      @for (d of items(); track d.id) {
        <div class="dev-row">
          <span class="dev-ico" aria-hidden="true">
            @if (ua(d).kind === 'desktop') {
              <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor"
                stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">
                <rect x="2.5" y="4" width="19" height="12.5" rx="1.6" />
                <path d="M9 20.5h6M12 16.5v4" />
              </svg>
            } @else {
              <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor"
                stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">
                <rect x="7" y="2.5" width="10" height="19" rx="2" />
                <path d="M11 18.5h2" />
              </svg>
            }
          </span>

          <div class="dev-main">
            <div class="dev-name">{{ ua(d).device }}</div>
            <div class="dev-meta">{{ d.ip }} · {{ ua(d).browser }}</div>
          </div>

          <div class="dev-side">
            <span class="dev-time">{{ time(d.created_at) }}</span>
            @if (d.current) {
              <span class="dev-on">当前在线</span>
            }
          </div>
        </div>
      } @empty {
        <p class="faint small" style="margin: 0">还没有登录记录</p>
      }
    </div>
  `,
  styles: `
    .dev-row {
      display: flex;
      align-items: center;
      gap: 12px;
      padding: 11px 0;
      border-bottom: 1px dashed var(--line);
    }

    .dev-row:last-child {
      border-bottom: 0;
    }

    .dev-ico {
      display: flex;
      justify-content: center;
      width: 24px;
      flex: 0 0 24px;
      color: var(--text-3);
    }

    .dev-main {
      flex: 1;
      min-width: 0;
    }

    .dev-name {
      font-size: var(--fs-sm);
      font-weight: 600;
      color: var(--text-1);
    }

    .dev-meta {
      font-size: var(--fs-xs);
      color: var(--text-3);
    }

    .dev-side {
      flex: 0 0 auto;
      text-align: right;
    }

    .dev-time {
      display: block;
      font-size: var(--fs-xs);
      color: var(--text-3);
    }

    .dev-on {
      display: block;
      font-size: var(--fs-xs);
      font-weight: 500;
      color: var(--success);
    }
  `,
})
export class DeviceList {
  readonly items = input<DeviceRow[]>([]);

  protected ua(d: DeviceRow) {
    return parseUA(d.ua);
  }

  protected time(ts: number): string {
    if (!ts) return '—';
    const d = new Date(ts * 1000);
    const p = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
  }
}
