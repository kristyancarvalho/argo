#!/usr/bin/env sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
mark="$root/logo/argo-logo-mark.svg"

for size in 32 64 128 256 512 1024; do
  rsvg-convert --width "$size" --height "$size" "$mark" --output "$root/logo/argo-logo-$size.png"
done

rsvg-convert --width 1024 --height 1024 "$mark" --output "$root/logo/argo-avatar.png"
rsvg-convert --width 720 --height 600 "$root/logo/argo-logo.svg" --output "$root/logo/argo-logo.png"
rsvg-convert --width 1040 --height 320 "$root/logo/argo-logo-horizontal.svg" --output "$root/logo/argo-logo-horizontal.png"
rsvg-convert --width 1440 --height 480 "$root/banner/argo-banner.svg" --output "$root/banner/argo-banner.png"
rsvg-convert --width 1280 --height 640 "$root/banner/argo-social-preview.svg" --output "$root/banner/argo-social-preview.png"
rsvg-convert --width 1200 --height 600 "$root/previews/argo-logo-dark.svg" --output "$root/previews/argo-logo-dark.png"
