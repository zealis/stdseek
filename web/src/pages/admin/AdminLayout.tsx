import { useState } from 'react';
import {
  Alert, Box, Button, Container, Paper, Stack, Tab, Tabs, TextField, Typography,
} from '@mui/material';
import { Outlet, useLocation, useNavigate } from 'react-router-dom';
import { getAdminToken, setAdminToken } from '../../api';

const TABS = [
  { path: '/manage/submit', label: '网站提交' },
  { path: '/manage/review', label: '审核队列' },
  { path: '/manage/whitelist', label: '白名单' },
  { path: '/manage/blacklist', label: '黑名单' },
];

export default function AdminLayout() {
  const nav = useNavigate();
  const loc = useLocation();
  const [token, setToken] = useState(getAdminToken());
  const [input, setInput] = useState('');
  const current = TABS.find((t) => loc.pathname.startsWith(t.path))?.path ?? '/manage/whitelist';

  if (!token) {
    return (
      <Container sx={{ pt: 6 }}>
        <Paper variant="outlined" sx={{ p: 4, maxWidth: 480, mx: 'auto', bgcolor: 'background.paper' }}>
          <Typography variant="h6" sx={{ mb: 1 }}>管理后台需要令牌</Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
            令牌在服务端启动日志中（`-admin-token` 可固定）。
          </Typography>
          <Stack spacing={1.5}>
            <TextField
              autoFocus
              label="管理员令牌"
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter' && input.trim()) { setAdminToken(input.trim()); setToken(input.trim()); } }}
            />
            <Button
              variant="contained"
              disabled={!input.trim()}
              onClick={() => { setAdminToken(input.trim()); setToken(input.trim()); }}
              sx={{ alignSelf: 'flex-start' }}
            >
              进入后台
            </Button>
          </Stack>
        </Paper>
      </Container>
    );
  }

  return (
    <Container sx={{ pt: 3, pb: 6 }}>
      <Stack direction="row" alignItems="center" sx={{ mb: 0.5 }}>
        <Typography variant="h5" sx={{ flexGrow: 1 }}>管理后台</Typography>
        <Button size="small" onClick={() => { setAdminToken(''); setToken(''); }}>退出</Button>
      </Stack>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        索引质量靠策展：只有白名单内的站点会被抓取。
      </Typography>
      <Box sx={{ borderBottom: 1, borderColor: 'divider', mb: 3 }}>
        <Tabs value={current} onChange={(_, v) => nav(v)} variant="scrollable">
          {TABS.map((t) => <Tab key={t.path} value={t.path} label={t.label} />)}
        </Tabs>
      </Box>
      <Outlet />
      <Alert severity="info" variant="outlined" sx={{ mt: 3 }}>
        令牌仅保存在本机 localStorage；修改令牌或点击「退出」会清空。
      </Alert>
    </Container>
  );
}
