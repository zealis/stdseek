import { Component } from '@angular/core';
import { RouterOutlet } from '@angular/router';
import { Header } from './layout/header';
import { Tabbar } from './layout/tabbar';

@Component({
  selector: 'app-root',
  imports: [RouterOutlet, Header, Tabbar],
  template: `
    <vx-header />
    <main>
      <router-outlet />
    </main>
    <footer class="footer">
      <span>voidxo 维空</span>
      <span>无广告 不追踪</span>
    </footer>
    <vx-tabbar />
  `,
})
export class App {}
