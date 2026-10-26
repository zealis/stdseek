import { useEffect, useState } from 'react';
import { Alert, Box, Button, Container, Paper, Stack, TextField, Typography } from '@mui/material';
import { Link as RouterLink, useNavigate } from 'react-router-dom';
import { status, type Status } from '../api';

export default function Home() {
  const [q, setQ] = useState('');
  const [st, setSt] = useState<Status | null>(null);
  const nav = useNavigate();

  useEffect(() => {
    void status().then(setSt);
  }, []);

  const go = () => {
    const v = q.trim();
    if (!v) return;
    nav(`/search?q=${encodeURIComponent(v)}`);
  };

  // 空索引是首次部署最常见的情况：exe 起来了、首页也能开，但搜什么都是 0 条。
  // 不给引导的话，用户第一反应是"这东西坏了"，而不是"我还没往里加站点"。
  const empty = st !== null && st.documents === 0;

  return (
    <Container sx={{ pt: { xs: 8, md: 14 } }}>
      <Stack alignItems="center" spacing={2}>
        <Typography variant="h3" sx={{ color: 'primary.main', letterSpacing: '-0.03em' }}>
          voidxo <span style={{ color: '#8B7355' }}>维空</span>
        </Typography>
        <Typography color="text.secondary" sx={{ textAlign: 'center', maxWidth: 560 }}>
          只搜白名单内的高质量技术站点。每条结果都告诉你「为什么排在这里」，不追踪、无广告。
        </Typography>

        <Paper
          component="form"
          onSubmit={(e) => { e.preventDefault(); go(); }}
          variant="outlined"
          sx={{ p: 1, mt: 3, width: '100%', maxWidth: 640, display: 'flex', gap: 1, bgcolor: 'background.paper' }}
        >
          <TextField
            autoFocus
            fullWidth
            placeholder="报错文本、API 名、版本号…"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            variant="standard"
            InputProps={{ disableUnderline: true, sx: { px: 1.5 } }}
          />
          <Button type="submit" variant="contained" sx={{ px: 3, whiteSpace: 'nowrap', flexShrink: 0 }}>
            搜索
          </Button>
        </Paper>

        {empty ? (
          <Alert
            severity="info"
            variant="outlined"
            sx={{ mt: 1, maxWidth: 640, width: '100%', alignItems: 'flex-start' }}
          >
            <Typography variant="body2" sx={{ fontWeight: 600, mb: 0.5 }}>
              索引还是空的
            </Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
              服务已经跑起来了，但还没有抓取任何内容。去后台录入几个站点，再执行一次抓取就能搜了。
            </Typography>
            <Stack direction="row" spacing={1}>
              <Button size="small" variant="contained" component={RouterLink} to="/manage/whitelist">
                去后台加站点
              </Button>
              <Typography variant="caption" color="text.secondary" sx={{ alignSelf: 'center' }}>
                或在启动时加 <code>-crawl-once</code> 抓取已配置的站点
              </Typography>
            </Stack>
          </Alert>
        ) : (
          <Box sx={{ display: 'flex', gap: 1, mt: 1 }}>
            {['crashloopbackoff', 'gse 分词', 'hamming distance'].map((s) => (
              <Button key={s} size="small" onClick={() => nav(`/search?q=${encodeURIComponent(s)}`)}
                sx={{ color: 'text.secondary', textTransform: 'none' }}>
                {s}
              </Button>
            ))}
          </Box>
        )}

        {st !== null && !empty && (
          <Typography variant="caption" color="text.secondary">
            已收录 {st.documents} 篇文档 · {st.sites} 个白名单站点
          </Typography>
        )}
      </Stack>
    </Container>
  );
}
