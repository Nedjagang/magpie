export function Footer() {
  return (
    <footer className="border-t border-white/[0.04] px-6 md:px-20 py-14">
      <div className="max-w-[1200px] mx-auto flex flex-col md:flex-row justify-between items-start md:items-center gap-8">
        <div className="flex items-center gap-3">
          <svg width="20" height="20" viewBox="0 0 24 24" fill="#fff" className="opacity-60">
            <path d="M22 7.4l-3.1.5c-.5-1.3-1.7-2.2-3.3-2.2-1.9 0-3.2 1.2-3.6 2.8L3 17.2c-.3.3-.1.8.3.7l4.2-.9c.3 1.6 1.6 2.9 3.6 2.9 1.4 0 2.5-.6 3.1-1.6.5.9 1.4 1.5 2.5 1.5v-1.5c-1 0-1.7-.7-1.7-1.8 0-.6.2-1 .6-1.5l3.7-4.3-2.6.5c-.1-.5-.4-1-.7-1.3L22 7.4z" />
            <circle cx="16.3" cy="8.4" r="0.85" fill="#000" />
          </svg>
          <span className="font-[family-name:var(--font-space-grotesk)] text-[14px] font-medium text-white/50 tracking-tight">
            Magpie
          </span>
          <span className="text-[11px] text-white/20 ml-2">
            Fleet-scale OTel management
          </span>
        </div>

        <div className="flex flex-wrap gap-8">
          <FooterLink href="https://github.com/Nedjagang/magpie" label="GitHub" external />
          <FooterLink href="#platform" label="Platform" />
          <FooterLink href="#why" label="Why Magpie" />
          <FooterLink href="#early-access" label="Early Access" />
        </div>

        <div className="text-[11px] text-white/20 font-light">
          Apache 2.0 &middot; Built for operators, not vendors.
        </div>
      </div>
    </footer>
  );
}

function FooterLink({
  href,
  label,
  external,
}: {
  href: string;
  label: string;
  external?: boolean;
}) {
  return (
    <a
      href={href}
      {...(external ? { target: "_blank", rel: "noopener noreferrer" } : {})}
      className="text-[12px] text-white/35 hover:text-white/70 transition-colors duration-200"
    >
      {label}
    </a>
  );
}
