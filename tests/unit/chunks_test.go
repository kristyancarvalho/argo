package unit_test

import (
	"errors"
	"math"
	"testing"

	"github.com/kristyancarvalho/argo/internal/downloader"
)

func TestChunkPlanExactDivision(t *testing.T) {
	planner := chunkPlanner(t, 8, 1)
	chunks, err := planner.Plan(100, 4)
	if err != nil {
		t.Fatal(err)
	}
	assertChunks(t, chunks, []downloader.Chunk{
		{Index: 0, Start: 0, End: 24},
		{Index: 1, Start: 25, End: 49},
		{Index: 2, Start: 50, End: 74},
		{Index: 3, Start: 75, End: 99},
	})
}

func TestChunkPlanUnevenDivision(t *testing.T) {
	planner := chunkPlanner(t, 8, 1)
	chunks, err := planner.Plan(10, 3)
	if err != nil {
		t.Fatal(err)
	}
	assertChunks(t, chunks, []downloader.Chunk{
		{Index: 0, Start: 0, End: 3},
		{Index: 1, Start: 4, End: 6},
		{Index: 2, Start: 7, End: 9},
	})
}

func TestChunkPlanVerySmallFile(t *testing.T) {
	planner := chunkPlanner(t, 8, 1024)
	chunks, err := planner.Plan(17, 8)
	if err != nil {
		t.Fatal(err)
	}
	assertChunks(t, chunks, []downloader.Chunk{{Index: 0, Start: 0, End: 16}})
}

func TestChunkPlanEnforcesMinimumSizeAndMaximumCount(t *testing.T) {
	planner := chunkPlanner(t, 4, 10)
	chunks, err := planner.Plan(35, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 3 {
		t.Fatalf("planned %d chunks, expected 3", len(chunks))
	}
	for _, chunk := range chunks {
		if chunk.Size() < 10 {
			t.Errorf("chunk %+v is smaller than the minimum", chunk)
		}
	}
}

func TestChunkPlanLargeFile(t *testing.T) {
	planner := chunkPlanner(t, 16, 1)
	chunks, err := planner.Plan(math.MaxInt64, 16)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 16 {
		t.Fatalf("planned %d chunks, expected 16", len(chunks))
	}
	if chunks[0].Start != 0 || chunks[len(chunks)-1].End != math.MaxInt64-1 {
		t.Fatalf("large plan boundaries are %+v to %+v", chunks[0], chunks[len(chunks)-1])
	}
	assertContiguous(t, chunks, math.MaxInt64)
}

func TestChunkPlanRejectsInvalidInputs(t *testing.T) {
	tests := []struct {
		name      string
		maximum   int
		minimum   int64
		total     int64
		requested int
	}{
		{"zero maximum", 0, 1, 10, 1},
		{"zero minimum", 1, 0, 10, 1},
		{"zero total", 1, 1, 0, 1},
		{"negative total", 1, 1, -1, 1},
		{"zero requested", 1, 1, 10, 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			planner, err := downloader.NewChunkPlanner(test.maximum, test.minimum)
			if err == nil {
				_, err = planner.Plan(test.total, test.requested)
			}
			var planError downloader.InvalidChunkPlanError
			if !errors.As(err, &planError) {
				t.Fatalf("invalid plan returned %T, expected InvalidChunkPlanError", err)
			}
		})
	}
}

func chunkPlanner(t *testing.T, maximum int, minimum int64) downloader.ChunkPlanner {
	t.Helper()
	planner, err := downloader.NewChunkPlanner(maximum, minimum)
	if err != nil {
		t.Fatal(err)
	}

	return planner
}

func assertChunks(t *testing.T, actual, expected []downloader.Chunk) {
	t.Helper()
	if len(actual) != len(expected) {
		t.Fatalf("planned %d chunks, expected %d", len(actual), len(expected))
	}
	for index := range expected {
		if actual[index] != expected[index] {
			t.Errorf("chunk %d is %+v, expected %+v", index, actual[index], expected[index])
		}
	}
	if len(expected) > 0 {
		assertContiguous(t, actual, expected[len(expected)-1].End+1)
	}
}

func assertContiguous(t *testing.T, chunks []downloader.Chunk, totalSize int64) {
	t.Helper()
	position := int64(0)
	for index, chunk := range chunks {
		if chunk.Index != index {
			t.Errorf("chunk index is %d, expected %d", chunk.Index, index)
		}
		if chunk.Start != position {
			t.Errorf("chunk %d starts at %d, expected %d", index, chunk.Start, position)
		}
		if chunk.End < chunk.Start {
			t.Errorf("chunk %d has invalid bounds %+v", index, chunk)
		}
		position = chunk.End + 1
	}
	if position != totalSize {
		t.Errorf("chunks end at %d, expected %d", position, totalSize)
	}
}
