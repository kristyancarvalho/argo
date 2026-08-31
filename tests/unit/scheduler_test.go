package unit_test

import (
	"testing"

	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/scheduler"
)

func TestSchedulerQueueOrderingAndActiveDeferral(t *testing.T) {
	first := model.DownloadID("first")
	second := model.DownloadID("second")
	third := model.DownloadID("third")
	queue := scheduler.NewQueue([]model.DownloadID{first, second})
	if !queue.Enqueue(third) {
		t.Fatal("third download was not enqueued")
	}
	if queue.Enqueue(second) {
		t.Fatal("duplicate pending download was enqueued")
	}

	assertNextDownload(t, queue, first)
	if !queue.Enqueue(first) {
		t.Fatal("active download was not deferred for rescheduling")
	}
	assertNextDownload(t, queue, second)
	assertNextDownload(t, queue, third)
	if _, available := queue.Next(); available {
		t.Fatal("active download was scheduled a second time")
	}
	queue.Complete(first)
	assertNextDownload(t, queue, first)
}

func assertNextDownload(t *testing.T, queue *scheduler.Queue, expected model.DownloadID) {
	t.Helper()
	actual, available := queue.Next()
	if !available {
		t.Fatalf("expected download %s, queue was empty", expected)
	}
	if actual != expected {
		t.Fatalf("next download is %s, expected %s", actual, expected)
	}
}
