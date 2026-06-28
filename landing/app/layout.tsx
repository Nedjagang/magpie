import type { Metadata } from "next";
import { Barlow, Space_Grotesk } from "next/font/google";
import "./globals.css";

const barlow = Barlow({
  variable: "--font-barlow",
  subsets: ["latin"],
  weight: ["300", "400", "500", "600", "700"],
});

const spaceGrotesk = Space_Grotesk({
  variable: "--font-space-grotesk",
  subsets: ["latin"],
  weight: ["400", "500", "600", "700"],
});

export const metadata: Metadata = {
  title: "Magpie — One config push. Every collector. Two seconds.",
  description:
    "Magpie is a control plane for OpenTelemetry. Publish a pipeline config once — every matching host hot-reloads in ~2s. No SSH. No Ansible. No forked agent.",
  openGraph: {
    title: "Magpie — Fleet-wide OTel config in 2 seconds",
    description:
      "Control plane for OpenTelemetry. One config push reaches every collector. No SSH, no Ansible, no custom DSL.",
    type: "website",
  },
  twitter: {
    card: "summary_large_image",
  },
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html
      lang="en"
      className={`${barlow.variable} ${spaceGrotesk.variable} h-full antialiased`}
    >
      <body className="min-h-full">{children}</body>
    </html>
  );
}
