# Argo

Argo is a Linux-first download manager and network traffic governor.

## Status

Early development.

## Goals

- Provide reliable daemon-backed HTTP and HTTPS downloads.
- Schedule transfers intentionally with bounded resource usage.
- Protect system responsiveness through optional Linux-native traffic control.
- Remain lightweight, scriptable, and terminal-first.

## Configuration

`argod` reads `$XDG_CONFIG_HOME/argo/config.toml`, falling back to `~/.config/argo/config.toml`.

```toml
[download]
default_priority = "normal"
max_concurrent_downloads = 3
max_chunks_per_download = 4
rate_limit = "0"

[network]
pause_on_metered = false
resume_after_metered = false

[qos]
policy = "off"

[profiles.gaming]
download_limit = "30M"
default_priority = "high"
max_concurrent_downloads = 2
pause_on_metered = true
resume_after_metered = true
policy = "latency"
```

Configuration is loaded at daemon startup. Restart `argod` after editing the file.
Select a configured profile at runtime with `argo profile <name>`. `0` means unlimited bandwidth, while `K`, `M`, and `G` are decimal byte-rate suffixes. The active profile is persisted across daemon restarts.

## License

GPL-3.0-or-later.
