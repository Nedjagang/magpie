"use client";

import { motion, useInView } from "framer-motion";
import { useRef } from "react";

export function Architecture() {
  const ref = useRef(null);
  const isInView = useInView(ref, { once: true, margin: "-80px" });

  return (
    <section ref={ref} className="py-28 px-6 md:px-20">
      <div className="section-divider mb-28" />

      <div className="max-w-[1200px] mx-auto">
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-16 items-center">
          {/* Left - text */}
          <div>
            <motion.span
              initial={{ opacity: 0 }}
              animate={isInView ? { opacity: 1 } : {}}
              transition={{ duration: 0.6 }}
              className="text-[11px] font-medium tracking-[0.2em] uppercase text-[var(--color-accent)]"
            >
              Architecture
            </motion.span>

            <motion.h2
              initial={{ opacity: 0, y: 24 }}
              animate={isInView ? { opacity: 1, y: 0 } : {}}
              transition={{ duration: 0.7, delay: 0.1 }}
              className="font-[family-name:var(--font-space-grotesk)] font-bold text-[clamp(2rem,4vw,3.2rem)] leading-[0.92] tracking-[-1.5px] text-white mt-4"
            >
              One binary. Zero dependencies. Production in five minutes.
            </motion.h2>

            <motion.p
              initial={{ opacity: 0, y: 16 }}
              animate={isInView ? { opacity: 1, y: 0 } : {}}
              transition={{ duration: 0.7, delay: 0.2 }}
              className="text-[15px] text-white/45 font-light leading-relaxed mt-5 max-w-[420px]"
            >
              Magpie is a single Go binary serving the control plane, REST API, and
              OpAMP server. SQLite for small fleets. Postgres when you scale.
              The agent is a thin supervisor around vanilla otelcol-contrib —
              unmodified, unforked, always upstream.
            </motion.p>

            <motion.div
              initial={{ opacity: 0, y: 16 }}
              animate={isInView ? { opacity: 1, y: 0 } : {}}
              transition={{ duration: 0.7, delay: 0.35 }}
              className="mt-8 space-y-4"
            >
              <ArchPoint label="No custom agents" description="Vanilla otelcol-contrib on every host. Always." />
              <ArchPoint label="No config DSL" description="Standard OTel YAML. If it runs in otelcol, it runs in Magpie." />
              <ArchPoint label="No orchestration layer" description="No Kubernetes required. Runs anywhere Go compiles." />
            </motion.div>
          </div>

          {/* Right - visual */}
          <motion.div
            initial={{ opacity: 0, scale: 0.95 }}
            animate={isInView ? { opacity: 1, scale: 1 } : {}}
            transition={{ duration: 0.8, delay: 0.3 }}
            className="relative"
          >
            <div className="gradient-border rounded-2xl p-8 md:p-10 bg-black/40">
              <div className="space-y-6">
                <FlowStep
                  step="1"
                  title="magpie-agent"
                  subtitle="Lightweight supervisor on each host"
                  detail="OpAMP client · Process lifecycle · Health reporting"
                />
                <FlowArrow />
                <FlowStep
                  step="2"
                  title="magpied"
                  subtitle="Single-binary control plane"
                  detail="OpAMP server · REST API · SQLite/Postgres · Rollout engine"
                  highlighted
                />
                <FlowArrow />
                <FlowStep
                  step="3"
                  title="Dashboard"
                  subtitle="Config authoring & fleet visibility"
                  detail="Next.js 15 · Real-time fleet state · Publish dialog"
                />
              </div>
            </div>

            {/* Background glow for the card */}
            <div className="absolute inset-0 -z-10 bg-[var(--color-accent)] opacity-[0.03] blur-3xl rounded-full scale-75" />
          </motion.div>
        </div>
      </div>

      <div className="section-divider mt-28" />
    </section>
  );
}

function ArchPoint({ label, description }: { label: string; description: string }) {
  return (
    <div className="flex items-start gap-3">
      <div className="mt-1.5 w-1.5 h-1.5 rounded-full bg-[var(--color-accent)] shrink-0" />
      <div>
        <span className="text-[13px] font-medium text-white/80">{label}</span>
        <span className="text-[13px] text-white/40 font-light ml-1.5">{description}</span>
      </div>
    </div>
  );
}

function FlowStep({
  step,
  title,
  subtitle,
  detail,
  highlighted,
}: {
  step: string;
  title: string;
  subtitle: string;
  detail: string;
  highlighted?: boolean;
}) {
  return (
    <div className={`rounded-xl p-5 border ${highlighted ? "border-[var(--color-accent)]/20 bg-[var(--color-accent)]/[0.04]" : "border-white/[0.06] bg-white/[0.02]"}`}>
      <div className="flex items-center gap-3">
        <span className="text-[10px] font-mono text-[var(--color-accent)]/60 bg-[var(--color-accent)]/[0.08] w-6 h-6 rounded-md flex items-center justify-center">
          {step}
        </span>
        <div>
          <div className="font-[family-name:var(--font-space-grotesk)] text-[15px] font-semibold text-white tracking-tight">
            {title}
          </div>
          <div className="text-[11px] text-white/40 font-light">{subtitle}</div>
        </div>
      </div>
      <div className="text-[11px] text-white/30 font-light mt-2.5 pl-9 font-mono">
        {detail}
      </div>
    </div>
  );
}

function FlowArrow() {
  return (
    <div className="flex justify-center">
      <div className="w-px h-6 bg-gradient-to-b from-white/10 to-[var(--color-accent)]/20" />
    </div>
  );
}
