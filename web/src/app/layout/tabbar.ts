import { Component } from '@angular/core';
import { RouterLink, RouterLinkActive } from '@angular/router';
import { Icon, type IconName } from '../shared/ui/icon';

/**
 * 移动端底部导航（<640px 显示，DESIGN.md §7）：三项，图标 20px + 文字。
 *
 * 不放「后台」：这条导航每个访客都看得到，而后台是管理员的工具，摆在这里
 * 既挤掉了访客最需要的三个入口，也把管理入口暴露给所有人。管理员在手机上
 * 直接访问 /manage（或从设置页进）——顶栏导航在 <640 时整条收起，是有意的。
 */
const ITEMS: { href: string; label: string; icon: IconName; exact: boolean }[] = [
  { href: '/', label: '搜索', icon: 'search', exact: true },
  { href: '/contribute', label: '提交', icon: 'send', exact: false },
  { href: '/settings', label: '设置', icon: 'sliders', exact: false },
];

@Component({
  selector: 'vx-tabbar',
  imports: [RouterLink, RouterLinkActive, Icon],
  template: `
    <nav class="tabbar" aria-label="底部导航">
      @for (it of items; track it.href) {
        <a
          [routerLink]="it.href"
          routerLinkActive="is-active"
          [routerLinkActiveOptions]="{ exact: it.exact }"
          class="tabbar-link"
        >
          <vx-icon [name]="it.icon" [size]="20" />
          <span>{{ it.label }}</span>
        </a>
      }
    </nav>
  `,
})
export class Tabbar {
  protected readonly items = ITEMS;
}
