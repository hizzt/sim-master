package voiceclient

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

const (
	SignalingAudioSampleRate    = 8000
	signalingAudioChannels      = 1
	signalingAudioBitsPerSample = 16
	signalingAudioFrameSamples  = 160
	signalingAudioFrameDuration = 20 * time.Millisecond
	maxSignalingAudioPCMBytes   = SignalingAudioSampleRate * 2 * 120
)

// SignalingAudioPlayback is decoded PCM input for a live PCMU/PCMA call.
// Format is wav or pcm_s16le. Both formats are fixed to mono, 8 kHz, 16-bit.
type SignalingAudioPlayback struct {
	Format string
	Data   []byte
}

// SignalingAudioStats reports the file-backed G.711 media path for one call.
type SignalingAudioStats struct {
	CallID               string `json:"call_id"`
	Codec                string `json:"codec,omitempty"`
	SampleRate           int    `json:"sample_rate"`
	FrameDurationMS      int    `json:"frame_duration_ms"`
	PlaybackActive       bool   `json:"playback_active"`
	RTPPacketsSent       uint64 `json:"rtp_packets_sent"`
	RTPBytesSent         uint64 `json:"rtp_bytes_sent"`
	PCMSamplesSent       uint64 `json:"pcm_samples_sent"`
	AudioPacketsDecoded  uint64 `json:"audio_packets_decoded"`
	AudioSamplesRecorded uint64 `json:"audio_samples_recorded"`
	RecordingBytes       uint64 `json:"recording_bytes"`
	RecordingDurationMS  uint64 `json:"recording_duration_ms"`
	RecordingTruncated   bool   `json:"recording_truncated"`
	RTPPacketsLost       uint64 `json:"rtp_packets_lost"`
	RTPPacketsOutOfOrder uint64 `json:"rtp_packets_out_of_order"`
	LastPlaybackAt       string `json:"last_playback_at,omitempty"`
	LastPlaybackError    string `json:"last_playback_error,omitempty"`
}

// SignalingAudioRecording is an in-memory recording encoded for the control
// plane. Data is WAV or raw PCM depending on Format.
type SignalingAudioRecording struct {
	CallID        string              `json:"call_id"`
	Format        string              `json:"format"`
	ContentType   string              `json:"content_type"`
	SampleRate    int                 `json:"sample_rate"`
	Channels      int                 `json:"channels"`
	BitsPerSample int                 `json:"bits_per_sample"`
	Data          []byte              `json:"-"`
	Stats         SignalingAudioStats `json:"stats"`
}

// SignalingAudioController is kept separate from signaling and messaging so
// callers can feature-detect the file audio path without changing IMS setup.
type SignalingAudioController interface {
	PlaySignalingAudio(context.Context, string, SignalingAudioPlayback) (SignalingAudioStats, error)
	SignalingAudioRecording(string, string) (SignalingAudioRecording, error)
	SignalingAudioStats(string) (SignalingAudioStats, error)
}

type signalingAudioState struct {
	recordingPCM         []byte
	recordingTruncated   bool
	playbackActive       bool
	rtpPacketsSent       uint64
	rtpBytesSent         uint64
	pcmSamplesSent       uint64
	audioPacketsDecoded  uint64
	audioSamplesRecorded uint64
	rtpPacketsLost       uint64
	rtpPacketsOutOfOrder uint64
	lastPlaybackAt       time.Time
	lastPlaybackError    string
	txInitialized        bool
	txSequence           uint16
	txTimestamp          uint32
	txSSRC               uint32
	rxInitialized        bool
	rxExpectedSequence   uint16
	rxLastFrameSamples   int
}

type signalingAudioReceiveUpdate struct {
	DecodedPackets     uint64
	RecordedSamples    uint64
	RecordedBytes      uint64
	LostPackets        uint64
	OutOfOrderPackets  uint64
	RecordingTruncated bool
}

func signalingAudioCodecSupported(codec string) bool {
	return strings.EqualFold(strings.TrimSpace(codec), "PCMU") || strings.EqualFold(strings.TrimSpace(codec), "PCMA")
}

func remoteMayReceiveMedia(direction string) bool {
	return direction == "sendrecv" || direction == "recvonly"
}

func (s *signalingAudioState) resetReceiveSequence() {
	s.rxInitialized = false
	s.rxExpectedSequence = 0
	s.rxLastFrameSamples = 0
}

func (s *signalingAudioState) recordRTP(codec string, packet []byte, payloadType int) signalingAudioReceiveUpdate {
	sequence, payload, ok := signalingRTPPayload(packet, payloadType)
	if !ok || !signalingAudioCodecSupported(codec) {
		return signalingAudioReceiveUpdate{}
	}
	update := signalingAudioReceiveUpdate{}
	if !s.rxInitialized {
		s.rxInitialized = true
		s.rxExpectedSequence = sequence + 1
	} else if sequence != s.rxExpectedSequence {
		delta := uint16(sequence - s.rxExpectedSequence)
		if delta >= 0x8000 {
			s.rtpPacketsOutOfOrder++
			update.OutOfOrderPackets = 1
			return update
		}
		missing := uint64(delta)
		s.rtpPacketsLost += missing
		update.LostPackets = missing
		frameSamples := s.rxLastFrameSamples
		if frameSamples <= 0 {
			frameSamples = signalingAudioFrameSamples
		}
		requestedSilence := missing * uint64(frameSamples)
		appended := s.appendRecordingSilence(requestedSilence)
		update.RecordedSamples += appended
		update.RecordedBytes += appended * 2
		s.rxExpectedSequence = sequence + 1
	} else {
		s.rxExpectedSequence++
	}

	pcm := decodeG711(codec, payload)
	samples := uint64(len(pcm) / 2)
	appendedBytes := s.appendRecordingPCM(pcm)
	appendedSamples := uint64(appendedBytes / 2)
	s.rxLastFrameSamples = int(samples)
	s.audioPacketsDecoded++
	s.audioSamplesRecorded += update.RecordedSamples + appendedSamples
	update.DecodedPackets = 1
	update.RecordedSamples += appendedSamples
	update.RecordedBytes += uint64(appendedBytes)
	update.RecordingTruncated = s.recordingTruncated
	return update
}

func (s *signalingAudioState) appendRecordingPCM(pcm []byte) int {
	remaining := maxSignalingAudioPCMBytes - len(s.recordingPCM)
	if remaining <= 0 {
		if len(pcm) > 0 {
			s.recordingTruncated = true
		}
		return 0
	}
	if len(pcm) > remaining {
		pcm = pcm[:remaining-(remaining%2)]
		s.recordingTruncated = true
	}
	s.recordingPCM = append(s.recordingPCM, pcm...)
	return len(pcm)
}

func (s *signalingAudioState) appendRecordingSilence(samples uint64) uint64 {
	remainingSamples := uint64((maxSignalingAudioPCMBytes - len(s.recordingPCM)) / 2)
	toAppend := samples
	if toAppend > remainingSamples {
		toAppend = remainingSamples
		s.recordingTruncated = true
	}
	if toAppend > 0 {
		s.recordingPCM = append(s.recordingPCM, make([]byte, int(toAppend)*2)...)
	}
	return toAppend
}

func (c *Client) PlaySignalingAudio(ctx context.Context, callID string, input SignalingAudioPlayback) (stats SignalingAudioStats, err error) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return stats, errors.New("voiceclient: call_id is required")
	}
	if c == nil || c.signalingCalls == nil {
		return stats, errors.New("voiceclient: signaling call store is not ready")
	}
	status, ok := c.signalingCalls.status(callID)
	if !ok {
		return stats, fmt.Errorf("voiceclient: signaling call %s was not found", callID)
	}
	if signalingCallTerminal(status.State) {
		return stats, fmt.Errorf("voiceclient: signaling call %s is already %s", callID, status.State)
	}
	media := c.signalingCalls.media(callID)
	if media == nil {
		return stats, errors.New("voiceclient: call media endpoint is unavailable")
	}
	pcm, err := decodeSignalingAudioInput(input)
	if err != nil {
		return stats, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !media.playMu.TryLock() {
		return stats, errors.New("voiceclient: audio playback is already active for this call")
	}
	defer media.playMu.Unlock()

	media.mu.Lock()
	if media.closed || media.rtp == nil || media.answer == nil {
		media.mu.Unlock()
		return stats, errors.New("voiceclient: negotiated RTP media is not ready")
	}
	answer := *media.answer
	if !signalingAudioCodecSupported(answer.Codec) {
		media.mu.Unlock()
		return stats, fmt.Errorf("voiceclient: negotiated codec %s has no built-in encoder", answer.Codec)
	}
	if !remoteMayReceiveMedia(answer.Direction) {
		media.mu.Unlock()
		return stats, fmt.Errorf("voiceclient: remote SDP direction %s does not accept media", answer.Direction)
	}
	if !media.audio.txInitialized {
		if err := media.audio.initializeTransmit(); err != nil {
			media.mu.Unlock()
			return stats, err
		}
	}
	media.audio.playbackActive = true
	media.audio.lastPlaybackAt = time.Now().UTC()
	media.audio.lastPlaybackError = ""
	media.mu.Unlock()

	defer func() {
		media.mu.Lock()
		media.audio.playbackActive = false
		if err != nil {
			media.audio.lastPlaybackError = err.Error()
		}
		media.mu.Unlock()
		stats, _ = c.SignalingAudioStats(callID)
	}()

	peer := &net.UDPAddr{IP: append(net.IP(nil), answer.RemoteIP...), Port: answer.RTPPort}
	started := time.Now()
	frameCount := (len(pcm) + signalingAudioFrameSamples*2 - 1) / (signalingAudioFrameSamples * 2)
	for frameIndex := 0; frameIndex < frameCount; frameIndex++ {
		if frameIndex > 0 {
			deadline := started.Add(time.Duration(frameIndex) * signalingAudioFrameDuration)
			timer := time.NewTimer(time.Until(deadline))
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return stats, ctx.Err()
			case <-timer.C:
			}
		}
		framePCM := make([]byte, signalingAudioFrameSamples*2)
		start := frameIndex * len(framePCM)
		end := start + len(framePCM)
		if end > len(pcm) {
			end = len(pcm)
		}
		copy(framePCM, pcm[start:end])
		payload := encodeG711(answer.Codec, framePCM)

		media.mu.Lock()
		if media.closed || media.answer == nil || !sameIMSVoiceEndpoint(*media.answer, answer) {
			media.mu.Unlock()
			return stats, errors.New("voiceclient: RTP media endpoint changed during playback")
		}
		packet := media.audio.nextRTPPacket(answer.Payload, frameIndex == 0, payload)
		media.mu.Unlock()
		n, writeErr := media.rtp.WriteTo(packet, peer)
		if writeErr != nil {
			return stats, fmt.Errorf("voiceclient: send RTP audio: %w", writeErr)
		}
		if n != len(packet) {
			return stats, fmt.Errorf("voiceclient: short RTP audio write: wrote %d of %d bytes", n, len(packet))
		}
		media.mu.Lock()
		media.audio.rtpPacketsSent++
		media.audio.rtpBytesSent += uint64(n)
		media.audio.pcmSamplesSent += signalingAudioFrameSamples
		media.mu.Unlock()
		c.signalingCalls.update(callID, func(status *SignalingCallStatus) {
			if signalingCallTerminal(status.State) {
				return
			}
			status.RTPPacketsSent++
			status.RTPBytesSent += uint64(n)
			status.MediaReady = true
			status.MediaMode = "rtp_transport_sending"
		})
	}
	return c.SignalingAudioStats(callID)
}

func (s *signalingAudioState) initializeTransmit() error {
	seed := make([]byte, 10)
	if _, err := cryptorand.Read(seed); err != nil {
		return fmt.Errorf("voiceclient: initialize RTP sender: %w", err)
	}
	s.txSequence = binary.BigEndian.Uint16(seed[0:2])
	s.txTimestamp = binary.BigEndian.Uint32(seed[2:6])
	s.txSSRC = binary.BigEndian.Uint32(seed[6:10])
	s.txInitialized = true
	return nil
}

func (s *signalingAudioState) nextRTPPacket(payloadType int, marker bool, payload []byte) []byte {
	packet := make([]byte, 12+len(payload))
	packet[0] = 0x80
	packet[1] = byte(payloadType & 0x7f)
	if marker {
		packet[1] |= 0x80
	}
	binary.BigEndian.PutUint16(packet[2:4], s.txSequence)
	binary.BigEndian.PutUint32(packet[4:8], s.txTimestamp)
	binary.BigEndian.PutUint32(packet[8:12], s.txSSRC)
	copy(packet[12:], payload)
	s.txSequence++
	s.txTimestamp += signalingAudioFrameSamples
	return packet
}

func (c *Client) SignalingAudioStats(callID string) (SignalingAudioStats, error) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return SignalingAudioStats{}, errors.New("voiceclient: call_id is required")
	}
	if c == nil || c.signalingCalls == nil {
		return SignalingAudioStats{}, errors.New("voiceclient: signaling call store is not ready")
	}
	status, ok := c.signalingCalls.status(callID)
	if !ok {
		return SignalingAudioStats{}, fmt.Errorf("voiceclient: signaling call %s was not found", callID)
	}
	media := c.signalingCalls.media(callID)
	if media == nil {
		return SignalingAudioStats{}, errors.New("voiceclient: call media endpoint is unavailable")
	}
	media.mu.Lock()
	defer media.mu.Unlock()
	stats := SignalingAudioStats{
		CallID: callID, Codec: status.MediaCodec, SampleRate: SignalingAudioSampleRate,
		FrameDurationMS: int(signalingAudioFrameDuration / time.Millisecond),
		PlaybackActive:  media.audio.playbackActive, RTPPacketsSent: media.audio.rtpPacketsSent,
		RTPBytesSent: media.audio.rtpBytesSent, PCMSamplesSent: media.audio.pcmSamplesSent,
		AudioPacketsDecoded:  media.audio.audioPacketsDecoded,
		AudioSamplesRecorded: media.audio.audioSamplesRecorded,
		RecordingBytes:       uint64(len(media.audio.recordingPCM)),
		RecordingTruncated:   media.audio.recordingTruncated,
		RTPPacketsLost:       media.audio.rtpPacketsLost,
		RTPPacketsOutOfOrder: media.audio.rtpPacketsOutOfOrder,
		LastPlaybackError:    media.audio.lastPlaybackError,
	}
	stats.RecordingDurationMS = stats.AudioSamplesRecorded * 1000 / SignalingAudioSampleRate
	if !media.audio.lastPlaybackAt.IsZero() {
		stats.LastPlaybackAt = media.audio.lastPlaybackAt.Format(time.RFC3339Nano)
	}
	return stats, nil
}

func (c *Client) SignalingAudioRecording(callID, format string) (SignalingAudioRecording, error) {
	stats, err := c.SignalingAudioStats(callID)
	if err != nil {
		return SignalingAudioRecording{}, err
	}
	media := c.signalingCalls.media(strings.TrimSpace(callID))
	if media == nil {
		return SignalingAudioRecording{}, errors.New("voiceclient: call media endpoint is unavailable")
	}
	media.mu.Lock()
	pcm := append([]byte(nil), media.audio.recordingPCM...)
	media.mu.Unlock()
	format, err = normalizeSignalingAudioFormat(format)
	if err != nil {
		return SignalingAudioRecording{}, err
	}
	data := pcm
	contentType := "application/octet-stream"
	if format == "wav" {
		data = encodePCM16WAV(pcm)
		contentType = "audio/wav"
	}
	return SignalingAudioRecording{
		CallID: strings.TrimSpace(callID), Format: format, ContentType: contentType,
		SampleRate: SignalingAudioSampleRate, Channels: signalingAudioChannels,
		BitsPerSample: signalingAudioBitsPerSample, Data: data, Stats: stats,
	}, nil
}

func decodeSignalingAudioInput(input SignalingAudioPlayback) ([]byte, error) {
	format, err := normalizeSignalingAudioFormat(input.Format)
	if err != nil {
		return nil, err
	}
	var pcm []byte
	if format == "wav" {
		pcm, err = decodePCM16WAV(input.Data)
		if err != nil {
			return nil, fmt.Errorf("voiceclient: decode WAV audio: %w", err)
		}
	} else {
		pcm = append([]byte(nil), input.Data...)
	}
	if len(pcm) == 0 {
		return nil, errors.New("voiceclient: audio input is empty")
	}
	if len(pcm)%2 != 0 {
		return nil, errors.New("voiceclient: PCM16LE input length must be even")
	}
	if len(pcm) > maxSignalingAudioPCMBytes {
		return nil, fmt.Errorf("voiceclient: decoded PCM exceeds %d bytes (120 seconds)", maxSignalingAudioPCMBytes)
	}
	return pcm, nil
}

func normalizeSignalingAudioFormat(format string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "wav", "wave", "audio/wav":
		return "wav", nil
	case "pcm", "pcm_s16le", "s16le", "audio/l16":
		return "pcm_s16le", nil
	default:
		return "", errors.New("voiceclient: audio_format must be wav or pcm_s16le")
	}
}

func signalingRTPPayload(packet []byte, payloadType int) (uint16, []byte, bool) {
	if len(packet) < 12 || packet[0]>>6 != 2 || payloadType < 0 || payloadType > 127 || int(packet[1]&0x7f) != payloadType {
		return 0, nil, false
	}
	headerLength := 12 + int(packet[0]&0x0f)*4
	if len(packet) < headerLength {
		return 0, nil, false
	}
	if packet[0]&0x10 != 0 {
		if len(packet) < headerLength+4 {
			return 0, nil, false
		}
		extensionWords := int(binary.BigEndian.Uint16(packet[headerLength+2 : headerLength+4]))
		headerLength += 4 + extensionWords*4
		if len(packet) < headerLength {
			return 0, nil, false
		}
	}
	payloadEnd := len(packet)
	if packet[0]&0x20 != 0 {
		paddingLength := int(packet[len(packet)-1])
		if paddingLength == 0 || paddingLength > payloadEnd-headerLength {
			return 0, nil, false
		}
		payloadEnd -= paddingLength
	}
	if payloadEnd <= headerLength {
		return 0, nil, false
	}
	return binary.BigEndian.Uint16(packet[2:4]), packet[headerLength:payloadEnd], true
}

func encodeG711(codec string, pcm []byte) []byte {
	payload := make([]byte, len(pcm)/2)
	for index := range payload {
		sample := int16(binary.LittleEndian.Uint16(pcm[index*2 : index*2+2]))
		if strings.EqualFold(codec, "PCMA") {
			payload[index] = linearToALaw(sample)
		} else {
			payload[index] = linearToMuLaw(sample)
		}
	}
	return payload
}

func decodeG711(codec string, payload []byte) []byte {
	pcm := make([]byte, len(payload)*2)
	for index, value := range payload {
		var sample int16
		if strings.EqualFold(codec, "PCMA") {
			sample = aLawToLinear(value)
		} else {
			sample = muLawToLinear(value)
		}
		binary.LittleEndian.PutUint16(pcm[index*2:index*2+2], uint16(sample))
	}
	return pcm
}

func linearToMuLaw(sample int16) byte {
	value := int(sample)
	sign := 0
	if value < 0 {
		sign = 0x80
		value = -value
	}
	if value > 32635 {
		value = 32635
	}
	value += 0x84
	exponent := 7
	for mask := 0x4000; exponent > 0 && value&mask == 0; exponent-- {
		mask >>= 1
	}
	mantissa := (value >> (exponent + 3)) & 0x0f
	return byte(^(sign | exponent<<4 | mantissa))
}

func muLawToLinear(value byte) int16 {
	decoded := ^value
	magnitude := (int(decoded&0x0f) << 3) + 0x84
	magnitude <<= (decoded & 0x70) >> 4
	if decoded&0x80 != 0 {
		return int16(0x84 - magnitude)
	}
	return int16(magnitude - 0x84)
}

func linearToALaw(sample int16) byte {
	value := int(sample)
	mask := 0xd5
	if value < 0 {
		mask = 0x55
		value = -value - 8
	}
	if value < 0 {
		value = 0
	}
	if value > 32767 {
		value = 32767
	}
	segment := 0
	for threshold := 0xff; segment < 8 && value > threshold; segment++ {
		threshold = (threshold << 1) | 1
	}
	if segment >= 8 {
		return byte(0x7f ^ mask)
	}
	encoded := segment << 4
	if segment < 2 {
		encoded |= (value >> 4) & 0x0f
	} else {
		encoded |= (value >> (segment + 3)) & 0x0f
	}
	return byte(encoded ^ mask)
}

func aLawToLinear(value byte) int16 {
	decoded := value ^ 0x55
	magnitude := int(decoded&0x0f) << 4
	segment := int(decoded&0x70) >> 4
	switch segment {
	case 0:
		magnitude += 8
	case 1:
		magnitude += 0x108
	default:
		magnitude += 0x108
		magnitude <<= segment - 1
	}
	if decoded&0x80 != 0 {
		return int16(magnitude)
	}
	return int16(-magnitude)
}

func encodePCM16WAV(pcm []byte) []byte {
	dataLength := len(pcm)
	wav := make([]byte, 44+dataLength)
	copy(wav[0:4], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:8], uint32(36+dataLength))
	copy(wav[8:12], "WAVE")
	copy(wav[12:16], "fmt ")
	binary.LittleEndian.PutUint32(wav[16:20], 16)
	binary.LittleEndian.PutUint16(wav[20:22], 1)
	binary.LittleEndian.PutUint16(wav[22:24], signalingAudioChannels)
	binary.LittleEndian.PutUint32(wav[24:28], SignalingAudioSampleRate)
	binary.LittleEndian.PutUint32(wav[28:32], SignalingAudioSampleRate*signalingAudioChannels*signalingAudioBitsPerSample/8)
	binary.LittleEndian.PutUint16(wav[32:34], signalingAudioChannels*signalingAudioBitsPerSample/8)
	binary.LittleEndian.PutUint16(wav[34:36], signalingAudioBitsPerSample)
	copy(wav[36:40], "data")
	binary.LittleEndian.PutUint32(wav[40:44], uint32(dataLength))
	copy(wav[44:], pcm)
	return wav
}

func decodePCM16WAV(wav []byte) ([]byte, error) {
	if len(wav) < 12 || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return nil, errors.New("invalid RIFF/WAVE header")
	}
	var formatOK bool
	var data []byte
	for offset := 12; offset+8 <= len(wav); {
		chunkID := string(wav[offset : offset+4])
		chunkLength := int(binary.LittleEndian.Uint32(wav[offset+4 : offset+8]))
		chunkStart := offset + 8
		chunkEnd := chunkStart + chunkLength
		if chunkLength < 0 || chunkEnd < chunkStart || chunkEnd > len(wav) {
			return nil, fmt.Errorf("invalid %s chunk length", chunkID)
		}
		switch chunkID {
		case "fmt ":
			if chunkLength < 16 {
				return nil, errors.New("WAV fmt chunk is too short")
			}
			format := binary.LittleEndian.Uint16(wav[chunkStart : chunkStart+2])
			channels := binary.LittleEndian.Uint16(wav[chunkStart+2 : chunkStart+4])
			sampleRate := binary.LittleEndian.Uint32(wav[chunkStart+4 : chunkStart+8])
			bits := binary.LittleEndian.Uint16(wav[chunkStart+14 : chunkStart+16])
			if format != 1 || channels != signalingAudioChannels || sampleRate != SignalingAudioSampleRate || bits != signalingAudioBitsPerSample {
				return nil, fmt.Errorf("WAV must be PCM mono %d Hz 16-bit", SignalingAudioSampleRate)
			}
			formatOK = true
		case "data":
			data = wav[chunkStart:chunkEnd]
		}
		offset = chunkEnd
		if chunkLength%2 != 0 {
			offset++
		}
	}
	if !formatOK {
		return nil, errors.New("WAV fmt chunk is missing")
	}
	if data == nil {
		return nil, errors.New("WAV data chunk is missing")
	}
	if len(data)%2 != 0 {
		return nil, errors.New("WAV PCM data length must be even")
	}
	return append([]byte(nil), data...), nil
}
