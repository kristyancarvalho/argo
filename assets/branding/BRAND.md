# Argo Brand Guide

Argo's identity combines a vessel, an abstract letter A, and a network route. The result is intentionally technical, minimal, and legible at terminal-adjacent sizes.

## Palette

| Name | Hex | Use |
| --- | --- | --- |
| Midnight | `#071A33` | Primary dark background |
| Deep ocean | `#0B2A4A` | Secondary background |
| Ocean | `#0E5AA7` | Structural blue |
| Signal | `#1687E8` | Primary accent |
| Sky | `#65B8FF` | Highlights and routes |
| Ice | `#D8EEFF` | Secondary foreground |
| White | `#F7FBFF` | Primary foreground |

## Logo usage

- Use `logo/argo-logo-mark.svg` for square icons and compact placements.
- Use `logo/argo-logo-horizontal.svg` when the name must remain visible.
- Use `logo/argo-logo.svg` for centered or stacked compositions.
- Keep clear space around the mark equal to at least one eighth of its width.
- Do not recolor individual elements, stretch the logo, or place it over visually busy imagery.

The SVG sources use broadly available sans-serif fallbacks and remain the canonical editable assets. PNG exports are supplied for platforms that do not accept SVG.

## Asset inventory

- `logo/`: canonical marks, wordmarks, avatar, and raster exports
- `banner/`: README and social-preview compositions
- `widgets/`: repository status badges
- `previews/`: presentation samples on dark backgrounds

Run `./assets/branding/export.sh` from the repository root to regenerate all raster exports. The script requires `rsvg-convert`.
