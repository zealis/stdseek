import { type Routes } from '@angular/router';

export const routes: Routes = [
  { path: '', loadComponent: () => import('./pages/home').then((m) => m.HomePage), title: '搜索' },
  { path: 'search', loadComponent: () => import('./pages/search').then((m) => m.SearchPage), title: '搜索' },
  { path: 'contribute', loadComponent: () => import('./pages/contribute').then((m) => m.ContributePage), title: '提交站点' },
  { path: 'settings', loadComponent: () => import('./pages/settings').then((m) => m.SettingsPage), title: '设置' },
  {
    path: 'manage',
    loadChildren: () => import('./pages/admin/routes').then((m) => m.ADMIN_ROUTES),
  },
  { path: '**', redirectTo: '' },
];
