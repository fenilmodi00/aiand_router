import containerQueries from "@tailwindcss/container-queries";
import type { Config } from "tailwindcss";
import tailwindcssAnimate from "tailwindcss-animate";

const config: Config = {
  content: [
    "./src/components/**/*.{js,ts,jsx,tsx,mdx}",
    "./src/app/**/*.{js,ts,jsx,tsx,mdx}",
  ],
  plugins: [tailwindcssAnimate, containerQueries],
  theme: {
    colors: {
      transparent: "transparent",
      accent: {
        DEFAULT: "hsl(var(--accent))",
        foreground: "hsl(var(--accent-foreground))",
      },
      background: "hsl(var(--background))",
      border: "hsl(var(--border))",
      "border-darker": "hsl(var(--border-darker))",
      brand: "hsl(var(--brand))",
      card: {
        DEFAULT: "hsl(var(--card))",
        foreground: "hsl(var(--card-foreground))",
      },
      danger: {
        DEFAULT: "hsl(var(--danger))",
        foreground: "hsl(var(--danger-foreground))",
      },
      foreground: "hsl(var(--foreground))",
      input: "hsl(var(--input))",
      muted: {
        DEFAULT: "hsl(var(--muted))",
        foreground: "hsl(var(--muted-foreground))",
      },
      popover: {
        DEFAULT: "hsl(var(--popover))",
        foreground: "hsl(var(--popover-foreground))",
      },
      primary: {
        DEFAULT: "hsl(var(--primary))",
        foreground: "hsl(var(--primary-foreground))",
      },
      ring: "hsl(var(--ring))",
      success: {
        DEFAULT: "hsl(var(--success))",
        foreground: "hsl(var(--success-foreground))",
      },
      tooltip: {
        DEFAULT: "hsl(var(--tooltip))",
        foreground: "hsl(var(--tooltip-foreground))",
      },
      warning: {
        DEFAULT: "hsl(var(--warning))",
        foreground: "hsl(var(--warning-foreground))",
      },
    },
    extend: {
      fontFamily: {
        sans: ["var(--font-geist)", "Geist", "system-ui", "sans-serif"],
      },
      fontSize: {
        "2xs": ["0.75rem", "0.875rem"],
        xs: ["0.8125rem", "1rem"],
      },
      borderRadius: {
        lg: "0.625rem",
        md: "0.5rem",
        sm: "0.375rem",
        xl: "0.75rem",
        "2xl": "1rem",
        full: "9999px",
      },
      spacing: {
        "content-width": "96rem",
        "text-width": "var(--text-width)",
      },
      keyframes: {
        "fade-in": {
          from: { opacity: "0" },
          to: { opacity: "1" },
        },
        slide: {
          to: { transform: "translateX(100%)" },
        },
      },
      animation: {
        "fade-in": "fade-in 0.25s ease-out",
        "slide-infinite": "slide 2s infinite",
      },
    },
  },
};

export default config;
