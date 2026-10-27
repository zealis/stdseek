export type GeoRiskRow = {
  ip: string;
  label: string;
  accounts: number;
  logins: number;
  last_seen: number;
  // 以下来自归属地缓存，未解析过时为 0 / 空
  asn?: number;
  score?: number;
  status?: string;
  risk?: string;
};

export type GeoInfo = {
  label: string;
  asn: number;
  score: number;
  status: string;
  risk: string; // JSON 数组字符串
};

export type IPBan = {
  id: number;
  pattern: string;
  reason: string;
  created_at: number;
  expires_at: number;
};
