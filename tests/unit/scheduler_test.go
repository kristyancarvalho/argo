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
	queue := scheduler.NewQueue([]scheduler.Entry{
		{ID: first, Priority: model.PriorityNormal},
		{ID: second, Priority: model.PriorityNormal},
	})
	if !queue.Enqueue(scheduler.Entry{ID: third, Priority: model.PriorityNormal}) {
		t.Fatal("third download was not enqueued")
	}
	if queue.Enqueue(scheduler.Entry{ID: second, Priority: model.PriorityHigh}) {
		t.Fatal("duplicate pending download was enqueued")
	}

	assertNextDownload(t, queue, first)
	if !queue.Enqueue(scheduler.Entry{ID: first, Priority: model.PriorityNormal}) {
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

func TestSchedulerOrdersPriorityStably(t *testing.T) {
	queue := scheduler.NewQueue([]scheduler.Entry{
		{ID: "low", Priority: model.PriorityLow},
		{ID: "normal-one", Priority: model.PriorityNormal},
		{ID: "high-one", Priority: model.PriorityHigh},
		{ID: "high-two", Priority: model.PriorityHigh},
		{ID: "normal-two", Priority: model.PriorityNormal},
	})
	for _, expected := range []model.DownloadID{"high-one", "high-two", "normal-one", "normal-two", "low"} {
		assertNextDownload(t, queue, expected)
	}
}

func TestSchedulerUpdatesPendingPriority(t *testing.T) {
	queue := scheduler.NewQueue([]scheduler.Entry{
		{ID: "first", Priority: model.PriorityNormal},
		{ID: "second", Priority: model.PriorityLow},
	})
	queue.UpdatePriority("second", model.PriorityHigh)
	assertNextDownload(t, queue, "second")
	assertNextDownload(t, queue, "first")
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
