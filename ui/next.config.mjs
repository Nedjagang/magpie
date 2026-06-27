/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  // Emit a self-contained server bundle so the runtime Docker image can ship
  // just a node process + the trimmed node_modules, no pnpm install at boot.
  output: "standalone",
};

export default nextConfig;
