"use client";

import { useState, FormEvent } from "react";
import { motion, AnimatePresence } from "framer-motion";

interface WaitlistFormProps {
  variant?: "hero" | "cta";
}

export function WaitlistForm({ variant = "hero" }: WaitlistFormProps) {
  const [email, setEmail] = useState("");
  const [status, setStatus] = useState<"idle" | "loading" | "success" | "error">("idle");
  const [errorMsg, setErrorMsg] = useState("");

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!email || !email.includes("@")) return;
    setStatus("loading");
    setErrorMsg("");

    try {
      const res = await fetch("/api/waitlist", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email }),
      });

      if (!res.ok) {
        const data = await res.json();
        throw new Error(data.error || "Request failed");
      }

      setStatus("success");
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : "Something went wrong.");
      setStatus("error");
    }
  };

  return (
    <div>
      <AnimatePresence mode="wait">
        {status === "success" ? (
          <motion.div
            key="success"
            initial={{ opacity: 0, scale: 0.95 }}
            animate={{ opacity: 1, scale: 1 }}
            transition={{ duration: 0.4 }}
            className="gradient-border rounded-2xl px-6 py-5 flex items-center justify-center gap-3 text-[15px] text-white"
          >
            <div className="w-8 h-8 rounded-full bg-emerald-500/20 flex items-center justify-center shrink-0">
              <svg
                width="16"
                height="16"
                viewBox="0 0 24 24"
                fill="none"
                stroke="#4ade80"
                strokeWidth="2.5"
                strokeLinecap="round"
                strokeLinejoin="round"
              >
                <path d="M5 13l4 4L19 7" />
              </svg>
            </div>
            <span className="font-light">
              You&apos;re in. Check your inbox for confirmation.
            </span>
          </motion.div>
        ) : (
          <motion.form
            key="form"
            onSubmit={handleSubmit}
            exit={{ opacity: 0, scale: 0.95 }}
            className="flex flex-col sm:flex-row gap-3"
          >
            <input
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="work@company.com"
              required
              className="waitlist-input flex-1 h-[52px] rounded-xl px-5 text-[15px] font-light"
            />
            <button
              type="submit"
              disabled={status === "loading"}
              className="h-[52px] bg-white text-black rounded-xl px-7 text-[14px] font-semibold flex items-center justify-center gap-2 hover:-translate-y-px hover:shadow-2xl hover:shadow-white/10 active:translate-y-0 transition-all duration-200 disabled:opacity-50 whitespace-nowrap cursor-pointer"
            >
              {status === "loading" ? (
                <span className="flex items-center gap-2">
                  <svg className="animate-spin h-4 w-4" viewBox="0 0 24 24" fill="none">
                    <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
                    <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
                  </svg>
                  Requesting...
                </span>
              ) : (
                <>
                  {variant === "hero" ? "Request Early Access" : "Get Started"}
                  <svg
                    width="14"
                    height="14"
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="2.5"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                  >
                    <path d="M5 12h14" />
                    <path d="M12 5l7 7-7 7" />
                  </svg>
                </>
              )}
            </button>
          </motion.form>
        )}
      </AnimatePresence>
      {status === "error" && (
        <p className="text-[12px] text-red-400/80 mt-2.5 text-center font-light">
          {errorMsg}
        </p>
      )}
      {status !== "success" && status !== "error" && (
        <p className="text-[11px] text-white/25 mt-3.5 text-center font-light">
          Limited spots. No credit card. Deploy in under 5 minutes.
        </p>
      )}
    </div>
  );
}
