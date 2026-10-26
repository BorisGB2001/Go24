package live

import "testing"

func TestBroker(t *testing.T) {
	b := NewBroker()
	a, stopA := b.Subscribe("t1")
	c, stopC := b.Subscribe("t1")
	other, stopO := b.Subscribe("t2")
	defer stopO()
	b.Publish("t1", "c1")
	if <-a != "c1" || <-c != "c1" {
		t.Fatal("subscribers of t1 did not get c1")
	}
	select {
	case v := <-other:
		t.Fatalf("t2 got %q", v)
	default:
	}
	stopA()
	b.Publish("t1", "c2")
	if <-c != "c2" {
		t.Fatal("remaining subscriber missed c2")
	}
	select {
	case v := <-a:
		t.Fatalf("unsubscribed channel got %q", v)
	default:
	}
	stopC()
	if len(b.subs) != 1 {
		t.Fatalf("subs = %v", b.subs)
	}
	for i := 0; i < 100; i++ { // a full channel never blocks Publish
		b.Publish("t2", "x")
	}
}
