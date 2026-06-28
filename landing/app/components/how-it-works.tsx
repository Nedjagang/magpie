"use client";

import { motion, useInView } from "framer-motion";
import { useRef } from "react";
import { VideoBackground } from "./video-background";

const features = [
  {
    number: "01",
    title: "One publish. Entire fleet.",
    description:
      "Author standard OpenTelemetry YAML in the dashboard. One click delivers it to every matching host in under two seconds. No SSH tunnels. No deployment pipelines. No restart windows.",
    highlights: ["Sub-2s delivery", "Zero downtime", "Hot-reload"],
  },
  {
    number: "02",
    title: "Scope-based targeting.",
    description:
      "Group hosts by product, variant, or individual identity. Deliver different pipeline configs to different cohorts from one control plane. Reassign hosts without touching the host.",
    highlights: ["Product cohorts", "Per-host overrides", "Dynamic grouping"],
  },
  {
    number: "03",
    title: "Phased rollouts. Automatic guardrails.",
    description:
      "Every change goes through canary → soak → promote. Failed canaries auto-halt. One-click rollback to any previous revision. Append-only audit trail of every action.",
    highlights: ["Canary gates", "Auto-rollback", "Audit log"],
  },
];

export function HowItWorks() {
  const ref = useRef(null);
  const isInView = useInView(ref, { once: true, margin: "-80px" });

  return (
    <section
      id="platform"
      ref={ref}
      className="relative py-32 px-6 md:px-20 overflow-hidden"
    >
      <VideoBackground
        src="https://d8j0ntlcm91z4.cloudfront.net/user_38xzZboKViGWJOttwIXH07lWA1P/hf_20260418_094631_d30ab262-45ee-4b7d-99f3-5d5848c8ef13.mp4"
        overlay="section"
      />

      {/* Ambient glow */}
      <div className="glow-orb w-[500px] h-[500px] bg-[#6DB3F2] top-[20%] right-[-200px]" style={{ animationDelay: "-5s" }} />

      <div className="relative z-10 max-w-[1200px] mx-auto">
        <motion.span
          initial={{ opacity: 0 }}
          animate={isInView ? { opacity: 1 } : {}}
          transition={{ duration: 0.6 }}
          className="text-[11px] font-medium tracking-[0.2em] uppercase text-[var(--color-accent)] block"
        >
          Platform
        </motion.span>

        <motion.h2
          initial={{ opacity: 0, y: 24 }}
          animate={isInView ? { opacity: 1, y: 0 } : {}}
          transition={{ duration: 0.8, delay: 0.1 }}
          className="font-[family-name:var(--font-space-grotesk)] font-bold text-[clamp(2.5rem,5vw,4.5rem)] leading-[0.9] tracking-[-2px] text-white mt-4 max-w-[600px]"
        >
          Fleet management, rebuilt from first principles.
        </motion.h2>

        <motion.p
          initial={{ opacity: 0, y: 16 }}
          animate={isInView ? { opacity: 1, y: 0 } : {}}
          transition={{ duration: 0.7, delay: 0.2 }}
          className="text-base text-white/45 font-light max-w-[500px] leading-relaxed mt-5"
        >
          Not another wrapper. A purpose-built control plane that treats
          OpenTelemetry configuration as a managed, versioned, observable artifact.
        </motion.p>

        <div className="grid grid-cols-1 lg:grid-cols-3 gap-px mt-20 rounded-2xl overflow-hidden border border-white/[0.06]">
          {features.map((feature, i) => (
            <motion.div
              key={feature.number}
              initial={{ opacity: 0, y: 30 }}
              animate={isInView ? { opacity: 1, y: 0 } : {}}
              transition={{ duration: 0.7, delay: 0.3 + i * 0.12 }}
              className="relative bg-white/[0.015] p-8 md:p-10 flex flex-col shimmer"
            >
              {i < features.length - 1 && (
                <div className="hidden lg:block absolute right-0 top-[10%] bottom-[10%] w-px bg-white/[0.06]" />
              )}
              <span className="text-[11px] font-mono text-[var(--color-accent)]/60 tracking-wider">
                {feature.number}
              </span>
              <h3 className="font-[family-name:var(--font-space-grotesk)] font-semibold text-[22px] tracking-tight text-white mt-4 leading-tight">
                {feature.title}
              </h3>
              <p className="text-[13px] text-white/50 font-light leading-relaxed mt-4 flex-1">
                {feature.description}
              </p>
              <div className="flex flex-wrap gap-2 mt-6">
                {feature.highlights.map((h) => (
                  <span
                    key={h}
                    className="text-[10px] font-medium uppercase tracking-wider text-white/40 bg-white/[0.04] rounded-md px-2.5 py-1 border border-white/[0.06]"
                  >
                    {h}
                  </span>
                ))}
              </div>
            </motion.div>
          ))}
        </div>
      </div>
    </section>
  );
}
