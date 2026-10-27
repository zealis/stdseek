export type GeoRiskRow = {
  ip: string;
  label: string;
  accounts: number;
  logins: number;
  last_seen: number;
};

export type IPBan = {
  id: number;
  pattern: string;
  reason: string;
  created_at: number;
  expires_at: number;
};
