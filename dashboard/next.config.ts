import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  transpilePackages: ["@heroui-pro/react"],
  // The dev server binds 127.0.0.1 (see package.json); allow both loopback names.
  allowedDevOrigins: ["127.0.0.1", "localhost"],
};

export default nextConfig;
