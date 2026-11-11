import { SWRProvider } from "@/lib/swr";
import { TooltipProvider } from "@/components/molecules/Tooltip";
import type { Metadata } from "next";
import { Geist } from "next/font/google";

import "./globals.css";

const geist = Geist({
  subsets: ["latin"],
  display: "swap",
  variable: "--font-geist",
});

export const metadata: Metadata = {
  title: "AI& 🔗",
  description: "Router dashboard and settings",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" className={geist.variable}>
      <body className="h-screen overflow-hidden font-sans antialiased">
        <SWRProvider>
          <TooltipProvider>{children}</TooltipProvider>
        </SWRProvider>
      </body>
    </html>
  );
}
