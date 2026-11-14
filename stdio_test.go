package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	pluginconfig "github.com/aws/session-manager-plugin/src/config"
	"github.com/aws/session-manager-plugin/src/datachannel"
	pluginlog "github.com/aws/session-manager-plugin/src/log"
	"github.com/aws/session-manager-plugin/src/message"
	"github.com/aws/session-manager-plugin/src/sessionmanagerplugin/session"
)

func TestValidateStdioConfig(t *testing.T) {
	t.Parallel()

	valid := Config{Profile: "default", Region: "eu-west-2", InstanceName: "web1", RemotePort: 22}
	tests := []struct {
		name     string
		cfg      Config
		allowAny bool
		wantErr  error
	}{
		{name: "name selector", cfg: valid},
		{name: "id selector", cfg: Config{Profile: "default", Region: "eu-west-2", InstanceID: "i-123", RemotePort: 22}},
		{name: "missing selector", cfg: Config{Profile: "default", Region: "eu-west-2", RemotePort: 22}, wantErr: ErrMissingInstanceSelector},
		{name: "missing port", cfg: Config{Profile: "default", Region: "eu-west-2", InstanceName: "web1"}, wantErr: ErrMissingRemotePort},
		{name: "invalid port", cfg: Config{Profile: "default", Region: "eu-west-2", InstanceName: "web1", RemotePort: 65536}, wantErr: ErrInvalidRemotePort},
		{name: "any is unsafe", cfg: valid, allowAny: true, wantErr: ErrStdioAny},
		{name: "local port is incompatible", cfg: Config{Profile: "default", Region: "eu-west-2", InstanceName: "web1", RemotePort: 22, LocalPort: 1234}, wantErr: ErrStdioLocalPort},
		{name: "remote host is incompatible", cfg: Config{Profile: "default", Region: "eu-west-2", InstanceName: "web1", RemotePort: 22, RemoteHost: "db.internal"}, wantErr: ErrStdioRemoteHost},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateStdioConfig(tt.cfg, tt.allowAny)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("validateStdioConfig() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestStartSSHSession(t *testing.T) {
	t.Parallel()

	want := &ssm.StartSessionOutput{SessionId: aws.String("session-123")}
	client := &fakeSSMClient{output: want}
	got, err := startSSHSession(context.Background(), client, "i-123", 22, "AWS-StartSSHSession")
	if err != nil || got != want {
		t.Fatalf("startSSHSession() = %v, %v; want output and no error", got, err)
	}
	if aws.ToString(client.gotInput.Target) != "i-123" || aws.ToString(client.gotInput.DocumentName) != "AWS-StartSSHSession" {
		t.Fatalf("StartSession input = %+v", client.gotInput)
	}
	if ports := client.gotInput.Parameters["portNumber"]; len(ports) != 1 || ports[0] != "22" {
		t.Fatalf("portNumber = %v, want [22]", ports)
	}
	if len(client.gotInput.Parameters) != 1 {
		t.Fatalf("parameters = %v, want only portNumber", client.gotInput.Parameters)
	}
}

func TestStartSSHSessionCustomDocument(t *testing.T) {
	t.Parallel()
	client := &fakeSSMClient{output: &ssm.StartSessionOutput{}}
	_, err := startSSHSession(context.Background(), client, "i-123", 22, "CustomSSHDocument")
	if err != nil {
		t.Fatal(err)
	}
	if got := aws.ToString(client.gotInput.DocumentName); got != "CustomSSHDocument" {
		t.Fatalf("document name = %q, want CustomSSHDocument", got)
	}
	if got := client.gotInput.Parameters["portNumber"]; len(got) != 1 || got[0] != "22" {
		t.Fatalf("portNumber = %v, want [22]", got)
	}
}

func TestValidateSSHResponse(t *testing.T) {
	t.Parallel()

	valid := &ssm.StartSessionOutput{
		SessionId:  aws.String("session-123"),
		StreamUrl:  aws.String("wss://example.invalid"),
		TokenValue: aws.String("token"),
	}
	tests := []struct {
		name     string
		response *ssm.StartSessionOutput
		wantErr  bool
	}{
		{name: "valid", response: valid},
		{name: "nil response", wantErr: true},
		{name: "missing session ID", response: &ssm.StartSessionOutput{StreamUrl: valid.StreamUrl, TokenValue: valid.TokenValue}, wantErr: true},
		{name: "missing stream URL", response: &ssm.StartSessionOutput{SessionId: valid.SessionId, TokenValue: valid.TokenValue}, wantErr: true},
		{name: "missing token", response: &ssm.StartSessionOutput{SessionId: valid.SessionId, StreamUrl: valid.StreamUrl}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateSSHResponse(tt.response)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateSSHResponse() = %v, want error: %t", err, tt.wantErr)
			}
		})
	}
}

type codedError string

func (e codedError) Error() string     { return string(e) }
func (e codedError) ErrorCode() string { return string(e) }

func TestStdioExitCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "not found by name", err: fmt.Errorf("resolve: %w", ErrNoRunningInstances), want: 3},
		{name: "not found by ID", err: fmt.Errorf("resolve: %w", ErrInstanceNotFound), want: 3},
		{name: "ambiguous", err: fmt.Errorf("resolve: %w", ErrMultipleRunningInstances), want: 4},
		{name: "expired token", err: fmt.Errorf("AWS: %w", codedError("ExpiredTokenException")), want: 5},
		{name: "missing profile", err: fmt.Errorf("AWS: %w", awsconfig.SharedConfigProfileNotExistError{Profile: "missing"}), want: 5},
		{name: "missing credentials", err: errors.New("failed to refresh cached credentials, no EC2 IMDS role found"), want: 5},
		{name: "session failure", err: errors.New("websocket closed"), want: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := stdioExitCode(tt.err); got != tt.want {
				t.Fatalf("stdioExitCode(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestStdioErrorMessageSuggestsSSOLogin(t *testing.T) {
	t.Parallel()

	err := errors.New("cached SSO token is expired, or not present, and cannot be refreshed")
	message := stdioErrorMessage(err, "staging")
	if !strings.Contains(message, "aws sso login --profile staging") {
		t.Fatalf("message = %q, want SSO login instruction", message)
	}
	if strings.Contains(message, "\n") {
		t.Fatalf("message = %q, want one line", message)
	}
}

type recordingDataChannel struct {
	datachannel.IDataChannel
	payloads [][]byte
}

func (f *recordingDataChannel) SendInputDataMessage(_ pluginlog.T, _ message.PayloadType, payload []byte) error {
	f.payloads = append(f.payloads, bytes.Clone(payload))
	return nil
}

func TestStdioPortSessionCopiesRawBytes(t *testing.T) {
	t.Parallel()

	payload := []byte{'S', 'S', 'H', '-', '2', '.', '0', '-', 0, 255, '\n'}
	channel := &recordingDataChannel{}
	var stdout bytes.Buffer
	port := &stdioPortSession{
		Session: session.Session{DataChannel: channel},
		input:   bytes.NewReader(payload),
		output:  &stdout,
	}
	if err := port.SetSessionHandlers(nil); err != nil {
		t.Fatalf("SetSessionHandlers() unexpected error: %v", err)
	}
	if len(channel.payloads) != 1 || !bytes.Equal(channel.payloads[0], payload) {
		t.Fatalf("sent payloads = %v, want %v", channel.payloads, payload)
	}
	ready, err := port.ProcessStreamMessagePayload(nil, message.ClientMessage{Payload: payload})
	if err != nil || !ready || !bytes.Equal(stdout.Bytes(), payload) {
		t.Fatalf("output = %v, ready = %t, err = %v", stdout.Bytes(), ready, err)
	}
}

func TestStdioPortSessionRejectsShortWrites(t *testing.T) {
	t.Parallel()

	port := &stdioPortSession{output: shortWriter{}}
	_, err := port.ProcessStreamMessagePayload(nil, message.ClientMessage{Payload: []byte("SSH")})
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("ProcessStreamMessagePayload() = %v, want short write", err)
	}
}

func TestStdioPortSessionStopUnblocksInput(t *testing.T) {
	t.Parallel()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	port := &stdioPortSession{Session: session.Session{DataChannel: &recordingDataChannel{}}, input: reader}
	done := make(chan error, 1)
	go func() { done <- port.SetSessionHandlers(nil) }()
	port.Stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetSessionHandlers() = %v after Stop", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop did not unblock stdin")
	}
}

func TestStartStdioPluginKeepsBannerOffStream(t *testing.T) {
	originalStdout := os.Stdout
	originalPortHandler := session.SessionRegistry[pluginconfig.PortPluginName]
	defer func() {
		os.Stdout = originalStdout
		session.Register(originalPortHandler)
	}()

	var stream bytes.Buffer
	payload := []byte{'S', 'S', 'H', '-', 0, 255}
	response := &ssm.StartSessionOutput{SessionId: aws.String("session-123")}
	err := startStdioPlugin(response, "eu-west-2", "default", "i-123", "https://ssm.eu-west-2.amazonaws.com", &stream, func(_ []string, _ io.Writer) {
		fmt.Fprint(os.Stdout, "plugin banner")
		port, ok := session.SessionRegistry[pluginconfig.PortPluginName].(*stdioPortSession)
		if !ok {
			t.Error("stdio port handler was not registered")
			return
		}
		_, _ = port.ProcessStreamMessagePayload(nil, message.ClientMessage{Payload: payload})
	})
	if err != nil {
		t.Fatalf("startStdioPlugin() = %v", err)
	}
	if !bytes.Equal(stream.Bytes(), payload) {
		t.Fatalf("stream = %v, want %v", stream.Bytes(), payload)
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestRunStdioSessionLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("EOF terminates session", func(t *testing.T) {
		t.Parallel()
		terminated := false
		err := runStdioSessionLifecycle(context.Background(), "session-123", func() error { return nil }, func(_ context.Context, id string) error {
			terminated = id == "session-123"
			return nil
		})
		if err != nil || !terminated {
			t.Fatalf("result = %v, terminated = %t", err, terminated)
		}
	})

	t.Run("plugin failure terminates session", func(t *testing.T) {
		t.Parallel()
		want := errors.New("plugin failed")
		terminated := false
		err := runStdioSessionLifecycle(context.Background(), "session-123", func() error { return want }, func(_ context.Context, _ string) error {
			terminated = true
			return nil
		})
		if !errors.Is(err, want) || !terminated {
			t.Fatalf("result = %v, terminated = %t", err, terminated)
		}
	})

	t.Run("cancellation terminates session", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		pluginDone := make(chan struct{})
		terminated := make(chan struct{})
		result := make(chan error, 1)
		go func() {
			result <- runStdioSessionLifecycle(ctx, "session-123", func() error { <-pluginDone; return nil }, func(_ context.Context, _ string) error {
				close(terminated)
				close(pluginDone)
				return nil
			})
		}()
		cancel()
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("runStdioSessionLifecycle() = %v, want cancellation", err)
			}
		case <-time.After(time.Second):
			t.Fatal("runStdioSessionLifecycle() did not return after cancellation")
		}
		select {
		case <-terminated:
		default:
			t.Fatal("session was not terminated")
		}
	})
}
