"use client";

import { motion, useInView } from "framer-motion";
import { useRef } from "react";

const rows = [
  {
    dimension: "Agent distribution",
    magpie: "Vanilla otelcol-contrib, unmodified",
    others: "Custom forked agent binary",
    magpieWins: true,
  },
  {
    dimension: "Config language",
    magpie: "Standard OTel YAML — no abstraction layer",
    others: "Proprietary DSL or guided UI pipelines",
    magpieWins: true,
  },
  {
    dimension: "Deployment",
    magpie: "Single binary + SQLite. 5 minutes.",
    others: "Cloud-managed or complex self-host",
    magpieWins: true,
  },
  {
    dimension: "Fleet scale",
    magpie: "2,000+ agents per instance (Postgres-backed)",
    others: "Varies — often requires cloud tier",
    magpieWins: true,
  },
  {
    dimension: "Rollout safety",
    magpie: "Phased canary with automatic gates",
    others: "Instant push or manual canary",
    magpieWins: true,
  },
  {
    dimension: "Vendor lock-in",
    magpie: "Apache 2.0. Self-host forever.",
    others: "Source-available or cloud-only",
    magpieWins: true,
  },
];

export function Compare() {
  const ref = useRef(null);
  const isInView = useInView(ref, { once: true, margin: "-80px" });

  return (
    <section id="why" ref={ref} className="py-32 px-6 md:px-20">
      <div className="max-w-[1200px] mx-auto">
        <motion.span
          initial={{ opacity: 0 }}
          animate={isInView ? { opacity: 1 } : {}}
          transition={{ duration: 0.6 }}
          className="text-[11px] font-medium tracking-[0.2em] uppercase text-[var(--color-accent)]"
        >
          Why Magpie
        </motion.span>

        <motion.h2
          initial={{ opacity: 0, y: 24 }}
          animate={isInView ? { opacity: 1, y: 0 } : {}}
          transition={{ duration: 0.8, delay: 0.1 }}
          className="font-[family-name:var(--font-space-grotesk)] font-bold text-[clamp(2.2rem,4.5vw,4rem)] leading-[0.9] tracking-[-2px] text-white mt-4 max-w-[550px]"
        >
          The fleet management you wanted from day one.
        </motion.h2>

        <motion.p
          initial={{ opacity: 0, y: 16 }}
          animate={isInView ? { opacity: 1, y: 0 } : {}}
          transition={{ duration: 0.7, delay: 0.2 }}
          className="text-[15px] text-white/40 font-light max-w-[520px] leading-relaxed mt-5"
        >
          Existing solutions force a tradeoff: vendor lock-in for ease, or raw
          complexity for control. Magpie gives you both.
        </motion.p>

        {/* Comparison table */}
        <motion.div
          initial={{ opacity: 0, y: 24 }}
          animate={isInView ? { opacity: 1, y: 0 } : {}}
          transition={{ duration: 0.8, delay: 0.3 }}
          className="mt-14 gradient-border rounded-2xl overflow-hidden"
        >
          {/* Header */}
          <div className="grid grid-cols-[1fr_1.2fr_1.2fr] border-b border-white/[0.06] bg-white/[0.02]">
            <div className="p-5 md:px-8" />
            <div className="p-5 md:px-8 border-l border-white/[0.06]">
              <div className="flex items-center gap-2">
                <svg width="16" height="16" viewBox="0 0 24 24" fill="var(--color-accent)">
                  <path d="M22 7.4l-3.1.5c-.5-1.3-1.7-2.2-3.3-2.2-1.9 0-3.2 1.2-3.6 2.8L3 17.2c-.3.3-.1.8.3.7l4.2-.9c.3 1.6 1.6 2.9 3.6 2.9 1.4 0 2.5-.6 3.1-1.6.5.9 1.4 1.5 2.5 1.5v-1.5c-1 0-1.7-.7-1.7-1.8 0-.6.2-1 .6-1.5l3.7-4.3-2.6.5c-.1-.5-.4-1-.7-1.3L22 7.4z" />
                </svg>
                <span className="font-[family-name:var(--font-space-grotesk)] text-[13px] font-semibold text-white">
                  Magpie
                </span>
              </div>
            </div>
            <div className="p-5 md:px-8 border-l border-white/[0.06]">
              <span className="text-[13px] text-white/40 font-medium">
                Bindplane / Alloy / Others
              </span>
            </div>
          </div>

          {/* Rows */}
          {rows.map((row, i) => (
            <div
              key={row.dimension}
              className={`grid grid-cols-[1fr_1.2fr_1.2fr] ${
                i < rows.length - 1 ? "border-b border-white/[0.04]" : ""
              } hover:bg-white/[0.01] transition-colors`}
            >
              <div className="p-5 md:px-8 flex items-center">
                <span className="text-[12px] text-white/50 font-medium">
                  {row.dimension}
                </span>
              </div>
              <div className="p-5 md:px-8 border-l border-white/[0.06] flex items-center gap-2">
                <div className="w-1.5 h-1.5 rounded-full bg-emerald-400/80 shrink-0" />
                <span className="text-[13px] text-white/80 font-light">
                  {row.magpie}
                </span>
              </div>
              <div className="p-5 md:px-8 border-l border-white/[0.06] flex items-center">
                <span className="text-[13px] text-white/35 font-light">
                  {row.others}
                </span>
              </div>
            </div>
          ))}
        </motion.div>
      </div>
    </section>
  );
}
