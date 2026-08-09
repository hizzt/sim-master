package runtimehost

import (
	"context"
	"testing"

	"github.com/1239t/vowifi-go/runtimehost/eventhost"
)

type localNumberEventRecorder struct {
	events []eventhost.Event
}

func (r *localNumberEventRecorder) Dispatch(_ context.Context, event eventhost.Event) {
	r.events = append(r.events, event)
}

func TestLearnLocalNumberUpdatesStateAndDispatchesSafeEvent(t *testing.T) {
	recorder := &localNumberEventRecorder{}
	instance := &Instance{deviceID: "modem-1", dispatch: recorder}

	instance.learnLocalNumber(context.Background(), " +447700900123 ", "register")
	if got := instance.State().PhoneNumber; got != "+447700900123" {
		t.Fatalf("state phone number = %q", got)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("events = %d", len(recorder.events))
	}
	learned, ok := recorder.events[0].(eventhost.LocalNumberLearned)
	if !ok {
		t.Fatalf("event type = %T", recorder.events[0])
	}
	if learned.Number != "+447700900123" || learned.Source != "register" || learned.IMSI != "" {
		t.Fatalf("event = %+v", learned)
	}

	instance.learnLocalNumber(context.Background(), "+447700900123", "register")
	if len(recorder.events) != 1 {
		t.Fatalf("unchanged number dispatched again: %d events", len(recorder.events))
	}
}
