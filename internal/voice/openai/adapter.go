// Package openai provides OpenAI-compatible STT and TTS HTTP adapters.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/voice"
)

const (
	defaultMaxInputBytes  = 25 << 20
	defaultMaxOutputBytes = 32 << 20
)

type CredentialProvider func(context.Context) (string, error)

type Options struct {
	BaseURL            string
	TranscriptionModel string
	SpeechModel        string
	Credential         CredentialProvider
	HTTPClient         *http.Client
	MaxInputBytes      int
	MaxOutputBytes     int
}

type Adapter struct {
	baseURL            *url.URL
	transcriptionModel string
	speechModel        string
	credential         CredentialProvider
	httpClient         *http.Client
	maxInputBytes      int
	maxOutputBytes     int
}

func New(options Options) (*Adapter, error) {
	baseURL, err := parseBaseURL(options.BaseURL)
	if err != nil {
		return nil, err
	}
	if options.TranscriptionModel == "" {
		return nil, errors.New("voice adapter: transcription model is required")
	}
	if options.SpeechModel == "" {
		return nil, errors.New("voice adapter: speech model is required")
	}
	if options.Credential == nil {
		return nil, errors.New("voice adapter: credential provider is required")
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	maxInput := options.MaxInputBytes
	if maxInput <= 0 {
		maxInput = defaultMaxInputBytes
	}
	maxOutput := options.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = defaultMaxOutputBytes
	}
	return &Adapter{
		baseURL: baseURL, transcriptionModel: options.TranscriptionModel,
		speechModel: options.SpeechModel, credential: options.Credential,
		httpClient: client, maxInputBytes: maxInput, maxOutputBytes: maxOutput,
	}, nil
}

func (adapter *Adapter) Transcribe(ctx context.Context, input voice.AudioInput) (voice.Transcript, error) {
	if len(input.Data) == 0 {
		return voice.Transcript{}, errors.New("transcribe: audio is empty")
	}
	if len(input.Data) > adapter.maxInputBytes {
		return voice.Transcript{}, fmt.Errorf("transcribe: audio exceeds %d byte limit", adapter.maxInputBytes)
	}
	filename := input.Filename
	if filename == "" {
		filename = "recording.webm"
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	partHeader := make(map[string][]string)
	partHeader["Content-Disposition"] = []string{fmt.Sprintf(`form-data; name="file"; filename=%q`, filename)}
	if input.ContentType != "" {
		partHeader["Content-Type"] = []string{input.ContentType}
	}
	part, err := writer.CreatePart(textprotoHeader(partHeader))
	if err != nil {
		return voice.Transcript{}, fmt.Errorf("transcribe: create audio part: %w", err)
	}
	if _, err := part.Write(input.Data); err != nil {
		return voice.Transcript{}, fmt.Errorf("transcribe: write audio part: %w", err)
	}
	_ = writer.WriteField("model", adapter.transcriptionModel)
	_ = writer.WriteField("response_format", "json")
	if input.Language != "" {
		_ = writer.WriteField("language", input.Language)
	}
	if err := writer.Close(); err != nil {
		return voice.Transcript{}, fmt.Errorf("transcribe: close multipart body: %w", err)
	}
	request, err := adapter.newRequest(ctx, http.MethodPost, "audio/transcriptions", &body)
	if err != nil {
		return voice.Transcript{}, err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := adapter.httpClient.Do(request)
	if err != nil {
		return voice.Transcript{}, fmt.Errorf("transcribe request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return voice.Transcript{}, fmt.Errorf("transcribe provider returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		Text     string `json:"text"`
		Language string `json:"language"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, int64(adapter.maxOutputBytes)+1))
	if err := decoder.Decode(&payload); err != nil {
		return voice.Transcript{}, fmt.Errorf("decode transcription: %w", err)
	}
	if strings.TrimSpace(payload.Text) == "" {
		return voice.Transcript{}, errors.New("transcribe provider returned empty text")
	}
	return voice.Transcript{Text: payload.Text, Language: payload.Language}, nil
}

func (adapter *Adapter) Synthesize(ctx context.Context, requestValue voice.SpeechRequest) (voice.AudioOutput, error) {
	if strings.TrimSpace(requestValue.Text) == "" {
		return voice.AudioOutput{}, errors.New("synthesize: text is empty")
	}
	if requestValue.Voice == "" {
		return voice.AudioOutput{}, errors.New("synthesize: voice is required")
	}
	format := requestValue.Format
	if format == "" {
		format = "mp3"
	}
	payload := map[string]any{
		"model": adapter.speechModel, "input": requestValue.Text,
		"voice": requestValue.Voice, "response_format": format,
	}
	if requestValue.Instructions != "" {
		payload["instructions"] = requestValue.Instructions
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return voice.AudioOutput{}, fmt.Errorf("encode speech request: %w", err)
	}
	request, err := adapter.newRequest(ctx, http.MethodPost, "audio/speech", bytes.NewReader(body))
	if err != nil {
		return voice.AudioOutput{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := adapter.httpClient.Do(request)
	if err != nil {
		return voice.AudioOutput{}, fmt.Errorf("speech request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return voice.AudioOutput{}, fmt.Errorf("speech provider returned HTTP %d", response.StatusCode)
	}
	audio, err := io.ReadAll(io.LimitReader(response.Body, int64(adapter.maxOutputBytes)+1))
	if err != nil {
		return voice.AudioOutput{}, fmt.Errorf("read speech audio: %w", err)
	}
	if len(audio) > adapter.maxOutputBytes {
		return voice.AudioOutput{}, fmt.Errorf("speech output exceeds %d byte limit", adapter.maxOutputBytes)
	}
	if len(audio) == 0 {
		return voice.AudioOutput{}, errors.New("speech provider returned empty audio")
	}
	contentType := response.Header.Get("Content-Type")
	if contentType == "" {
		contentType = contentTypeFor(format)
	}
	return voice.AudioOutput{Data: audio, ContentType: contentType, Format: format}, nil
}

func (adapter *Adapter) newRequest(ctx context.Context, method, suffix string, body io.Reader) (*http.Request, error) {
	credential, err := adapter.credential(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve voice credential: %w", err)
	}
	if credential == "" {
		return nil, errors.New("resolve voice credential: empty credential")
	}
	endpoint := *adapter.baseURL
	endpoint.Path = path.Join(endpoint.Path, suffix)
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create voice request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set("Accept", "application/json, audio/*")
	return request, nil
}

func parseBaseURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, errors.New("voice adapter: base URL is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return nil, errors.New("voice adapter: invalid base URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("voice adapter: base URL must not contain credentials, query, or fragment")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopback(parsed.Hostname())) {
		return nil, errors.New("voice adapter: HTTPS is required outside localhost")
	}
	return parsed, nil
}

func isLoopback(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

func contentTypeFor(format string) string {
	switch format {
	case "wav":
		return "audio/wav"
	case "opus":
		return "audio/opus"
	case "aac":
		return "audio/aac"
	case "flac":
		return "audio/flac"
	default:
		return "audio/mpeg"
	}
}
