package main

import (
	"io"
	"log"
	"testing"
)

func TestHandleResponseIgnoresHeartbeatStatusObject(t *testing.T) {
	client := NewNapCatClient("", log.New(io.Discard, "", 0), nil)
	payload := []byte(`{"post_type":"meta_event","meta_event_type":"heartbeat","status":{"online":true,"good":true},"interval":30000}`)

	if client.handleResponse(payload) {
		t.Fatalf("heartbeat event should not be treated as action response")
	}
}
