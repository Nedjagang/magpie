"use client";

import { motion, useInView } from "framer-motion";
import { useRef } from "react";
import { VideoBackground } from "./video-background";
import { WaitlistForm } from "./waitlist-form";

export function CTA() {
  const ref = useRef(null);
  const isInView = useInView(ref, { once: true, margin: "-80px" });

  return (
    <section
      ref={ref}
      className="relative min-h-[80vh] flex items-center justify-center px-6 py-32 overflow-hidden"
    >
      <VideoBackground
        src="https://d8j0ntlcm91z4.cloudfront.net/user_38xzZboKViGWJOttwIXH07lWA1P/hf_20260418_080021_d598092b-c4c2-4e53-8e46-94cf9064cd50.mp4"
        overlay="cta"
      />

      {/* Glow */}
      <div className="glow-orb w-[500px] h-[500px] bg-[#6DB3F2] top-[30%] left-[30%]" style={{ animationDelay: "-10s" }} />

      <div className="relative z-10 max-w-[600px] w-full text-center flex flex-col items-center">
        <motion.span
          initial={{ opacity: 0 }}
          animate={isInView ? { opacity: 1 } : {}}
          transition={{ duration: 0.6 }}
          className="text-[11px] font-medium tracking-[0.2em] uppercase text-[var(--color-accent)]"
        >
          Early Access
        </motion.span>

        <motion.h2
          initial={{ opacity: 0, y: 30 }}
          animate={isInView ? { opacity: 1, y: 0 } : {}}
          transition={{ duration: 0.8, delay: 0.1 }}
          className="font-[family-name:var(--font-space-grotesk)] font-bold text-[clamp(2.4rem,5vw,4rem)] leading-[0.9] tracking-[-2px] text-white mt-5"
        >
          Stop managing configs
          <br />
          <span className="text-white/60">by hand.</span>
        </motion.h2>

        <motion.p
          initial={{ opacity: 0, y: 16 }}
          animate={isInView ? { opacity: 1, y: 0 } : {}}
          transition={{ duration: 0.7, delay: 0.25 }}
          className="text-[15px] text-white/45 font-light leading-relaxed mt-5"
        >
          We&apos;re onboarding design partners who run 50+ OTel collectors.
          Get priority access, direct support, and shape the roadmap.
        </motion.p>

        <motion.div
          initial={{ opacity: 0, y: 20 }}
          animate={isInView ? { opacity: 1, y: 0 } : {}}
          transition={{ duration: 0.7, delay: 0.4 }}
          className="w-full max-w-[480px] mt-10"
        >
          <WaitlistForm variant="cta" />
        </motion.div>

        {/* Trust signals */}
        <motion.div
          initial={{ opacity: 0 }}
          animate={isInView ? { opacity: 1 } : {}}
          transition={{ duration: 0.8, delay: 0.6 }}
          className="flex flex-wrap justify-center gap-6 mt-14"
        >
          <TrustSignal label="Apache 2.0" sublabel="Open source, always" />
          <TrustSignal label="Go + SQLite" sublabel="Zero runtime dependencies" />
          <TrustSignal label="OpAMP native" sublabel="Industry-standard protocol" />
        </motion.div>
      </div>
    </section>
  );
}

function TrustSignal({ label, sublabel }: { label: string; sublabel: string }) {
  return (
    <div className="text-center px-4">
      <div className="font-[family-name:var(--font-space-grotesk)] text-[14px] font-semibold text-white/70 tracking-tight">
        {label}
      </div>
      <div className="text-[10px] text-white/30 font-light mt-0.5">
        {sublabel}
      </div>
    </div>
  );
}
