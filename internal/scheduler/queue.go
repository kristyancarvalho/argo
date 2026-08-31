package scheduler

import "github.com/kristyancarvalho/argo/internal/model"

type Entry struct {
	ID       model.DownloadID
	Priority model.Priority
}

type Queue struct {
	pending []Entry
	queued  map[model.DownloadID]struct{}
	active  map[model.DownloadID]struct{}
}

func NewQueue(initial []Entry) *Queue {
	queue := &Queue{
		pending: make([]Entry, 0, len(initial)),
		queued:  make(map[model.DownloadID]struct{}, len(initial)),
		active:  make(map[model.DownloadID]struct{}),
	}
	for _, entry := range initial {
		queue.Enqueue(entry)
	}

	return queue
}

func (queue *Queue) Enqueue(entry Entry) bool {
	if _, exists := queue.queued[entry.ID]; exists {
		return false
	}
	queue.pending = append(queue.pending, entry)
	queue.queued[entry.ID] = struct{}{}

	return true
}

func (queue *Queue) UpdatePriority(identifier model.DownloadID, priority model.Priority) {
	for index := range queue.pending {
		if queue.pending[index].ID == identifier {
			queue.pending[index].Priority = priority
		}
	}
}

func (queue *Queue) Next() (model.DownloadID, bool) {
	selected := -1
	for index, entry := range queue.pending {
		if _, active := queue.active[entry.ID]; active {
			continue
		}
		if selected < 0 || priorityRank(entry.Priority) > priorityRank(queue.pending[selected].Priority) {
			selected = index
		}
	}
	if selected < 0 {
		return "", false
	}
	identifier := queue.pending[selected].ID
	queue.pending = append(queue.pending[:selected], queue.pending[selected+1:]...)
	delete(queue.queued, identifier)
	queue.active[identifier] = struct{}{}

	return identifier, true
}

func (queue *Queue) Complete(identifier model.DownloadID) {
	delete(queue.active, identifier)
}

func (queue *Queue) Active() int {
	return len(queue.active)
}

func priorityRank(priority model.Priority) int {
	switch priority {
	case model.PriorityHigh:
		return 2
	case model.PriorityNormal:
		return 1
	case model.PriorityLow:
		return 0
	default:
		return -1
	}
}
