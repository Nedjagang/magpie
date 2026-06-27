export function sinceShort(iso: string): string {
  const diffMs = Date.now() - new Date(iso).getTime();
  const s = Math.max(0, Math.floor(diffMs / 1000));
  if (s < 5) return "just now";
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.floor(h / 24);
  return `${d}d ago`;
}

export function shortHash(hex?: string, n = 7): string {
  if (!hex) return "—";
  return hex.slice(0, n);
}

export function configStatusLabel(s?: string): string {
  if (!s) return "—";
  return s.replace(/^RemoteConfigStatuses_/, "").toLowerCase();
}
