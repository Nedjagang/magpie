"use client";

import { motion, useInView } from "framer-motion";
import { useRef } from "react";

export function Bento() {
  const ref = useRef(null);
  const isInView = useInView(ref, { once: true, margin: "-80px" });

  return (
    <section ref={ref} className="py-28 px-6 md:px-20">
      <div className="max-w-[1200px] mx-auto">
        <motion.span
          initial={{ opacity: 0 }}
          animate={isInView ? { opacity: 1 } : {}}
          transition={{ duration: 0.6 }}
          className="text-[11px] font-medium tracking-[0.2em] uppercase text-[var(--color-accent)]"
        >
          What changes
        </motion.span>

        <motion.h2
          initial={{ opacity: 0, y: 20 }}
          animate={isInView ? { opacity: 1, y: 0 } : {}}
          transition={{ duration: 0.7, delay: 0.1 }}
          className="font-[family-name:var(--font-space-grotesk)] font-bold text-[clamp(2rem,4vw,3.2rem)] leading-[0.92] tracking-[-1.5px] text-white mt-4 max-w-[500px]"
        >
          Before Magpie vs. after.
        </motion.h2>

        {/* Bento grid */}
        <motion.div
          initial={{ opacity: 0, y: 24 }}
          animate={isInView ? { opacity: 1, y: 0 } : {}}
          transition={{ duration: 0.8, delay: 0.2 }}
          className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4 mt-14"
        >
          {/* Large card */}
          <div className="lg:col-span-2 gradient-border rounded-2xl p-8 md:p-10 relative overflow-hidden">
            <div className="absolute top-0 right-0 w-[300px] h-[300px] bg-[var(--color-accent)] opacity-[0.03] blur-[100px] rounded-full" />
            <div className="relative">
              <div className="text-[11px] font-mono text-[var(--color-accent)]/50 tracking-wider mb-4">
                BEFORE
              </div>
              <div className="space-y-2.5">
                <StrikeThrough text="SSH into each host to update YAML" />
                <StrikeThrough text="Ansible playbooks with 30-minute runs" />
                <StrikeThrough text="Hope nothing breaks during rollout" />
                <StrikeThrough text="No visibility into what's actually applied" />
              </div>

              <div className="h-px bg-white/[0.06] my-7" />

              <div className="text-[11px] font-mono text-emerald-400/60 tracking-wider mb-4">
                AFTER MAGPIE
              </div>
              <div className="space-y-2.5">
                <CheckItem text="Edit YAML in dashboard. Click publish." />
                <CheckItem text="Sub-2s propagation. Zero-downtime reload." />
                <CheckItem text="Canary gates catch failures before fleet-wide." />
                <CheckItem text="Every host's applied config visible in real-time." />
              </div>
            </div>
          </div>

          {/* Side cards */}
          <div className="space-y-4">
            <div className="gradient-border rounded-2xl p-6 h-[calc(50%-8px)]">
              <div className="text-[40px] font-[family-name:var(--font-space-grotesk)] font-bold text-white leading-none tracking-tight">
                95%
              </div>
              <div className="text-[13px] text-white/40 font-light mt-2 leading-relaxed">
                Reduction in config management time for early design partners.
              </div>
            </div>
            <div className="gradient-border rounded-2xl p-6 h-[calc(50%-8px)] flex flex-col justify-between">
              <div>
                <div className="text-[28px] font-[family-name:var(--font-space-grotesk)] font-bold text-white leading-none tracking-tight">
                  5 min
                </div>
                <div className="text-[13px] text-white/40 font-light mt-2 leading-relaxed">
                  From git clone to managing your first fleet.
                </div>
              </div>
              <div className="text-[10px] text-white/25 font-mono mt-4">
                make quickstart
              </div>
            </div>
          </div>
        </motion.div>
      </div>
    </section>
  );
}

function StrikeThrough({ text }: { text: string }) {
  return (
    <div className="flex items-center gap-3">
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="rgba(255,100,100,0.5)" strokeWidth="2" strokeLinecap="round">
        <path d="M18 6L6 18M6 6l12 12" />
      </svg>
      <span className="text-[14px] text-white/30 font-light line-through decoration-white/15">
        {text}
      </span>
    </div>
  );
}

function CheckItem({ text }: { text: string }) {
  return (
    <div className="flex items-center gap-3">
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="rgba(74,222,128,0.7)" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
        <path d="M5 13l4 4L19 7" />
      </svg>
      <span className="text-[14px] text-white/70 font-light">
        {text}
      </span>
    </div>
  );
}
