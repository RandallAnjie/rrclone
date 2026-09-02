import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  transpilePackages: ["@heroui-pro/react"],
  // Local verification uses http://127.0.0.1:3000 against a 0.0.0.0 bind.
  allowedDevOrigins: ["127.0.0.1", "localhost"],
};

export default nextConfig;
