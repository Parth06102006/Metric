package ws_test

import (
	"sync"
	"testing"
	"time"

	"traffic-center/domain"
	"traffic-center/internal/ws"
)

func TestHubDeliversToSubscribers(t *testing.T) {
	hub := ws.NewHub()
	client := hub.Register("p1")
	defer hub.Unregister("p1", client)

	hub.BroadcastSnapshot("p1", domain.Snapshot{RPS: 2.5})

	select {
	case message := <-client.Send():
		if len(message) == 0 {
			t.Fatal("empty message")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for broadcast")
	}
}

func TestHubDropDisconnectsSubscribers(t *testing.T) {
	hub := ws.NewHub()
	first := hub.Register("p1")
	second := hub.Register("p1")
	other := hub.Register("p2")
	defer hub.Unregister("p2", other)

	hub.Drop("p1")

	for _, subscriber := range []*ws.Client{first, second} {
		select {
		case <-subscriber.Done():
		case <-time.After(time.Second):
			t.Fatal("dropped project subscriber must observe done")
		}
	}

	// Other projects stay connected.
	if hub.SubscriberCount("p1") != 0 {
		t.Fatalf("SubscriberCount after Drop = %d, want 0", hub.SubscriberCount("p1"))
	}
	if hub.SubscriberCount("p2") != 1 {
		t.Fatalf("SubscriberCount(other) after Drop = %d, want 1", hub.SubscriberCount("p2"))
	}

	hub.BroadcastSnapshot("p2", domain.Snapshot{RPS: 1})
	select {
	case message := <-other.Send():
		if len(message) == 0 {
			t.Fatal("empty message")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for broadcast on surviving project")
	}
}

func TestHubConcurrentBroadcastAndUnregister(t *testing.T) {
	hub := ws.NewHub()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			client := hub.Register("p1")
			drained := make(chan struct{})
			go func() {
				defer close(drained)
				for {
					select {
					case <-client.Send():
					case <-client.Done():
						return
					}
				}
			}()

			for j := 0; j < 50; j++ {
				hub.BroadcastSnapshot("p1", domain.Snapshot{RPS: float64(j)})
			}

			hub.Unregister("p1", client)
			<-drained
		}()
	}

	wg.Wait()

	if count := hub.SubscriberCount("p1"); count != 0 {
		t.Fatalf("subscribers after unregister = %d, want 0", count)
	}
}

func TestHubUnregisterIsIdempotent(t *testing.T) {
	hub := ws.NewHub()
	client := hub.Register("p1")

	hub.Unregister("p1", client)
	hub.Unregister("p1", client)
}
