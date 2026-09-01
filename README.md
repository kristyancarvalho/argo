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
directory = "~/Downloads"
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
latency_target = "20ms"
probe_target = ""
probe_timeout = "1s"
sample_interval = "1s"

[profiles.gaming]
download_limit = "30M"
default_priority = "high"
max_concurrent_downloads = 2
pause_on_metered = true
resume_after_metered = true
policy = "latency"
latency_target = "15ms"
min_rate = "10M"
max_rate = "80M"
```

Configuration is loaded at daemon startup. Restart `argod` after editing the file.
Completed downloads default to `$HOME/Downloads`; `download.directory` accepts an absolute path or a path beginning with `~/`. Argo keeps resumable partial data separately under `$XDG_STATE_HOME/argo/parts`, falling back to `$HOME/.local/state/argo/parts`, and never derives either location from the CLI working directory.
Select a configured profile at runtime with `argo profile <name>`. Select system traffic shaping with `argo policy <off|balanced|throughput|latency|focus>`. Download rates use bytes per second, while `qos.link_rate`, `min_rate`, and `max_rate` use bits per second. `K`, `M`, and `G` are decimal suffixes. Latency policy profiles can override the acceptable latency increase and rate bounds. Set `qos.probe_target` to a safe `host:port` endpoint to collect TCP-connect latency; an empty target produces missing telemetry and the bounded fallback behavior. The active profile is persisted across daemon restarts.

Download priority only orders queued transfers managed by Argo. It does not shape packets or change an already active transfer. Traffic policies divide guaranteed link capacity between Argo and the default class: `focus` reserves 20% for Argo to protect system responsiveness, `balanced` reserves 50%, and `throughput` reserves 80%. Unused capacity can be borrowed by either class. `latency` adjusts Argo's limit from live latency measurements.

QoS is applied only while downloads are active and requires a nonzero `qos.link_rate`, a connected interface, and the privileged `argo-qosd` service. `argo status` reports whether shaping is active and shows the latest application error. Other applications, including download managers such as FDM, remain in the default class unless they run in the exact same cgroup as `argod`; using the packaged systemd services gives `argod` its own cgroup.

To verify a live setup, check `systemctl --user status argod.service`, `systemctl status argo-qosd@$(id -un).service`, and `argo status`. Kernel state is visible with `sudo tc -s class show dev <interface>` and `sudo nft list table inet argo` while an Argo download is active.

Run `argo tui` for the optional terminal interface. It connects to the existing user daemon, and exiting the interface does not stop active downloads.

Run `argo --help` to list commands and explain the difference between download priority and system traffic policies. Argo uses colors when writing to a terminal; set `NO_COLOR=1` to disable them.

`argo resume <id>` continues valid partial state with the same transfer identity. `argo retry <id>` creates a new transfer from completed, failed, or canceled history, preserving the original record. Repeated URLs always create new IDs, and existing destination names receive a deterministic numeric suffix instead of being overwritten.

## systemd

Install `packaging/systemd/argod.service` under the user unit directory and `packaging/systemd/argo-qosd@.service` under the system unit directory. Start the downloader for the current user with `systemctl --user enable --now argod.service`.

The QoS helper is a system service template. Start exactly one instance for the account running `argod`, for example `systemctl enable --now argo-qosd@alice.service`. The helper runs as that account with only `CAP_NET_ADMIN`; `argod` remains unprivileged. It owns the Argo nftables table and traffic-control tree used by active traffic policies.

## Development

Run the complete local CI-equivalent suite with `make check`. Individual targets include `format`, `format-check`, `vet`, `lint`, `test-unit`, `test-integration`, `test-e2e`, `test-race`, and `build`.

Run `make run` to compile all executables into `bin/` and launch `argod` for local testing. Stop it with Ctrl+C. Pass daemon options with `RUN_ARGS`, for example `make run RUN_ARGS="-rate-limit 1000000"`.

## License

GPL-3.0-or-later.
