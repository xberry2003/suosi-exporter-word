package logic

import (
	"context"
	"strings"
	"testing"
)

func TestWaitLoginReturnsWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := WaitLoginReturnCookieString(ctx, "TB_ACCESS_TOKEN")
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("expected cancellation error, got %v", err)
	}
}
