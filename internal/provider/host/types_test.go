package host

import (
	"errors"
	"testing"
)

func TestCallErrorContainsOnlyStableDeliveryFields(t *testing.T) {
	err := &CallError{
		Code: "ambiguous_result", RequestID: "nonce-2", IdempotencyKey: "turn-a",
		Delivery: DeliveryMaybeSent, ReplaySafe: false, Message: "capture result is unknown",
	}
	if err.Error() != "capture result is unknown" {
		t.Fatalf("Error() = %q", err.Error())
	}
	var target *CallError
	if !errors.As(err, &target) || target.Delivery != DeliveryMaybeSent {
		t.Fatalf("errors.As() target = %#v", target)
	}
}
