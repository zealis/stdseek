import { Box, Container, Tab, Tabs, Typography } from '@mui/material';
import { Outlet, useLocation, useNavigate } from 'react-router-dom';

const TABS = [
  { path: '/manage/submit', label: '网站提交' },
  { path: '/manage/review', label: '审核队列' },
  { path: '/manage/whitelist', label: '白名单' },
  { path: '/manage/blacklist', label: '黑名单' },
];

export default function AdminLayout() {
  const nav = useNavigate();
  const loc = useLocation();
  const current = TABS.find((t) => loc.pathname.startsWith(t.path))?.path ?? '/manage/whitelist';

  return (
    <Container sx={{ pt: 3, pb: 6 }}>
      <Typography variant="h5" sx={{ mb: 0.5 }}>管理后台</Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        索引质量靠策展：只有白名单内的站点会被抓取。
      </Typography>
      <Box sx={{ borderBottom: 1, borderColor: 'divider', mb: 3 }}>
        <Tabs value={current} onChange={(_, v) => nav(v)} variant="scrollable">
          {TABS.map((t) => <Tab key={t.path} value={t.path} label={t.label} />)}
        </Tabs>
      </Box>
      <Outlet />
    </Container>
  );
}
