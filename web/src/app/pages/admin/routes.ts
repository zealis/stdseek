import { type Routes } from '@angular/router';

// /manage 直接落在白名单：它是后台最常打开的一页
export const ADMIN_ROUTES: Routes = [
  {
    path: '',
    loadComponent: () => import('./admin-shell').then((m) => m.AdminShell),
    children: [
      { path: '', pathMatch: 'full', redirectTo: 'whitelist' },
      { path: 'review', title: '审核队列', loadComponent: () => import('./review').then((m) => m.ReviewPage) },
      { path: 'whitelist', title: '白名单', loadComponent: () => import('./whitelist').then((m) => m.WhitelistPage) },
      { path: 'blacklist', title: '黑名单', loadComponent: () => import('./blacklist').then((m) => m.BlacklistPage) },
      { path: 'submit', title: '网站提交', loadComponent: () => import('./submit').then((m) => m.SubmitPage) },
      { path: 'labels', title: '标签反馈', loadComponent: () => import('./labels').then((m) => m.LabelsPage) },
      { path: 'features', title: '功能开关', loadComponent: () => import('./features').then((m) => m.FeaturesPage) },
      { path: 'usage', title: '每日用量', loadComponent: () => import('./usage').then((m) => m.UsagePage) },
    ],
  },
];
