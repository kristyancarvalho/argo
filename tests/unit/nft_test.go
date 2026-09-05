package unit_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/kristyancarvalho/argo/internal/qos"
	"github.com/kristyancarvalho/argo/internal/qos/nft"
)

func TestNftablesClassificationRuleset(t *testing.T) {
	plan, err := qos.GenerateClassification("wlan0", qos.CgroupSelector{
		Path: "user.slice/argo.service", Level: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	ruleset, err := nft.Ruleset(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"table inet argo",
		"chain output",
		"type route hook output priority mangle; policy accept;",
		`oifname "wlan0" socket cgroupv2 level 2 "user.slice/argo.service"`,
		"counter",
		"meta mark set ((meta mark & 0xffff0000) | 0x0000a400)",
	} {
		if !strings.Contains(ruleset, expected) {
			t.Fatalf("ruleset %q does not contain %q", ruleset, expected)
		}
	}
	if strings.Contains(ruleset, "default") {
		t.Fatal("ruleset creates a per-application default rule")
	}
}

func TestNftablesRejectsInvalidInputsAndMissingExecutable(t *testing.T) {
	if _, err := nft.Ruleset(qos.ClassificationPlan{}); err == nil {
		t.Fatal("disabled classification produced a ruleset")
	}
	if _, err := nft.NewWithRunner(nil); err == nil {
		t.Fatal("nil nftables runner succeeded")
	}
	_, err := nft.NewWithExecutable("argo-nft-command-that-does-not-exist")
	var unavailable nft.UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("missing nft executable returned %v", err)
	}
}
