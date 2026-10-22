import { useCallback, useEffect, useState } from 'react';
import {
  Alert, Box, Button, Chip, CircularProgress, Container, Pagination, Paper, Stack,
  TextField, ToggleButton, ToggleButtonGroup, Typography,
} from '@mui/material';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { search, type Hit } from '../api';
import { FILTERS, TIME_WINDOWS, loadPrefs } from '../prefs';
import ResultCard from '../components/ResultCard';

const PAGE_SIZE = 10;

export default function Results() {
  const [sp, setSp] = useSearchParams();
  const nav = useNavigate();
  const q = sp.get('q') ?? '';
  const page = Number(sp.get('page') ?? '1');
  const prefs = loadPrefs();

  const [input, setInput] = useState(q);
  const [exclude, setExclude] = useState<string[]>(prefs.exclude);
  const [since, setSince] = useState<number>(prefs.since);
  const [hits, setHits] = useState<Hit[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const run = useCallback(async (query: string, pg: number, ex: string[], sc: number) => {
    if (!query.trim()) return;
    setLoading(true);
    setError('');
    try {
      const r = await search(query, { exclude: ex, since: sc, page: pg, size: PAGE_SIZE });
      setHits(r.hits ?? []);
      setTotal(r.total);
    } catch (e) {
      setError((e as Error).message);
      setHits([]);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    setInput(q);
    void run(q, page, exclude, since);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q, page, exclude.join(','), since]);

  const submit = () => {
    const v = input.trim();
    if (!v) return;
    setSp({ q: v, page: '1' });
  };

  const toggle = (key: string) => {
    setExclude((prev) => (prev.includes(key) ? prev.filter((x) => x !== key) : [...prev, key]));
    setSp({ q, page: '1' });
  };

  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  return (
    <Container sx={{ pt: 3, pb: 6 }}>
      <Paper component="form" variant="outlined"
        onSubmit={(e) => { e.preventDefault(); submit(); }}
        sx={{ p: 0.75, display: 'flex', gap: 1, bgcolor: 'background.paper' }}>
        <TextField
          fullWidth
          value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder="搜索报错文本、API、版本号…"
          variant="standard"
          InputProps={{ disableUnderline: true, sx: { px: 1.5 } }}
        />
        <Button type="submit" variant="contained" sx={{ px: 3, whiteSpace: 'nowrap', flexShrink: 0 }}>搜索</Button>
      </Paper>

      <Stack direction="row" spacing={1} sx={{ mt: 2, flexWrap: 'wrap' }} useFlexGap alignItems="center">
        <Typography variant="caption" color="text.secondary" sx={{ mr: 0.5 }}>过滤</Typography>
        {FILTERS.map((f) => {
          const on = exclude.includes(f.key);
          return (
            <Chip
              key={f.key}
              label={f.label}
              title={f.hint}
              onClick={() => toggle(f.key)}
              color={on ? 'primary' : 'default'}
              variant={on ? 'filled' : 'outlined'}
            />
          );
        })}
        <Box sx={{ flexGrow: 1 }} />
        <ToggleButtonGroup
          size="small"
          exclusive
          value={since}
          sx={{ '& .MuiToggleButton-root.Mui-selected': { bgcolor: 'primary.main', color: '#fff' },
            '& .MuiToggleButton-root.Mui-selected:hover': { bgcolor: 'primary.main' } }}
          onChange={(_, v) => { if (v !== null) { setSince(v); setSp({ q, page: '1' }); } }}
        >
          {TIME_WINDOWS.map((t) => (
            <ToggleButton key={t.since} value={t.since} sx={{ px: 1.5, py: 0.25, fontSize: '0.8rem' }}>
              {t.label}
            </ToggleButton>
          ))}
        </ToggleButtonGroup>
      </Stack>

      <Box sx={{ mt: 2, mb: 1.5 }}>
        <Typography variant="body2" color="text.secondary">
          {loading ? '检索中…' : q ? `“${q}” 共 ${total} 条结果 · 每个站点只展示最相关的若干条` : '输入关键词开始搜索'}
        </Typography>
      </Box>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}
      {loading && <Box sx={{ display: 'flex', justifyContent: 'center', py: 6 }}><CircularProgress size={28} /></Box>}

      {!loading && !error && hits.length === 0 && q && (
        <Paper variant="outlined" sx={{ p: 4, textAlign: 'center', bgcolor: 'background.paper' }}>
          <Typography color="text.secondary">没有找到结果。</Typography>
          <Typography variant="caption" color="text.secondary">
            索引只覆盖白名单站点；可以试试去掉过滤开关，或缩短关键词。
          </Typography>
        </Paper>
      )}

      <Stack spacing={1.5}>
        {hits.map((h) => <ResultCard key={h.url} hit={h} />)}
      </Stack>

      {pages > 1 && (
        <Box sx={{ display: 'flex', justifyContent: 'center', mt: 3 }}>
          <Pagination
            count={pages}
            page={page}
            onChange={(_, p) => { nav(`/search?q=${encodeURIComponent(q)}&page=${p}`); window.scrollTo(0, 0); }}
          />
        </Box>
      )}
    </Container>
  );
}
