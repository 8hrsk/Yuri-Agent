// Package voice defines provider-neutral speech-to-text and text-to-speech
// contracts. Audio capture and playback remain UI/platform responsibilities.
package voice

import "context"

type AudioInput struct {
	Data        []byte
	Filename    string
	ContentType string
	Language    string
}

type Transcript struct {
	Text     string
	Language string
}

type SpeechRequest struct {
	Text         string
	Voice        string
	Format       string
	Instructions string
}

type AudioOutput struct {
	Data        []byte
	ContentType string
	Format      string
}

type Transcriber interface {
	Transcribe(context.Context, AudioInput) (Transcript, error)
}

type Synthesizer interface {
	Synthesize(context.Context, SpeechRequest) (AudioOutput, error)
}
