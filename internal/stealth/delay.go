package stealth

import (
	"context"
	"time"
)

// HumanDelay pauses execution for a random duration between minMs and maxMs milliseconds.
func HumanDelay(minMs, maxMs int) {
	_ = HumanDelayCtx(context.Background(), minMs, maxMs)
}

// HumanDelayCtx pauses execution for a random duration between minMs and maxMs milliseconds,
// respecting context cancellation.
func HumanDelayCtx(ctx context.Context, minMs, maxMs int) error {
	if minMs <= 0 {
		minMs = 300
	}
	if maxMs < minMs {
		maxMs = minMs + 1000
	}

	rndMu.Lock()
	delayMs := minMs + rnd.Intn(maxMs-minMs+1)
	rndMu.Unlock()

	duration := time.Duration(delayMs) * time.Millisecond
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
