import { type Routes } from '@angular/router';
import { adminGuard } from './core/auth.guard';
import { HomePage } from './pages/home';

export const routes: Routes = [
  // 首页是静态导入（不进懒加载）：它是绝大多数人的落地页，而懒加载要多一次往返——
  // 骨架已经把 hero 画出来了，用户这时敲字却没反应，正是「首页感觉慢」的那一段。
  // 代价是 main 里多了 2KB(gzip)，换来打开即可输入。
  { path: '', component: HomePage, title: 'title.search' },
  {
    path: 'search',
    loadComponent: () => import('./pages/search').then((m) => m.SearchPage),
    title: 'title.search',
  },
  {
    path: 'contribute',
    loadComponent: () => import('./pages/contribute').then((m) => m.ContributePage),
    title: 'title.contribute',
  },
  {
    path: 'settings',
    loadComponent: () => import('./pages/settings').then((m) => m.SettingsPage),
    title: 'title.settings',
  },
  // 用 api-docs 而不是 /api：/api/* 是后端路由，SPA fallback 抢不到。
  {
    path: 'api-docs',
    loadComponent: () => import('./pages/api-docs').then((m) => m.ApiDocsPage),
    title: 'title.api',
  },
  {
    path: 'usage',
    loadComponent: () => import('./pages/usage').then((m) => m.UsagePage),
    title: 'title.usage',
  },
  {
    path: 'help',
    loadComponent: () => import('./pages/help').then((m) => m.HelpPage),
    title: 'title.help',
  },
  {
    path: 'privacy',
    loadComponent: () => import('./pages/privacy').then((m) => m.PrivacyPage),
    title: 'title.privacy',
  },
  // 账号：这四个页面公开可访问
  {
    path: 'login',
    loadComponent: () => import('./pages/auth/login').then((m) => m.LoginPage),
    title: 'title.login',
  },
  {
    path: 'register',
    loadComponent: () => import('./pages/auth/register').then((m) => m.RegisterPage),
    title: 'title.register',
  },
  {
    path: 'forgot',
    loadComponent: () => import('./pages/auth/forgot').then((m) => m.ForgotPage),
    title: 'title.forgot',
  },
  {
    path: 'reset',
    loadComponent: () => import('./pages/auth/reset').then((m) => m.ResetPage),
    title: 'title.reset',
  },
  {
    path: 'manage',
    canActivate: [adminGuard],
    loadChildren: () => import('./pages/admin/routes').then((m) => m.ADMIN_ROUTES),
  },
  { path: '**', redirectTo: '' },
];
