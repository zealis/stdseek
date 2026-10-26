import { useCallback, useEffect, useState } from 'react';
import {
  Alert, Box, Button, Chip, CircularProgress, Container, Pagination, Paper, Stack,
  TextField, ToggleButton, ToggleButtonGroup, Tooltip, Typography,
} from '@mui/material';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { publicFeatures, search, type Hit } from '../api';
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
  const [discover, setDiscover] = useState<Hit[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [showWeak, setShowWeak] = useState(false);
  const [freshIntent, setFreshIntent] = useState(false);
  // AI 摘要入口由后台开关决定；默认 false，接口失败也保持 false
  const [aiSummary, setAiSummary] = useState(false);

  useEffect(() => {
    void publicFeatures().then((r) => setAiSummary(Boolean(r.features?.ai_summary)));
  }, []);

  const run = useCallback(async (query: string, pg: number, ex: string[], sc: number) => {
    if (!query.trim()) return;
    setLoading(true);
    setError('');
    try {
      const r = await search(query, { exclude: ex, since: sc, page: pg, size: PAGE_SIZE });
      setHits(r.hits ?? []);
      setDiscover(r.discover ?? []);
      setTotal(r.total);
      setFreshIntent(Boolean(r.freshness_intent));
    } catch (e) {
      setError((e as Error).message);
      setHits([]);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    setInput(q);
    setShowWeak(false);
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

  // 弱相关折叠：召回为了保住中文查询命中英文文档而放宽到「命中任一词即召回」，
  // 排序只能保证最相关的在前，保证不了弱相关不出现——「单元测试」首条 3.96 分、
  // 第 2 条 0.37 分，其后全是噪声。数据不删（翻页/展开仍可取），只收进折叠区。
  // 只在首页折叠：用户主动翻页说明他愿意看更多。
  const topScore = hits.length ? hits[0].score : 0;
  const cut = topScore * 0.2;
  const foldable = page === 1 && hits.length > 2 && topScore > 0;
  const strong = foldable ? hits.filter((h) => h.score >= cut) : hits;
  const weak = foldable ? hits.filter((h) => h.score < cut) : [];

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
        <Stack direction="row" spacing={1} alignItems="center" sx={{ flexWrap: 'wrap' }} useFlexGap>
          <Typography variant="body2" color="text.secondary">
            {loading ? '检索中…' : q ? `“${q}” 共 ${total} 条结果 · 每个站点只展示最相关的若干条` : '输入关键词开始搜索'}
          </Typography>
          {!loading && freshIntent && (
            <Tooltip title="查询含「最新 / 最近」这类时效意图，结果已按发布时间加权">
              <Chip size="small" label="按发布时间优先" sx={{ height: 20, bgcolor: 'rgba(47,79,79,0.07)', color: 'primary.main' }} />
            </Tooltip>
          )}
        </Stack>
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
        {strong.map((h) => <ResultCard key={h.url} hit={h} q={q} aiSummary={aiSummary} />)}
      </Stack>

      {weak.length > 0 && (
        <Box sx={{ mt: 2 }}>
          <Button size="small" variant="text" onClick={() => setShowWeak((v) => !v)}
            sx={{ color: 'text.secondary', px: 1 }}>
            {showWeak ? '收起弱相关结果' : `还有 ${weak.length} 条相关性较低的结果`}
          </Button>
          {showWeak && (
            <Stack spacing={1.5} sx={{ mt: 1.5 }}>
              {weak.map((h) => <ResultCard key={h.url} hit={h} q={q} aiSummary={aiSummary} />)}
            </Stack>
          )}
        </Box>
      )}

      {discover.length > 0 && (
        <Paper variant="outlined" sx={{ mt: 4, p: 2, bgcolor: 'background.paper' }}>
          <Stack direction="row" spacing={1} alignItems="center" sx={{ mb: 1.5, px: 0.5 }}>
            <Typography variant="subtitle2" sx={{ color: 'primary.main' }}>深网发现</Typography>
            <Tooltip title="与查询相关、但语料内很少有人链接的内容——独立博客、小开源项目、冷门讨论。繁荣度算法反着用：不被引用的好内容也值得被看见。">
              <Typography variant="caption" color="text.secondary" sx={{ cursor: 'help' }}>ⓘ 为什么有这一区</Typography>
            </Tooltip>
          </Stack>
          <Stack spacing={1.5}>
            {discover.map((h) => <ResultCard key={`d-${h.url}`} hit={h} q={q} aiSummary={aiSummary} />)}
          </Stack>
        </Paper>
      )}

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
