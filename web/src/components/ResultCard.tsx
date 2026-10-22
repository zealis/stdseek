import { Card, CardContent, Chip, Link, Stack, Typography } from '@mui/material';

export type HitView = {
  url: string;
  domain: string;
  title: string;
  description: string;
  labels: string[] | null;
  score: number;
  reasons: string[] | null;
  time_by_fetch: boolean;
  sort_time: number;
};

function fmtTime(ts: number, byFetch: boolean) {
  if (!ts) return '';
  const d = new Date(ts * 1000);
  const s = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
  return byFetch ? `${s}（按收录时间）` : s;
}

export default function ResultCard({ hit }: { hit: HitView }) {
  return (
    <Card variant="outlined" sx={{ bgcolor: 'background.paper', '&:hover': { borderColor: 'primary.main' } }}>
      <CardContent sx={{ pb: '12px !important' }}>
        <Link href={hit.url} target="_blank" rel="noreferrer" underline="hover"
          sx={{ color: 'primary.main', fontWeight: 600, fontSize: '1.05rem' }}>
          {hit.title || hit.url}
        </Link>
        <Stack direction="row" spacing={1} alignItems="center" sx={{ mt: 0.5, mb: 1, flexWrap: 'wrap' }} useFlexGap>
          <Typography variant="caption" color="text.secondary">{hit.domain}</Typography>
          {hit.sort_time > 0 && (
            <Typography variant="caption" color="text.secondary">· {fmtTime(hit.sort_time, hit.time_by_fetch)}</Typography>
          )}
          {hit.labels?.map((l) => (
            <Chip key={l} label={l} size="small" variant="outlined"
              sx={{ borderColor: '#E0D6C4', color: 'secondary.main', height: 20 }} />
          ))}
        </Stack>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
          {hit.description || '（无摘要）'}
        </Typography>
        <Stack direction="row" spacing={0.5} sx={{ flexWrap: 'wrap' }} useFlexGap>
          {(hit.reasons ?? []).map((r) => (
            <Chip key={r} label={r} size="small"
              sx={{ bgcolor: 'rgba(47,79,79,0.07)', color: 'primary.main', height: 22 }} />
          ))}
        </Stack>
      </CardContent>
    </Card>
  );
}
