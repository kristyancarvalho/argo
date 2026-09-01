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
link_rate = "0"

[profiles.gaming]
download_limit = "30M"
default_priority = "high"
max_concurrent_downloads = 2
pause_on_metered = true
resume_after_metered = true
policy = "latency"
```

Configuration is loaded at daemon startup. Restart `argod` after editing the file.
Select a configured profile at runtime with `argo profile <name>`. Select system traffic shaping with `argo policy <off|balanced|throughput|focus>`. Download rates use bytes per second, while `qos.link_rate` is the connection capacity in bits per second. `K`, `M`, and `G` are decimal suffixes; a positive link rate is required while shaping active downloads. The active profile is persisted across daemon restarts.

## systemd

Install `packaging/systemd/argod.service` under the user unit directory and `packaging/systemd/argo-qosd@.service` under the system unit directory. Start the downloader for the current user with `systemctl --user enable --now argod.service`.

The QoS helper is a system service template. Start exactly one instance for the account running `argod`, for example `systemctl enable --now argo-qosd@alice.service`. The helper runs as that account with only `CAP_NET_ADMIN`; `argod` remains unprivileged. It owns the Argo nftables table and traffic-control tree used by active traffic policies.

## License

GPL-3.0-or-later.
