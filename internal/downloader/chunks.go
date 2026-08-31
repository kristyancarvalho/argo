package downloader

import "fmt"

const (
	DefaultMaximumChunks          = 4
	DefaultMinimumChunkSize int64 = 1 << 20
)

type Chunk struct {
	Index int
	Start int64
	End   int64
}

func (chunk Chunk) Size() int64 {
	return chunk.End - chunk.Start + 1
}

type ChunkPlanner struct {
	maximumChunks    int
	minimumChunkSize int64
}

func NewChunkPlanner(maximumChunks int, minimumChunkSize int64) (ChunkPlanner, error) {
	if maximumChunks <= 0 {
		return ChunkPlanner{}, InvalidChunkPlanError{Reason: "maximum chunk count must be positive"}
	}
	if minimumChunkSize <= 0 {
		return ChunkPlanner{}, InvalidChunkPlanError{Reason: "minimum chunk size must be positive"}
	}

	return ChunkPlanner{
		maximumChunks:    maximumChunks,
		minimumChunkSize: minimumChunkSize,
	}, nil
}

func DefaultChunkPlanner() ChunkPlanner {
	planner, _ := NewChunkPlanner(DefaultMaximumChunks, DefaultMinimumChunkSize)

	return planner
}

func (planner ChunkPlanner) Plan(totalSize int64, requestedChunks int) ([]Chunk, error) {
	if planner.maximumChunks <= 0 || planner.minimumChunkSize <= 0 {
		return nil, InvalidChunkPlanError{Reason: "chunk planner is not configured"}
	}
	if totalSize <= 0 {
		return nil, InvalidChunkPlanError{Reason: fmt.Sprintf("total size %d must be positive", totalSize)}
	}
	if requestedChunks <= 0 {
		return nil, InvalidChunkPlanError{Reason: "requested chunk count must be positive"}
	}

	chunkCount := min(requestedChunks, planner.maximumChunks)
	maximumBySize := totalSize / planner.minimumChunkSize
	if maximumBySize < 1 {
		maximumBySize = 1
	}
	if int64(chunkCount) > maximumBySize {
		chunkCount = int(maximumBySize)
	}

	baseSize := totalSize / int64(chunkCount)
	remainder := totalSize % int64(chunkCount)
	chunks := make([]Chunk, 0, chunkCount)
	start := int64(0)
	for index := 0; index < chunkCount; index++ {
		size := baseSize
		if int64(index) < remainder {
			size++
		}
		end := start + size - 1
		chunks = append(chunks, Chunk{Index: index, Start: start, End: end})
		start = end + 1
	}

	return chunks, nil
}
