# Docker-Sentinel — Visual Design Brief

> Give this to an image generation AI (Gemini, etc.) to create backgrounds and favicons.

## What is Docker-Sentinel?

A self-hosted Docker container monitoring and auto-update dashboard. Think "mission control for your homelab" — it watches all your Docker containers, checks registries for updates, and lets you approve/auto-apply them. The UI is a single-page dashboard showing container status, update queues, and system health.

## Design System: Material Design 3 (Purple Baseline)

The entire UI follows Google's Material Design 3 specification with a **purple primary colour** and tinted surfaces. Both light and dark modes are supported.

## Colour Palette

### Primary (Purple)
| Role | Light Mode | Dark Mode |
|------|-----------|-----------|
| Primary | `#6750A4` | `#D0BCFF` |
| On Primary | `#FFFFFF` | `#381E72` |
| Primary Container | `#EADDFF` | `#4F378B` |
| On Primary Container | `#21005D` | `#EADDFF` |

### Secondary (Desaturated Purple)
| Role | Light Mode | Dark Mode |
|------|-----------|-----------|
| Secondary | `#625B71` | `#CCC2DC` |
| Secondary Container | `#E8DEF8` | `#4A4458` |

### Tertiary (Rose)
| Role | Light Mode | Dark Mode |
|------|-----------|-----------|
| Tertiary | `#7D5260` | `#EFB8C8` |
| Tertiary Container | `#FFD8E4` | `#633B48` |

### Surfaces
| Role | Light Mode | Dark Mode |
|------|-----------|-----------|
| Surface | `#FEF7FF` | `#141218` |
| Surface Container | `#F3EDF7` | `#211F26` |
| Surface Container High | `#ECE6F0` | `#2B2930` |

### Status Colours
| Status | Light Mode | Dark Mode |
|--------|-----------|-----------|
| Success (Green) | `#2E7D32` | `#81C784` |
| Warning (Amber) | `#E65100` | `#FFD54F` |
| Error (Red) | `#B3261E` | `#F2B8B5` |
| Info (Blue) | `#1565C0` | `#82B1FF` |

### Key Accent
- **Hover accent:** `#7F67BE` (light) / `#B69DF8` (dark)

## Typography
- **Sans:** Roboto (400, 500, 700)
- **Mono:** Roboto Mono (400, 500)

## Current Brand Mark
- The nav bar shows a small circle with the letter **"S"** in Roboto Bold on a `#6750A4` purple background with white text
- Brand text: **"Sentinel"** in Roboto Bold

## Mood & Personality

### Keywords
- **Watchful** — a silent guardian monitoring your infrastructure
- **Calm confidence** — everything is under control
- **Technical precision** — clean data, clear status indicators
- **Purple depth** — rich, layered, not flat
- **Homelab warmth** — approachable, not corporate

### Avoid
- Aggressive/alarming imagery (this isn't a security threat tool)
- Overly corporate/sterile looks
- Neon or cyberpunk aesthetics
- Busy/cluttered patterns

### Think
- A calm command centre at twilight
- Deep purple gradients with subtle texture
- Container/shipping metaphors (stacked boxes, harbours) rendered abstractly
- Shield or eye motifs (watching, protecting)
- Soft glows and ambient lighting rather than hard edges

## What I Need

### 1. Favicon (SVG preferred)

**Requirements:**
- Must be recognisable at 16x16, 32x32, and 192x192
- Works on both light and dark browser chrome
- SVG format preferred (site already references `/favicon.svg`)
- Also need a 192x192 PNG for PWA manifest

**Concept directions (pick one or propose):**
- **Shield + S** — A shield shape with the letter S, in primary purple (`#6750A4`) with a white or light purple interior
- **Eye/radar** — A stylised eye or radar sweep incorporating the S, suggesting watchfulness
- **Container sentinel** — A minimalist Docker container (box) with a small shield or eye accent
- **Abstract S** — A geometric/modern S mark that feels protective and technical

**Colour constraints:**
- Primary fill: `#6750A4` (the M3 purple)
- Accent/detail: `#D0BCFF` (light purple) or `#FFFFFF`
- Must have enough contrast on both white and dark backgrounds

### 2. Login Page Background

**Requirements:**
- Subtle, non-distracting pattern or gradient
- Must work behind a centred white/dark login card (400px wide)
- Needs to tile or stretch gracefully at any viewport size
- Should feel premium but not heavy

**Concept directions:**
- **Purple gradient mesh** — Soft, flowing gradients between `#141218` (dark surface), `#4F378B` (primary container dark), and `#381E72` (on-primary dark). Subtle, ambient, like twilight
- **Tinted geometric** — Very low-opacity geometric shapes (hexagons, containers, network nodes) on a deep purple-black background
- **Radial glow** — A single soft radial glow of `#6750A4` at ~15% opacity centred on the page, fading to `#141218`

**Colour constraints:**
- Background must be dark (the login page uses the dark surface `#141218` to `#211F26` range)
- Any pattern should be very subtle (5-15% opacity)
- The login card itself is `#1D1B20` (surface-container-low dark) with a `#49454F` border

### 3. Dashboard Subtle Background (Optional)

**Requirements:**
- Even more subtle than the login background
- Cannot interfere with table readability
- Could be a very faint watermark or gradient in the page footer area

## File Formats Needed

| Asset | Format | Sizes |
|-------|--------|-------|
| Favicon | SVG | Scalable |
| Favicon | PNG | 16x16, 32x32, 192x192 |
| Login background | SVG or PNG | 1920x1080 minimum, tileable preferred |
| Dashboard background | SVG or CSS gradient | Full-width, optional |

## Reference Screenshots

The dashboard uses:
- A sticky top nav bar (56px) with purple accent underline on active tab
- Card-based layout with rounded corners (8-12px radius)
- Status pills: green for running, grey for stopped, purple for updatable
- Accordion rows that expand to show container details
- Filter pill bar with toggle buttons
