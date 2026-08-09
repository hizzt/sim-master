package imscore

import (
	"context"
	"strings"
	"testing"

	"github.com/1239t/vowifi-go/runtimehost/voiceclient"
)

func TestServiceExposesSignalingAudioControllerAtRuntime(t *testing.T) {
	var service interface{} = &Service{}
	controller, ok := service.(voiceclient.SignalingAudioController)
	if !ok {
		t.Fatal("imscore Service must expose the signaling audio controller")
	}

	if _, err := controller.SignalingAudioStats("missing-call"); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("stats error = %v", err)
	}
	if _, err := controller.SignalingAudioRecording("missing-call", "wav"); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("recording error = %v", err)
	}
	if _, err := controller.PlaySignalingAudio(context.Background(), "missing-call", voiceclient.SignalingAudioPlayback{
		Format: "pcm_s16le",
		Data:   []byte{0, 0},
	}); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("playback error = %v", err)
	}
}
