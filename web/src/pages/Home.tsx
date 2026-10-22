import { useState } from 'react';
import { Box, Button, Container, Paper, Stack, TextField, Typography } from '@mui/material';
import { useNavigate } from 'react-router-dom';

export default function Home() {
  const [q, setQ] = useState('');
  const nav = useNavigate();

  const go = () => {
    const v = q.trim();
    if (!v) return;
    nav(`/search?q=${encodeURIComponent(v)}`);
  };

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

        <Box sx={{ display: 'flex', gap: 1, mt: 1 }}>
          {['crashloopbackoff', 'gse 分词', 'hamming distance'].map((s) => (
            <Button key={s} size="small" onClick={() => nav(`/search?q=${encodeURIComponent(s)}`)}
              sx={{ color: 'text.secondary', textTransform: 'none' }}>
              {s}
            </Button>
          ))}
        </Box>
      </Stack>
    </Container>
  );
}
