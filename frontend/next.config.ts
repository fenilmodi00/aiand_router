import type { NextConfig } from "next";

const isDev = process.env.NODE_ENV === "development";
const ROUTER_DEV_TARGET = process.env.ROUTER_DEV_TARGET ?? "http://localhost:8080";

const nextConfig: NextConfig = {
  // Static export for production; dev server uses local .next so the runtime
  // can resolve next/* modules from frontend/node_modules.
  ...(isDev
    ? {
        async rewrites() {
          // Proxy dashboard API calls to the Go router so it can serve
          // /admin/v1/* and /account/v1/* without CORS or absolute URLs.
          return [
            {
              source: "/admin/:path*",
              destination: `${ROUTER_DEV_TARGET}/admin/:path*`,
              basePath: false,
            },
            {
              source: "/account/:path*",
              destination: `${ROUTER_DEV_TARGET}/account/:path*`,
              basePath: false,
            },
          ];
        },
        // Bare-root convenience redirect so visiting localhost:3000
        // lands on the dashboard. In production the Go server does
        // this; in `next dev` requests hit Next directly and never
        // reach the Go backend, so we replicate it here.
        async redirects() {
          // Legacy /settings paths → their new top-level pages (see
          // docs/plans/2026-11-07-sidebar-navigation-migration.md). Next
          // prefixes both source and destination with the basePath (/ui);
          // production equivalents live in the Go static handler.
          return [
            {
              source: "/",
              destination: "/ui/",
              basePath: false,
              permanent: false,
            },
            {
              source: "/settings",
              destination: "/keys",
              permanent: false,
            },
            {
              source: "/settings/providers",
              destination: "/account",
              permanent: false,
            },
            {
              source: "/settings/models",
              destination: "/models",
              permanent: false,
            },
          ];
        },
      }
    : {
        // Static export → frontend/out/ (Next.js default). The Dockerfile
        // copies that into the Go server's assets/ui at the next stage.
        // Keep distDir at its default (`.next`) so we don't write outside
        // the project directory, which newer Next.js versions reject.
        output: "export",
      }),
  basePath: "/ui",
  devIndicators: false,
  experimental: {
    // Client Router Cache: reuse page segments on back/forward and Link
    // navigation so switching pages doesn't rebuild the RSC tree. Data
    // itself is cached by SWR (src/lib/apiHooks.ts); this keeps the fetched
    // segments and layouts reusable. The app is a static export, so the
    // `static` bucket governs; dynamic: 30 restores the Next 14 default.
    staleTimes: {
      dynamic: 30,
      static: 180,
    },
  },
};

export default nextConfig;
