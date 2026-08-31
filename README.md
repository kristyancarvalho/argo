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

[network]
pause_on_metered = false
resume_after_metered = false

[qos]
policy = "off"
```

Configuration is loaded at daemon startup. Restart `argod` after editing the file.

## License

GPL-3.0-or-later.
