package collab

import "testing"

func TestRoomSignalsCoalesceAndIsolate(t *testing.T) {
	hub := NewRoomSignals()
	a, offA := hub.Subscribe("papers:a")
	b, offB := hub.Subscribe("papers:a")
	c, offC := hub.Subscribe("papers:b")
	defer offB()
	defer offC()
	for i := 0; i < 1000; i++ {
		hub.Notify("papers:a")
	}
	for _, ch := range []<-chan struct{}{a, b} {
		select {
		case <-ch:
		default:
			t.Fatal("missing notification")
		}
		select {
		case <-ch:
			t.Fatal("notifications not coalesced")
		default:
		}
	}
	select {
	case <-c:
		t.Fatal("cross-room notification")
	default:
	}
	offA()
	offA()
	hub.Notify("papers:a")
	select {
	case <-a:
		t.Fatal("unsubscribed room still notified")
	default:
	}
	select {
	case <-b:
	default:
		t.Fatal("remaining subscriber missing")
	}
}
