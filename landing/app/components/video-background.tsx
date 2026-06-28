"use client";

import { useRef, useEffect } from "react";

interface VideoBackgroundProps {
  src: string;
  className?: string;
  overlay?: "hero" | "section" | "cta";
}

export function VideoBackground({ src, className = "", overlay = "section" }: VideoBackgroundProps) {
  const videoRef = useRef<HTMLVideoElement>(null);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;

    video.playbackRate = 0.6;

    const show = () => {
      video.style.opacity = "1";
    };

    video.addEventListener("canplaythrough", show);
    video.addEventListener("playing", show);

    if (video.readyState >= 3) {
      show();
    }

    video.play().catch(() => {});

    return () => {
      video.removeEventListener("canplaythrough", show);
      video.removeEventListener("playing", show);
    };
  }, []);

  const overlayClass = {
    hero: "from-black/50 via-black/20 to-black/60",
    section: "from-black/40 via-black/15 to-black/50",
    cta: "from-black/50 via-black/25 to-black/60",
  }[overlay];

  return (
    <>
      <video
        ref={videoRef}
        autoPlay
        muted
        loop
        playsInline
        preload="auto"
        className={`absolute inset-0 w-full h-full object-cover transition-opacity duration-[2000ms] scale-[1.05] ${className}`}
        style={{ zIndex: 0, opacity: 0 }}
      >
        <source src={src} type="video/mp4" />
      </video>
      {/* Radial vignette for text legibility */}
      <div
        className="absolute inset-0 z-[1]"
        style={{
          background: "radial-gradient(ellipse 75% 65% at 50% 45%, rgba(0,0,0,0.6) 0%, transparent 70%)",
        }}
      />
      {/* Top/bottom gradient */}
      <div className={`absolute inset-0 z-[1] bg-gradient-to-b ${overlayClass}`} />
    </>
  );
}
