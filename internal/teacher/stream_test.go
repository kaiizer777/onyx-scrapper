package teacher

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEventBroadcaster_PubSub(t *testing.T) {
	broadcaster := NewEventBroadcaster()
	runID := "tr_test_pubsub"

	ch, unsubscribe := broadcaster.Subscribe(runID)
	defer unsubscribe()

	testEvent := StreamEvent{
		RunID: runID,
		Event: "section_drafted",
		Data: map[string]string{
			"section_id": "sec_1",
			"title":      "Test Section",
		},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	broadcaster.Broadcast(testEvent)

	select {
	case received := <-ch:
		if received.Event != "section_drafted" {
			t.Errorf("expected event 'section_drafted', got %q", received.Event)
		}
		if received.RunID != runID {
			t.Errorf("expected runID %q, got %q", runID, received.RunID)
		}
		sseStr := received.FormatSSE()
		if !strings.HasPrefix(sseStr, "data: {") || !strings.HasSuffix(sseStr, "\n\n") {
			t.Errorf("unexpected SSE format: %q", sseStr)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for broadcast event")
	}
}

func TestEventBroadcaster_MultipleSubscribers(t *testing.T) {
	broadcaster := NewEventBroadcaster()
	runID := "tr_test_multi"

	ch1, unsub1 := broadcaster.Subscribe(runID)
	defer unsub1()
	ch2, unsub2 := broadcaster.Subscribe(runID)
	defer unsub2()

	broadcaster.Broadcast(StreamEvent{
		RunID: runID,
		Event: "outline_ready",
		Data:  "test-data",
	})

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		select {
		case ev := <-ch1:
			if ev.Event != "outline_ready" {
				t.Errorf("sub 1 got event %q", ev.Event)
			}
		case <-time.After(500 * time.Millisecond):
			t.Error("sub 1 timed out")
		}
	}()

	go func() {
		defer wg.Done()
		select {
		case ev := <-ch2:
			if ev.Event != "outline_ready" {
				t.Errorf("sub 2 got event %q", ev.Event)
			}
		case <-time.After(500 * time.Millisecond):
			t.Error("sub 2 timed out")
		}
	}()

	wg.Wait()
}

func TestEventBroadcaster_DifferentRunsIsolated(t *testing.T) {
	broadcaster := NewEventBroadcaster()

	chA, unsubA := broadcaster.Subscribe("run_A")
	defer unsubA()
	chB, unsubB := broadcaster.Subscribe("run_B")
	defer unsubB()

	broadcaster.Broadcast(StreamEvent{
		RunID: "run_A",
		Event: "event_for_A",
	})

	select {
	case ev := <-chA:
		if ev.Event != "event_for_A" {
			t.Errorf("expected 'event_for_A', got %q", ev.Event)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for run_A event")
	}

	select {
	case ev := <-chB:
		t.Fatalf("unexpected event on run_B subscriber: %v", ev)
	case <-time.After(50 * time.Millisecond):
		// Expected: no event on run_B
	}
}

func TestEventBroadcaster_UnsubscribeAndCloseRun(t *testing.T) {
	broadcaster := NewEventBroadcaster()
	runID := "tr_test_close"

	ch, unsubscribe := broadcaster.Subscribe(runID)
	unsubscribe()

	// Channel should be closed
	_, ok := <-ch
	if ok {
		t.Errorf("expected subscriber channel to be closed after unsubscribe")
	}

	// Repeated unsubscribe must not panic
	unsubscribe()

	ch2, unsub2 := broadcaster.Subscribe(runID)
	broadcaster.CloseRun(runID)

	_, ok2 := <-ch2
	if ok2 {
		t.Errorf("expected subscriber channel to be closed after CloseRun")
	}

	// Calling unsub after CloseRun must not panic
	unsub2()
}

func TestEventBroadcaster_DoubleCloseProtection_Concurrent(t *testing.T) {
	broadcaster := NewEventBroadcaster()
	runID := "tr_test_concurrent_close"

	const numSubs = 20
	unsubs := make([]func(), numSubs)
	for i := 0; i < numSubs; i++ {
		_, unsubs[i] = broadcaster.Subscribe(runID)
	}

	var wg sync.WaitGroup
	wg.Add(numSubs + 1)

	// Concurrently close run and call unsubs
	go func() {
		defer wg.Done()
		broadcaster.CloseRun(runID)
	}()

	for i := 0; i < numSubs; i++ {
		unsub := unsubs[i]
		go func() {
			defer wg.Done()
			unsub()
			// Call twice to test idempotency
			unsub()
		}()
	}

	wg.Wait()
}

