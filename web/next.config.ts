import type { NextConfig } from "next";
import { PHASE_DEVELOPMENT_SERVER } from "next/constants";

/**
 * Production is a static export that vmservice serves at `/`, on the same origin as its API: the
 * session cookie goes with every call. `next dev` has no such origin, so it forwards the API, the
 * sign-in routes and /config.json to the server named by VMSERVICE_DEV (see the README). Websockets
 * (the consoles) are not forwarded by `next dev`; try them against the real server.
 */
const developmentBackend = process.env.VMSERVICE_DEV ?? "http://127.0.0.1:8080";

const nextConfig = (phase: string): NextConfig => ({
  trailingSlash: true,
  ...(phase === PHASE_DEVELOPMENT_SERVER
    ? {
        agentRules: false,
        rewrites: async () => [
          { source: "/api/:path*", destination: `${developmentBackend}/api/:path*` },
          { source: "/auth/:path*", destination: `${developmentBackend}/auth/:path*` },
          { source: "/config.json", destination: `${developmentBackend}/config.json` },
        ],
      }
    : { output: "export", images: { unoptimized: true } }),
});

export default nextConfig;
