"use client";

import { useState, useEffect } from "react";
import { motion } from "framer-motion";

export function Navbar() {
  const [scrolled, setScrolled] = useState(false);

  useEffect(() => {
    const handleScroll = () => setScrolled(window.scrollY > 60);
    window.addEventListener("scroll", handleScroll, { passive: true });
    return () => window.removeEventListener("scroll", handleScroll);
  }, []);

  return (
    <motion.nav
      initial={{ y: -20, opacity: 0 }}
      animate={{ y: 0, opacity: 1 }}
      transition={{ duration: 0.8, delay: 0.1 }}
      className="fixed top-5 left-0 right-0 z-50 flex items-center justify-center px-6"
    >
      <div
        className={`flex items-center gap-1 rounded-full px-2 py-1.5 transition-all duration-500 ${
          scrolled
            ? "glass-strong shadow-2xl shadow-black/40"
            : "bg-transparent border border-transparent"
        }`}
      >
        <a
          href="#top"
          className="flex items-center gap-2.5 px-4 py-2 rounded-full hover:bg-white/[0.04] transition-colors"
        >
          <svg width="20" height="20" viewBox="0 0 24 24" fill="#fff">
            <path d="M22 7.4l-3.1.5c-.5-1.3-1.7-2.2-3.3-2.2-1.9 0-3.2 1.2-3.6 2.8L3 17.2c-.3.3-.1.8.3.7l4.2-.9c.3 1.6 1.6 2.9 3.6 2.9 1.4 0 2.5-.6 3.1-1.6.5.9 1.4 1.5 2.5 1.5v-1.5c-1 0-1.7-.7-1.7-1.8 0-.6.2-1 .6-1.5l3.7-4.3-2.6.5c-.1-.5-.4-1-.7-1.3L22 7.4z" />
            <circle cx="16.3" cy="8.4" r="0.85" fill="#000" />
          </svg>
          <span className="text-[14px] font-semibold font-[family-name:var(--font-space-grotesk)] tracking-tight hidden sm:inline">
            Magpie
          </span>
        </a>

        <div className="hidden md:flex items-center">
          <NavLink href="#platform">Platform</NavLink>
          <NavLink href="#why">Why Magpie</NavLink>
          <NavLink href="https://github.com/Nedjagang/magpie" external>
            GitHub
          </NavLink>
        </div>

        <a
          href="#early-access"
          className="ml-3 bg-white text-black px-5 py-2 rounded-full text-[13px] font-semibold flex items-center gap-2 hover:-translate-y-px hover:shadow-xl hover:shadow-white/10 active:translate-y-0 transition-all duration-200"
        >
          Request Access
          <svg
            width="12"
            height="12"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2.5"
            strokeLinecap="round"
            strokeLinejoin="round"
          >
            <path d="M7 17L17 7" />
            <path d="M7 7h10v10" />
          </svg>
        </a>
      </div>
    </motion.nav>
  );
}

function NavLink({
  href,
  children,
  external,
}: {
  href: string;
  children: React.ReactNode;
  external?: boolean;
}) {
  return (
    <a
      href={href}
      {...(external ? { target: "_blank", rel: "noopener noreferrer" } : {})}
      className="text-[13px] font-medium text-white/70 px-4 py-2 rounded-full hover:text-white hover:bg-white/[0.04] transition-all duration-200"
    >
      {children}
    </a>
  );
}
