"use client";

import { motion } from "framer-motion";
import { VideoBackground } from "./video-background";
import { WaitlistForm } from "./waitlist-form";

const words = ["The", "control", "plane", "your", "fleet", "deserves."];

export function Hero() {
  return (
    <section
      id="top"
      className="relative min-h-screen flex flex-col items-center justify-center text-center px-6 pt-32 pb-20 overflow-hidden"
    >
      <VideoBackground
        src="https://d8j0ntlcm91z4.cloudfront.net/user_38xzZboKViGWJOttwIXH07lWA1P/hf_20260418_080021_d598092b-c4c2-4e53-8e46-94cf9064cd50.mp4"
        overlay="hero"
      />

      {/* Accent glow orbs */}
      <div className="glow-orb w-[600px] h-[600px] bg-[#6DB3F2] top-[-200px] left-[-200px]" />
      <div className="glow-orb w-[400px] h-[400px] bg-[#4B8CE8] bottom-[-100px] right-[-100px]" style={{ animationDelay: "-7s" }} />

      <div className="relative z-10 flex flex-col items-center max-w-[900px]">
        {/* Status badge */}
        <motion.div
          initial={{ opacity: 0, y: 16, scale: 0.96 }}
          animate={{ opacity: 1, y: 0, scale: 1 }}
          transition={{ duration: 0.7, delay: 0.2 }}
          className="gradient-border rounded-full flex items-center gap-3 px-1.5 py-1 mb-8"
        >
          <span className="bg-[var(--color-accent)] text-black px-3 py-0.5 text-[10px] font-bold rounded-full uppercase tracking-wider">
            Private Beta
          </span>
          <span className="text-[13px] text-white/80 pr-4 font-light">
            Now accepting early design partners
          </span>
        </motion.div>

        {/* Headline */}
        <h1 className="font-[family-name:var(--font-space-grotesk)] font-bold text-[clamp(3.2rem,8vw,6.5rem)] leading-[0.88] tracking-[-3px] text-white">
          <span className="flex flex-wrap justify-center gap-x-[0.25em] gap-y-[0.08em]">
            {words.map((word, i) => (
              <motion.span
                key={`${word}-${i}`}
                initial={{ opacity: 0, y: 50, filter: "blur(10px)" }}
                animate={{ opacity: 1, y: 0, filter: "blur(0px)" }}
                transition={{
                  duration: 0.8,
                  delay: 0.3 + i * 0.07,
                  ease: [0.16, 1, 0.3, 1],
                }}
              >
                {word}
              </motion.span>
            ))}
          </span>
        </h1>

        {/* Subheadline */}
        <motion.p
          initial={{ opacity: 0, y: 20 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.8, delay: 0.9 }}
          className="text-lg md:text-xl text-white/60 font-light leading-relaxed max-w-[640px] mt-7 tracking-[-0.2px]"
        >
          Magpie manages OpenTelemetry pipelines across thousands of hosts from a single pane.
          Push config once. Every collector reloads in under two seconds. No agents to fork.
          No YAML to SSH. No vendor lock-in.
        </motion.p>

        {/* Waitlist */}
        <motion.div
          initial={{ opacity: 0, y: 20 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.8, delay: 1.1 }}
          id="early-access"
          className="w-full max-w-[520px] mt-10"
        >
          <WaitlistForm variant="hero" />
        </motion.div>

        {/* Social proof / metrics strip */}
        <motion.div
          initial={{ opacity: 0, y: 16 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.8, delay: 1.4 }}
          className="flex flex-wrap items-center justify-center gap-8 mt-14"
        >
          <Metric value="< 2s" label="Fleet-wide propagation" />
          <div className="w-px h-8 bg-white/10 hidden sm:block" />
          <Metric value="2,000+" label="Agents per instance" />
          <div className="w-px h-8 bg-white/10 hidden sm:block" />
          <Metric value="0" label="Custom DSLs" />
        </motion.div>
      </div>

      {/* Trusted by strip */}
      <motion.div
        initial={{ opacity: 0 }}
        animate={{ opacity: 1 }}
        transition={{ duration: 1, delay: 1.8 }}
        className="relative z-10 mt-20 flex flex-col items-center gap-5"
      >
        <span className="text-[11px] text-white/30 uppercase tracking-[0.15em] font-medium">
          Ships to any OTLP-compatible backend
        </span>
        <div className="flex flex-wrap justify-center items-center gap-x-12 gap-y-4">
          {["Grafana", "Datadog", "Honeycomb", "Splunk", "New Relic"].map((name) => (
            <span
              key={name}
              className="font-[family-name:var(--font-space-grotesk)] text-[17px] text-white/30 font-medium tracking-tight hover:text-white/60 transition-colors duration-300"
            >
              {name}
            </span>
          ))}
        </div>
      </motion.div>
    </section>
  );
}

function Metric({ value, label }: { value: string; label: string }) {
  return (
    <div className="text-center">
      <div className="font-[family-name:var(--font-space-grotesk)] font-bold text-2xl md:text-3xl tracking-tight text-white">
        {value}
      </div>
      <div className="text-[11px] text-white/40 font-light mt-1 tracking-wide">
        {label}
      </div>
    </div>
  );
}
