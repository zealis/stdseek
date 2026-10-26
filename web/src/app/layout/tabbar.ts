import { Component } from '@angular/core';
import { RouterLink, RouterLinkActive } from '@angular/router';
import { Icon, type IconName } from '../shared/ui/icon';

/** 移动端底部导航（<640px 显示，DESIGN.md §7）：四项，图标 20px + 文字。 */
const ITEMS: { href: string; label: string; icon: IconName; exact: boolean }[] = [
  { href: '/', label: '搜索', icon: 'search', exact: true },
  { href: '/contribute', label: '提交', icon: 'send', exact: false },
  { href: '/settings', label: '设置', icon: 'sliders', exact: false },
  { href: '/manage/whitelist', label: '后台', icon: 'shield', exact: false },
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
