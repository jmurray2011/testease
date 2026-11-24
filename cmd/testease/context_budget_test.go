package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// One docker context that never answers - an ssh context waiting for a
// password, say - is skipped with a notice rather than holding up the look at
// every other context.
func TestAContextThatNeverAnswersIsSkippedWhenItsTimeIsUp(t *testing.T) {
	old := contextBudget
	contextBudget = 50 * time.Millisecond
	t.Cleanup(func() { contextBudget = old })

	start := time.Now()
	_, err := askContext(context.Background(), func(ctx context.Context) (map[string]map[string]string, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err == nil || !strings.Contains(err.Error(), "no answer within 50ms") {
		t.Errorf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("waited past the budget")
	}
}
