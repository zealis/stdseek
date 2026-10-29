import { Component, input, output } from '@angular/core';
import { parseUA } from '../../core/ua';

export type DeviceRow = {
  id: number;
  ip: string;
  ua: string;
  kind: string;
  created_at: number;
  current: boolean;
  geo?: import('../../core/geo-types').GeoInfo | null;
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
      @for (d of items(); track d.id; let first = $first) {
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
            <div class="dev-meta">
              {{ ipText(d.ip) }}{{ geoText(d) }} · {{ ua(d).browser }}
            </div>
          </div>

          <div class="dev-side">
            <span class="dev-time">{{ time(d.created_at) }}</span>
            <div class="dev-side-row">
              @if (d.current && first) {
                <span class="dev-on">当前在线</span>
              } @else {
                <button
                  type="button"
                  class="dev-why"
                  aria-label="查看这条登录的详细信息"
                  (click)="detail.emit(d)"
                >?</button>
              }
            </div>
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
      font-size: var(--fs-xs);
      font-weight: 500;
      color: var(--success);
    }

    .dev-side-row {
      display: flex;
      align-items: center;
      justify-content: flex-end;
      gap: 8px;
    }

    .dev-why {
      width: 22px;
      height: 22px;
      border: 1px solid var(--line-strong);
      border-radius: 50%;
      background: none;
      color: var(--text-3);
      font-size: var(--fs-xs);
      cursor: pointer;
      transition: color var(--dur) var(--ease-out), border-color var(--dur) var(--ease-out);
    }

    .dev-why:hover {
      color: var(--text-1);
      border-color: var(--text-3);
    }
  `,
})
export class DeviceList {
  readonly items = input<DeviceRow[]>([]);
  /** 点问号查看单条详情（参考 owlsgo 的「这是您吗？」）。 */
  readonly detail = output<DeviceRow>();

  /** 回环地址显示为「本机」：::1 和 127.0.0.1 对普通用户没有任何含义。 */
  protected ipText(ip: string): string {
    if (ip === '::1' || ip === '127.0.0.1') return '本机';
    return ip;
  }

  /** 归属地文案：地区 · 运营商（运营商单独一段，不跟地区挤在一起）。 */
  protected geoText(d: DeviceRow): string {
    const parts: string[] = [];
    if (d.geo?.label) parts.push(d.geo.label);
    if (d.geo?.isp) parts.push(d.geo.isp);
    return parts.length ? ' · ' + parts.join(' · ') : '';
  }

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
