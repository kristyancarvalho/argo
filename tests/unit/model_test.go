package unit_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/kristyancarvalho/argo/internal/model"
)

func TestValidDownloadTransitions(t *testing.T) {
	valid := []struct {
		from model.Status
		to   model.Status
	}{
		{model.StatusQueued, model.StatusResolving},
		{model.StatusQueued, model.StatusDownloading},
		{model.StatusQueued, model.StatusPaused},
		{model.StatusQueued, model.StatusCanceled},
		{model.StatusResolving, model.StatusDownloading},
		{model.StatusResolving, model.StatusPaused},
		{model.StatusResolving, model.StatusFailed},
		{model.StatusResolving, model.StatusCanceled},
		{model.StatusDownloading, model.StatusPaused},
		{model.StatusDownloading, model.StatusVerifying},
		{model.StatusDownloading, model.StatusCompleted},
		{model.StatusDownloading, model.StatusFailed},
		{model.StatusDownloading, model.StatusCanceled},
		{model.StatusVerifying, model.StatusPaused},
		{model.StatusVerifying, model.StatusCompleted},
		{model.StatusVerifying, model.StatusFailed},
		{model.StatusVerifying, model.StatusCanceled},
		{model.StatusPaused, model.StatusDownloading},
		{model.StatusPaused, model.StatusCanceled},
		{model.StatusFailed, model.StatusQueued},
		{model.StatusFailed, model.StatusCanceled},
		{model.StatusCanceled, model.StatusQueued},
	}

	for _, test := range valid {
		t.Run(string(test.from)+"_to_"+string(test.to), func(t *testing.T) {
			if err := model.ValidateTransition(test.from, test.to); err != nil {
				t.Fatalf("expected valid transition: %v", err)
			}
		})
	}
}

func TestInvalidDownloadTransitions(t *testing.T) {
	statuses := []model.Status{
		model.StatusQueued,
		model.StatusResolving,
		model.StatusDownloading,
		model.StatusVerifying,
		model.StatusPaused,
		model.StatusCompleted,
		model.StatusFailed,
		model.StatusCanceled,
	}
	valid := map[[2]model.Status]struct{}{
		{model.StatusQueued, model.StatusResolving}:      {},
		{model.StatusQueued, model.StatusDownloading}:    {},
		{model.StatusQueued, model.StatusPaused}:         {},
		{model.StatusQueued, model.StatusCanceled}:       {},
		{model.StatusResolving, model.StatusDownloading}: {},
		{model.StatusResolving, model.StatusPaused}:      {},
		{model.StatusResolving, model.StatusFailed}:      {},
		{model.StatusResolving, model.StatusCanceled}:    {},
		{model.StatusDownloading, model.StatusPaused}:    {},
		{model.StatusDownloading, model.StatusVerifying}: {},
		{model.StatusDownloading, model.StatusCompleted}: {},
		{model.StatusDownloading, model.StatusFailed}:    {},
		{model.StatusDownloading, model.StatusCanceled}:  {},
		{model.StatusVerifying, model.StatusPaused}:      {},
		{model.StatusVerifying, model.StatusCompleted}:   {},
		{model.StatusVerifying, model.StatusFailed}:      {},
		{model.StatusVerifying, model.StatusCanceled}:    {},
		{model.StatusPaused, model.StatusDownloading}:    {},
		{model.StatusPaused, model.StatusCanceled}:       {},
		{model.StatusFailed, model.StatusQueued}:         {},
		{model.StatusFailed, model.StatusCanceled}:       {},
		{model.StatusCanceled, model.StatusQueued}:       {},
	}

	for _, from := range statuses {
		for _, to := range statuses {
			if _, ok := valid[[2]model.Status{from, to}]; ok {
				continue
			}
			err := model.ValidateTransition(from, to)
			var transitionError model.InvalidTransitionError
			if !errors.As(err, &transitionError) {
				t.Errorf("transition %s to %s returned %T, expected InvalidTransitionError", from, to, err)
			}
		}
	}
}

func TestNormalizeChecksum(t *testing.T) {
	digest := "A3" + strings.Repeat("0f", 31)
	checksum, err := model.NormalizeChecksum("SHA256:" + digest)
	if err != nil {
		t.Fatal(err)
	}
	if checksum != "sha256:"+strings.ToLower(digest) {
		t.Fatalf("normalized checksum is %q", checksum)
	}
	for _, value := range []string{"", "md5:" + strings.Repeat("0", 32), "sha256:abcd", "sha256:" + strings.Repeat("z", 64)} {
		if _, err := model.NormalizeChecksum(value); err == nil {
			t.Errorf("checksum %q was accepted", value)
		}
	}
}

func TestInvalidStatusInTransition(t *testing.T) {
	tests := []struct {
		from model.Status
		to   model.Status
	}{
		{"unknown", model.StatusQueued},
		{model.StatusQueued, "unknown"},
	}

	for _, test := range tests {
		err := model.ValidateTransition(test.from, test.to)
		var statusError model.InvalidStatusError
		if !errors.As(err, &statusError) {
			t.Errorf("ValidateTransition(%q, %q) returned %T, expected InvalidStatusError", test.from, test.to, err)
		}
	}
}

func TestParsePriority(t *testing.T) {
	tests := []struct {
		value    string
		expected model.Priority
	}{
		{"low", model.PriorityLow},
		{"normal", model.PriorityNormal},
		{"high", model.PriorityHigh},
	}

	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			priority, err := model.ParsePriority(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if priority != test.expected {
				t.Fatalf("got %q, expected %q", priority, test.expected)
			}
		})
	}
}

func TestParseInvalidPriority(t *testing.T) {
	for _, value := range []string{"", "NORMAL", "urgent"} {
		t.Run(value, func(t *testing.T) {
			_, err := model.ParsePriority(value)
			var priorityError model.InvalidPriorityError
			if !errors.As(err, &priorityError) {
				t.Fatalf("ParsePriority(%q) returned %T, expected InvalidPriorityError", value, err)
			}
		})
	}
}

func TestDownloadIdentifiers(t *testing.T) {
	id, err := model.NewDownloadID()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := model.ParseDownloadID(id.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsed != id {
		t.Fatalf("parsed identifier %q differs from generated identifier %q", parsed, id)
	}

	for _, value := range []string{"", "not-hex", "abcd"} {
		_, err := model.ParseDownloadID(value)
		var identifierError model.InvalidDownloadIDError
		if !errors.As(err, &identifierError) {
			t.Errorf("ParseDownloadID(%q) returned %T, expected InvalidDownloadIDError", value, err)
		}
	}
}
