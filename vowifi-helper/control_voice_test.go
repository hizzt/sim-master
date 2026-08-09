package main

import (
	"context"
	"strings"
	"testing"

	"github.com/1239t/vowifi-go/runtimehost"
)

func TestControlDialCallValidatesIMSReadiness(t *testing.T) {
	server := &controlServer{instance: &runtimehost.Instance{}}
	response := server.handle(context.Background(), controlRequest{Action: "dial_call", PhoneNumber: "+18005551212"})
	if response.OK || !strings.Contains(response.Error, "IMS registration over SWu is not ready") {
		t.Fatalf("response=%+v", response)
	}
}

func TestControlHangupCallRequiresCallID(t *testing.T) {
	server := &controlServer{instance: &runtimehost.Instance{}}
	response := server.handle(context.Background(), controlRequest{Action: "hangup_call"})
	if response.OK || response.Error != "call_id is required" {
		t.Fatalf("response=%+v", response)
	}
}

func TestControlVoiceStatusReturnsCallList(t *testing.T) {
	server := &controlServer{instance: &runtimehost.Instance{}}
	response := server.handle(context.Background(), controlRequest{Action: "voice_status"})
	if !response.OK || response.Calls == nil || len(response.Calls) != 0 {
		t.Fatalf("response=%+v", response)
	}
}

func TestControlAudioActionsValidateInputs(t *testing.T) {
	server := &controlServer{instance: &runtimehost.Instance{}}
	for name, request := range map[string]controlRequest{
		"play call":      {Action: "play_audio", AudioFormat: "wav", AudioBase64: "AA=="},
		"play data":      {Action: "play_audio", CallID: "call-1", AudioFormat: "wav"},
		"recording call": {Action: "get_recording", AudioFormat: "wav"},
		"stats call":     {Action: "audio_stats"},
	} {
		response := server.handle(context.Background(), request)
		if response.OK || response.Error == "" {
			t.Fatalf("%s response=%+v", name, response)
		}
	}
	response := server.handle(context.Background(), controlRequest{
		Action: "play_audio", CallID: "call-1", AudioFormat: "wav", AudioBase64: "not base64",
	})
	if response.OK || !strings.Contains(response.Error, "not valid standard base64") {
		t.Fatalf("invalid base64 response=%+v", response)
	}
}
