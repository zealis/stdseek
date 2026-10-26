import { type Routes } from '@angular/router';

export const routes: Routes = [
  { path: '', loadComponent: () => import('./pages/home').then((m) => m.HomePage), title: '搜索' },
  { path: 'search', loadComponent: () => import('./pages/search').then((m) => m.SearchPage), title: '搜索' },
  { path: 'contribute', loadComponent: () => import('./pages/contribute').then((m) => m.ContributePage), title: '提交站点' },
  { path: 'settings', loadComponent: () => import('./pages/settings').then((m) => m.SettingsPage), title: '设置' },
  // 用 api-docs 而不是 /api：/api/* 是后端路由，SPA fallback 抢不到。
  { path: 'api-docs', loadComponent: () => import('./pages/api-docs').then((m) => m.ApiDocsPage), title: 'API' },
  { path: 'usage', loadComponent: () => import('./pages/usage').then((m) => m.UsagePage), title: '每日用量' },
  { path: 'help', loadComponent: () => import('./pages/help').then((m) => m.HelpPage), title: '帮助' },
  {
    path: 'manage',
    loadChildren: () => import('./pages/admin/routes').then((m) => m.ADMIN_ROUTES),
  },
  { path: '**', redirectTo: '' },
];
