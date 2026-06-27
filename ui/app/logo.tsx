// Magpie wordmark glyph — "Pipeline Magpie" concept.
//
// A slender side-profile magpie whose long tail sweeps rightward, doubling as
// the telemetry pipeline flowing out of the collector. The tail is deliberately
// longer than the body (the defining feature of a real magpie) and tapers to a
// fine point. A small accent dot at the beak is the "shiny" — the signal the
// bird has collected.
//
// Single-path fills (no strokes) so it scales cleanly from 16px favicon to
// hero-size. Inherits currentColor for light/dark themes; accent dot is the
// only fixed-tint element and takes a prop for easy override.

export function MagpieGlyph({
  size = 30,
  accent = "var(--color-accent)",
  title = "Magpie",
}: {
  size?: number;
  accent?: string;
  title?: string;
}) {
  // viewBox is intentionally wide (120 x 44) to give the tail room to extend
  // well past the body — body lives in x: 2–44, tail runs to x≈116.
  const w = (size * 120) / 44;
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 120 44"
      width={w}
      height={size}
      role="img"
      aria-label={title}
      style={{ display: "block" }}
    >
      <title>{title}</title>

      {/* Body + head as a unified silhouette. Slender, forward-tilted wedge.
          Path is constructed so the head reads as a distinct lobe on top-left
          rather than a tacked-on circle. */}
      <path
        fill="currentColor"
        d="
          M 22 6
          C 15 6 11 10 11 15
          C 11 17 12 19 14 20
          C 10 21 7 24 7 28
          C 7 32 11 34 17 34
          L 38 34
          C 42 34 45 32 46 29
          L 44 24
          C 43 22 42 21 40 20
          L 38 14
          C 36 9 31 6 22 6 Z
        "
      />

      {/* Tail — a long curved sweep that tapers to a point far to the right.
          This is the magpie's signature feature; keep it at least 2x body
          length or the silhouette stops reading as "magpie". */}
      <path
        fill="currentColor"
        d="
          M 46 26
          C 68 30 92 34 116 40
          L 116 41
          C 92 36 68 32 45 29
          Z
        "
      />

      {/* Beak — small triangle pointing left from the head. */}
      <path fill="currentColor" d="M 11 14 L 3 13 L 11 16 Z" />

      {/* Legs — thin, short, spaced under the body. */}
      <rect x="20" y="34" width="1.4" height="7" rx="0.6" fill="currentColor" />
      <rect x="28" y="34" width="1.4" height="7" rx="0.6" fill="currentColor" />

      {/* White belly patch — the pied accent that distinguishes a magpie from
          a crow or a generic songbird. Sits inside the body silhouette. */}
      <ellipse cx="30" cy="28" rx="7" ry="2" fill="var(--color-paper)" />

      {/* Eye — tiny negative-space circle on the head. Disappears at favicon
          size but helps legibility at masthead size. */}
      <circle cx="20" cy="13" r="0.9" fill="var(--color-paper)" />

      {/* Shiny — the telemetry signal the magpie has collected. Only tinted
          element. */}
      <circle cx="1" cy="13" r="1.8" fill={accent} />
    </svg>
  );
}
