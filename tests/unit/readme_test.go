package unit_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestReadmeHasPublicProjectSectionsAndCommands(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(content)
	sections := []string{
		"# Argo",
		"## Table of contents",
		"## Overview",
		"## Why Argo exists",
		"## Features",
		"## Architecture",
		"## Build and installation",
		"## Quick start",
		"## CLI overview",
		"## Configuration",
		"## Project status",
		"## Development workflow",
		"## Testing",
		"## Repository structure",
		"## License",
	}
	for _, section := range sections {
		if !strings.Contains(readme, section) {
			t.Fatalf("README does not contain %q", section)
		}
	}
	for _, command := range []string{
		"argo add <url>",
		"argo list",
		"argo show <id>",
		"argo pause <id>",
		"argo resume <id>",
		"argo cancel <id>",
		"argo remove <id>",
		"argo clear",
		"argo retry <id>",
		"argo priority <id> <level>",
		"argo watch",
		"argo status",
		"argo policy <name>",
		"argo profile <name>",
		"argo tui",
		"argo help",
	} {
		if !strings.Contains(readme, command) {
			t.Fatalf("README does not document %q", command)
		}
	}
	for _, status := range []string{"early-stage", "pre-1.0", "Linux-first"} {
		if !strings.Contains(readme, status) {
			t.Fatalf("README does not state %q", status)
		}
	}
}

func TestReadmeBrandingAndLocalLinksResolve(t *testing.T) {
	root := repositoryRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(content)
	for _, asset := range []string{
		"assets/branding/banner/argo-banner.svg",
		"assets/branding/widgets/status-early-development.svg",
		"assets/branding/widgets/stage-pre-1-0.svg",
		"assets/branding/widgets/language-go.svg",
		"assets/branding/widgets/platform-linux.svg",
		"assets/branding/widgets/license-gpl3.svg",
	} {
		if !strings.Contains(readme, asset) {
			t.Fatalf("README does not embed %q", asset)
		}
	}
	if !strings.Contains(readme, "actions/workflows/ci.yml/badge.svg?branch=main") {
		t.Fatal("README does not contain the live main-branch CI badge")
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?:src|href)="([^"]+)"`),
		regexp.MustCompile(`\[[^]]*\]\(([^)]+)\)`),
	}
	for _, pattern := range patterns {
		for _, match := range pattern.FindAllStringSubmatch(readme, -1) {
			target := match[1]
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "#") {
				continue
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(target))); err != nil {
				t.Fatalf("local README target %q does not resolve: %v", target, err)
			}
		}
	}
}

func TestReadmeUsesStructuredArchitectureAndRepositoryLayout(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(content)
	for _, required := range []string{
		"```mermaid\nflowchart TD",
		"Client[\"argo CLI / TUI\"]",
		"Daemon[\"argod\"]",
		"Helper[\"argo-qosd\"]",
		"NFT[\"nftables / conntrack\"]",
		"TC[\"tc / IFB\"]",
		"| Path | Purpose |",
		"| `assets/branding/` |",
		"| `tests/e2e/` |",
	} {
		if !strings.Contains(readme, required) {
			t.Fatalf("README does not contain structured content %q", required)
		}
	}
	if strings.Contains(readme, "BRAND"+".md") {
		t.Fatal("README still references the removed public brand guide")
	}
	blocks := regexp.MustCompile("(?s)```([^\\n]*)\\n(.*?)```").FindAllStringSubmatch(readme, -1)
	for _, block := range blocks {
		switch strings.TrimSpace(block[1]) {
		case "sh", "toml", "mermaid":
		default:
			t.Fatalf("README contains a non-command diagrammatic code block with language %q", block[1])
		}
	}
}
