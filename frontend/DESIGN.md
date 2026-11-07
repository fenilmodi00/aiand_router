# DESIGN.md: ai& Console

## Source
- URL: https://console.aiand.com
- Capture date: 2026-11-07
- Evidence: Firecrawl branding scrape (console.aiand.com)

## Design Summary
Console uses a minimal, shadcn/Tailwind-based design: near-black primary (#171717), white backgrounds, Geist font, 10px radius, 4px spacing base. No red accent — monochrome. Tone is utilitarian, developer-focused.

## Design Tokens

### Colors
| Role | Hex | Usage |
|---|---|---|
| primary | #171717 | Buttons, headings, links |
| accent | #171717 | Same as primary (monochrome) |
| background | #FFFFFF | Page background |
| textPrimary | #0A0A0A | Body text |
| muted | #F5F5F5 | Muted surfaces |
| muted-foreground | #737373 | Secondary text |
| border | #E5E5E5 | Borders |
| card | #FFFFFF | Card surfaces |

Dark mode: background #0A0A0A, foreground #FAFAFA, primary #FAFAFA, muted #1A1A1A, border #262626.

### Typography
- Font family (all roles): Geist
- Fallback stack: `Geist, system-ui, sans-serif`
- Weights: 400, 500, 600
- Scale:
  - h1/h2: 30px
  - body: 14px
- Line height: ~1.5 body, ~1.2 headings

### Spacing And Layout
- Base unit: 4px
- Border radius: 10px (buttons/inputs/cards), 8px (lg)
- Shadows: none on buttons

### Components
- Button primary: bg #171717, text #FAFAFA, radius 10px, no shadow
- Button secondary: bg #FFFFFF, text #0A0A0A, border #E5E5E5, radius 10px
- Input: transparent bg, text #0A0A0A, border #E5E5E5, radius 10px
- Card: white bg, 1px border #E5E5E5, radius 10px

### Logo
- SVG wordmark "ai&" (72x24 viewBox), monochrome, square (no rounding)
- Source: console.aiand.com/assets/logo-*.svg

## Implementation Notes (frontend)
- Tailwind: Geist font via next/font/google, variable `--font-geist`
- CSS vars in `src/app/globals.css` updated to console tokens
- Radius scale: lg=0.625rem, md=0.5rem, sm=0.375rem
- Logo: `/ui/aiand.svg` (console logo, no rounding)
EOF