package scheduler

import "github.com/kristyancarvalho/argo/internal/model"

type Queue struct {
	pending []model.DownloadID
	queued  map[model.DownloadID]struct{}
	active  map[model.DownloadID]struct{}
}

func NewQueue(initial []model.DownloadID) *Queue {
	queue := &Queue{
		pending: make([]model.DownloadID, 0, len(initial)),
		queued:  make(map[model.DownloadID]struct{}, len(initial)),
		active:  make(map[model.DownloadID]struct{}),
	}
	for _, identifier := range initial {
		queue.Enqueue(identifier)
	}

	return queue
}

func (queue *Queue) Enqueue(identifier model.DownloadID) bool {
	if _, exists := queue.queued[identifier]; exists {
		return false
	}
	queue.pending = append(queue.pending, identifier)
	queue.queued[identifier] = struct{}{}

	return true
}

func (queue *Queue) Next() (model.DownloadID, bool) {
	for index, identifier := range queue.pending {
		if _, active := queue.active[identifier]; active {
			continue
		}
		queue.pending = append(queue.pending[:index], queue.pending[index+1:]...)
		delete(queue.queued, identifier)
		queue.active[identifier] = struct{}{}

		return identifier, true
	}

	return "", false
}

func (queue *Queue) Complete(identifier model.DownloadID) {
	delete(queue.active, identifier)
}

func (queue *Queue) Active() int {
	return len(queue.active)
}
