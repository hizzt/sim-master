package voiceclient

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestG711KnownSilenceAndRoundTrip(t *testing.T) {
	if got := linearToMuLaw(0); got != 0xff {
		t.Fatalf("PCMU zero=%#x", got)
	}
	if got := muLawToLinear(0xff); got != 0 {
		t.Fatalf("PCMU decoded zero=%d", got)
	}
	if got := linearToALaw(0); got != 0xd5 {
		t.Fatalf("PCMA zero=%#x", got)
	}
	if got := aLawToLinear(0xd5); got != 8 {
		t.Fatalf("PCMA decoded zero=%d", got)
	}
	for _, codec := range []string{"PCMU", "PCMA"} {
		pcm := make([]byte, 8)
		for index, sample := range []int16{-12000, -1000, 1000, 12000} {
			binary.LittleEndian.PutUint16(pcm[index*2:index*2+2], uint16(sample))
		}
		decoded := decodeG711(codec, encodeG711(codec, pcm))
		for index := 0; index < len(pcm); index += 2 {
			want := int(int16(binary.LittleEndian.Uint16(pcm[index : index+2])))
			got := int(int16(binary.LittleEndian.Uint16(decoded[index : index+2])))
			delta := got - want
			if delta < 0 {
				delta = -delta
			}
			if delta > 700 {
				t.Fatalf("%s sample %d round-trip=%d delta=%d", codec, want, got, delta)
			}
		}
	}
}

func TestPCM16WAVRoundTripAndValidation(t *testing.T) {
	pcm := []byte{0, 0, 1, 0, 0xff, 0x7f, 0, 0x80}
	wav := encodePCM16WAV(pcm)
	decoded, err := decodePCM16WAV(wav)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != string(pcm) {
		t.Fatalf("decoded=%v want=%v", decoded, pcm)
	}
	bad := append([]byte(nil), wav...)
	binary.LittleEndian.PutUint32(bad[24:28], 16000)
	if _, err := decodePCM16WAV(bad); err == nil {
		t.Fatal("accepted a 16 kHz WAV")
	}
}

func TestSignalingAudioRecordsG711WithLossAndRetainsAfterClose(t *testing.T) {
	client := &Client{cfg: Config{LocalIP: net.ParseIP("127.0.0.1")}, signalingCalls: newSignalingCallStore()}
	media, err := client.openSignalingMedia(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	remote, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		media.Close()
		t.Fatal(err)
	}
	defer remote.Close()
	remotePort := remote.LocalAddr().(*net.UDPAddr).Port
	answer := imsVoiceAnswer{RemoteIP: net.ParseIP("127.0.0.1"), RTCPIP: net.ParseIP("127.0.0.1"), RTPPort: remotePort, RTCPPort: remotePort + 1, Codec: "PCMU", Payload: 0, Direction: "sendrecv"}
	media.setAnswer(answer)
	now := time.Now().UTC()
	if err := client.signalingCalls.add(&signalingCall{status: SignalingCallStatus{CallID: "record-g711", State: "signaling_established", StartedAt: now, UpdatedAt: now, MediaSupported: true, MediaCodec: "PCMU", AudioReady: true}, media: media}); err != nil {
		media.Close()
		t.Fatal(err)
	}
	client.startSignalingMediaReaders("record-g711", media)

	pcm := make([]byte, signalingAudioFrameSamples*2)
	for offset := 0; offset < len(pcm); offset += 2 {
		binary.LittleEndian.PutUint16(pcm[offset:offset+2], uint16(int16(1000)))
	}
	payload := encodeG711("PCMU", pcm)
	send := func(sequence uint16) {
		packet := make([]byte, 12+len(payload))
		packet[0] = 0x80
		packet[1] = 0
		binary.BigEndian.PutUint16(packet[2:4], sequence)
		copy(packet[12:], payload)
		if _, err := remote.WriteToUDP(packet, media.rtp.LocalAddr().(*net.UDPAddr)); err != nil {
			t.Fatal(err)
		}
	}
	send(10)
	send(12)
	send(12)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stats, statsErr := client.SignalingAudioStats("record-g711")
		if statsErr == nil && stats.AudioPacketsDecoded == 2 && stats.RTPPacketsOutOfOrder == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	stats, err := client.SignalingAudioStats("record-g711")
	if err != nil {
		t.Fatal(err)
	}
	if stats.AudioPacketsDecoded != 2 || stats.RTPPacketsLost != 1 || stats.RTPPacketsOutOfOrder != 1 || stats.AudioSamplesRecorded != 3*signalingAudioFrameSamples {
		t.Fatalf("stats=%+v", stats)
	}
	recording, err := client.SignalingAudioRecording("record-g711", "pcm_s16le")
	if err != nil {
		t.Fatal(err)
	}
	if len(recording.Data) != 3*signalingAudioFrameSamples*2 {
		t.Fatalf("recording bytes=%d", len(recording.Data))
	}
	for _, value := range recording.Data[signalingAudioFrameSamples*2 : signalingAudioFrameSamples*4] {
		if value != 0 {
			t.Fatal("missing RTP frame was not represented as PCM silence")
		}
	}
	client.finishSignalingCall("record-g711", 200, "terminated", "test complete")
	wav, err := client.SignalingAudioRecording("record-g711", "wav")
	if err != nil {
		t.Fatal(err)
	}
	if len(wav.Data) != 44+len(recording.Data) || string(wav.Data[:4]) != "RIFF" {
		t.Fatalf("retained WAV bytes=%d", len(wav.Data))
	}
}

func TestSignalingAudioPlaybackSendsPacedRTP(t *testing.T) {
	client := &Client{cfg: Config{LocalIP: net.ParseIP("127.0.0.1")}, signalingCalls: newSignalingCallStore()}
	media, err := client.openSignalingMedia(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	remote, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	remotePort := remote.LocalAddr().(*net.UDPAddr).Port
	answer := imsVoiceAnswer{RemoteIP: net.ParseIP("127.0.0.1"), RTCPIP: net.ParseIP("127.0.0.1"), RTPPort: remotePort, RTCPPort: remotePort + 1, Codec: "PCMA", Payload: 8, Direction: "sendrecv"}
	media.setAnswer(answer)
	now := time.Now().UTC()
	if err := client.signalingCalls.add(&signalingCall{status: SignalingCallStatus{CallID: "play-g711", State: "signaling_established", StartedAt: now, UpdatedAt: now, MediaSupported: true, MediaCodec: "PCMA", AudioReady: true}, media: media}); err != nil {
		t.Fatal(err)
	}
	pcm := make([]byte, signalingAudioFrameSamples*2*2)
	for offset := 0; offset < len(pcm); offset += 2 {
		binary.LittleEndian.PutUint16(pcm[offset:offset+2], uint16(int16(2000)))
	}
	result := make(chan error, 1)
	go func() {
		_, playErr := client.PlaySignalingAudio(context.Background(), "play-g711", SignalingAudioPlayback{Format: "pcm_s16le", Data: pcm})
		result <- playErr
	}()
	packets := make([][]byte, 0, 2)
	times := make([]time.Time, 0, 2)
	buffer := make([]byte, 2048)
	_ = remote.SetReadDeadline(time.Now().Add(time.Second))
	for len(packets) < 2 {
		n, _, readErr := remote.ReadFromUDP(buffer)
		if readErr != nil {
			t.Fatal(readErr)
		}
		packets = append(packets, append([]byte(nil), buffer[:n]...))
		times = append(times, time.Now())
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if packets[0][1]&0x7f != 8 || packets[0][1]&0x80 == 0 || packets[1][1]&0x80 != 0 || len(packets[0]) != 12+signalingAudioFrameSamples {
		t.Fatalf("RTP headers first=%v second=%v", packets[0][:12], packets[1][:12])
	}
	if binary.BigEndian.Uint16(packets[1][2:4]) != binary.BigEndian.Uint16(packets[0][2:4])+1 {
		t.Fatal("RTP sequence did not increment")
	}
	if binary.BigEndian.Uint32(packets[1][4:8]) != binary.BigEndian.Uint32(packets[0][4:8])+signalingAudioFrameSamples {
		t.Fatal("RTP timestamp did not increment by 160")
	}
	if elapsed := times[1].Sub(times[0]); elapsed < 12*time.Millisecond || elapsed > 100*time.Millisecond {
		t.Fatalf("RTP pacing=%s", elapsed)
	}
	stats, err := client.SignalingAudioStats("play-g711")
	if err != nil || stats.RTPPacketsSent != 2 || stats.PCMSamplesSent != 2*signalingAudioFrameSamples || stats.PlaybackActive {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}
