import { AppBar, Box, Button, Container, Toolbar, Typography } from '@mui/material';
import { Link as RouterLink, Route, Routes, useLocation } from 'react-router-dom';
import Home from './pages/Home';
import Results from './pages/Results';
import Settings from './pages/Settings';
import Contribute from './pages/Contribute';
import AdminLayout from './pages/admin/AdminLayout';
import Whitelist from './pages/admin/Whitelist';
import Blacklist from './pages/admin/Blacklist';
import Review from './pages/admin/Review';
import Submit from './pages/admin/Submit';
import Labels from './pages/admin/Labels';
import Features from './pages/admin/Features';

const NAV = [
  { to: '/', label: '搜索', short: '搜索' },
  { to: '/contribute', label: '提交站点', short: '提交' },
  { to: '/settings', label: '设置', short: '设置' },
  { to: '/manage/whitelist', label: '管理后台', short: '后台' },
];

export default function App() {
  const loc = useLocation();
  return (
    <Box sx={{ minHeight: '100vh', bgcolor: 'background.default' }}>
      <AppBar position="sticky" color="transparent" elevation={0}
        sx={{ bgcolor: 'background.default', borderBottom: '1px solid #E4E4DA' }}>
        <Container>
          <Toolbar disableGutters sx={{ gap: { xs: 1, sm: 2, md: 3 }, minHeight: { xs: 56, sm: 64 } }}>
            <Typography
              component={RouterLink}
              to="/"
              variant="h6"
              sx={{ color: 'primary.main', textDecoration: 'none', fontWeight: 700, whiteSpace: 'nowrap' }}
            >
              voidxo<span style={{ color: '#8B7355', marginLeft: 6, fontSize: '0.8em' }}>维空</span>
            </Typography>
            <Box sx={{ display: 'flex', gap: 0.25, flexGrow: 1, minWidth: 0 }}>
              {NAV.map((n) => {
                const active = n.to === '/' ? loc.pathname === '/' : loc.pathname.startsWith(n.to.split('/').slice(0, 2).join('/'));
                return (
                  <Button
                    key={n.to}
                    component={RouterLink}
                    to={n.to}
                    size="small"
                    sx={{
                      color: active ? 'primary.main' : 'text.secondary',
                      bgcolor: active ? 'rgba(47,79,79,0.08)' : 'transparent',
                      minWidth: 0,
                      px: { xs: 0.75, sm: 1.25 },
                      whiteSpace: 'nowrap',
                    }}
                  >
                    <Box component="span" sx={{ display: { xs: 'inline', sm: 'none' } }}>{n.short}</Box>
                    <Box component="span" sx={{ display: { xs: 'none', sm: 'inline' } }}>{n.label}</Box>
                  </Button>
                );
              })}
            </Box>
            <Typography variant="caption" color="text.secondary"
              sx={{ display: { xs: 'none', md: 'block' }, whiteSpace: 'nowrap' }}>
              仅索引白名单站点
            </Typography>
          </Toolbar>
        </Container>
      </AppBar>

      <Routes>
        <Route path="/" element={<Home />} />
        <Route path="/search" element={<Results />} />
        <Route path="/contribute" element={<Contribute />} />
        <Route path="/settings" element={<Settings />} />
        <Route path="/manage" element={<AdminLayout />}>
          <Route index element={<Whitelist />} />
          <Route path="whitelist" element={<Whitelist />} />
          <Route path="blacklist" element={<Blacklist />} />
          <Route path="review" element={<Review />} />
          <Route path="submit" element={<Submit />} />
          <Route path="labels" element={<Labels />} />
          <Route path="features" element={<Features />} />
        </Route>
      </Routes>

      <Box component="footer" sx={{ py: 4, textAlign: 'center' }}>
        <Typography variant="caption" color="text.secondary">
          voidxo 维空 · 可验证证据的开发者搜索 · 无广告、不追踪
        </Typography>
      </Box>
    </Box>
  );
}
