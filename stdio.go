package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	pluginconfig "github.com/aws/session-manager-plugin/src/config"
	pluginlog "github.com/aws/session-manager-plugin/src/log"
	"github.com/aws/session-manager-plugin/src/message"
	"github.com/aws/session-manager-plugin/src/sessionmanagerplugin/session"
)

var (
	ErrStdioAny        = errors.New("--any cannot be used with --stdio")
	ErrStdioLocalPort  = errors.New("--local-port cannot be used with --stdio")
	ErrStdioRemoteHost = errors.New("--remote-host cannot be used with --stdio")
)

func isCredentialError(err error) bool {
	var missingProfile awsconfig.SharedConfigProfileNotExistError
	if errors.As(err, &missingProfile) {
		return true
	}
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		switch coded.ErrorCode() {
		case "ExpiredToken", "ExpiredTokenException", "InvalidClientTokenId", "UnrecognizedClientException":
			return true
		}
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "sso session has expired") ||
		strings.Contains(message, "cached sso token is expired") ||
		strings.Contains(message, "no valid credential sources") ||
		strings.Contains(message, "failed to refresh cached credentials")
}

func stdioExitCode(err error) int {
	switch {
	case errors.Is(err, ErrNoRunningInstances), errors.Is(err, ErrInstanceNotFound):
		return 3
	case errors.Is(err, ErrMultipleRunningInstances):
		return 4
	case isCredentialError(err):
		return 5
	default:
		return 6
	}
}

func stdioErrorMessage(err error, profile string) string {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "sso session has expired") || strings.Contains(message, "cached sso token is expired") {
		return fmt.Sprintf("AWS SSO credentials for profile %q expired; run aws sso login --profile %s", profile, profile)
	}
	return strings.NewReplacer("\n", " ", "\r", " ").Replace(err.Error())
}

func validateStdioConfig(cfg Config, allowAny bool) error {
	if err := cfg.validateCommon(); err != nil {
		return err
	}
	if allowAny {
		return ErrStdioAny
	}
	if cfg.LocalPort != 0 {
		return ErrStdioLocalPort
	}
	if strings.TrimSpace(cfg.RemoteHost) != "" {
		return ErrStdioRemoteHost
	}
	if cfg.RemotePort == 0 {
		return ErrMissingRemotePort
	}
	if cfg.RemotePort < 1 || cfg.RemotePort > 65535 {
		return ErrInvalidRemotePort
	}
	return nil
}

func startSSHSession(ctx context.Context, client ssmStartSessionAPI, instanceID string, remotePort int, documentName string) (*ssm.StartSessionOutput, error) {
	return client.StartSession(ctx, &ssm.StartSessionInput{
		Target:       aws.String(instanceID),
		DocumentName: aws.String(documentName),
		Parameters: map[string][]string{
			"portNumber": {fmt.Sprintf("%d", remotePort)},
		},
	})
}

func validateSSHResponse(response *ssm.StartSessionOutput) error {
	if response == nil || aws.ToString(response.SessionId) == "" || aws.ToString(response.StreamUrl) == "" || aws.ToString(response.TokenValue) == "" {
		return errors.New("incomplete SSH session response from AWS")
	}
	return nil
}

func runSSH(ctx context.Context, cfg Config, output io.Writer) error {
	awsCfg, err := createAWSSession(ctx, cfg.Profile, cfg.Region)
	if err != nil {
		return fmt.Errorf("load AWS configuration: %w", err)
	}
	instanceID, err := resolveInstanceID(ctx, ec2.NewFromConfig(awsCfg), cfg, false)
	if err != nil {
		return fmt.Errorf("resolve instance: %w", err)
	}
	ssmClient := ssm.NewFromConfig(awsCfg)
	response, err := startSSHSession(ctx, ssmClient, instanceID, cfg.RemotePort, cfg.documentName(true))
	if err != nil {
		return fmt.Errorf("start SSH session: %w", err)
	}
	if err := validateSSHResponse(response); err != nil {
		return err
	}
	endpoint := fmt.Sprintf("https://ssm.%s.amazonaws.com", cfg.Region)
	return runStdioSessionLifecycle(ctx, aws.ToString(response.SessionId), func() error {
		return startStdioPlugin(response, cfg.Region, cfg.Profile, instanceID, endpoint, output, session.ValidateInputAndStartSession)
	}, func(ctx context.Context, sessionID string) error {
		return terminatePortForwardingSession(ctx, ssmClient, sessionID)
	})
}

type stdioPortSession struct {
	session.Session
	input    io.Reader
	output   io.Writer
	stopOnce sync.Once
	stopped  atomic.Bool
}

func (p *stdioPortSession) Name() string { return pluginconfig.PortPluginName }

func (p *stdioPortSession) Initialize(log pluginlog.T, current *session.Session) {
	p.Session = *current
	p.DataChannel.RegisterOutputStreamHandler(p.ProcessStreamMessagePayload, true)
	p.DataChannel.GetWsChannel().SetOnMessage(func(raw []byte) {
		_ = p.DataChannel.OutputMessageHandler(log, p.Stop, p.SessionId, raw)
	})
}

func (p *stdioPortSession) SetSessionHandlers(log pluginlog.T) error {
	buffer := make([]byte, pluginconfig.StreamDataPayloadSize)
	for {
		n, err := p.input.Read(buffer)
		if n > 0 {
			if sendErr := p.DataChannel.SendInputDataMessage(log, message.Output, buffer[:n]); sendErr != nil {
				return sendErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || (p.stopped.Load() && errors.Is(err, os.ErrClosed)) {
				return nil
			}
			return err
		}
	}
}

func (p *stdioPortSession) ProcessStreamMessagePayload(_ pluginlog.T, outputMessage message.ClientMessage) (bool, error) {
	n, err := p.output.Write(outputMessage.Payload)
	if err == nil && n != len(outputMessage.Payload) {
		err = io.ErrShortWrite
	}
	return true, err
}

func (p *stdioPortSession) Stop() {
	p.stopOnce.Do(func() {
		p.stopped.Store(true)
		if closer, ok := p.input.(io.Closer); ok {
			_ = closer.Close()
		}
	})
}

func startStdioPlugin(response *ssm.StartSessionOutput, region, profile, instanceID, endpoint string, output io.Writer, runPlugin func([]string, io.Writer)) error {
	// The plugin writes banners and closure messages to os.Stdout. The stdio
	// handler sends only session payloads to the saved output stream.
	os.Stdout = os.Stderr
	session.Register(&stdioPortSession{input: os.Stdin, output: output})
	return startSessionManagerPluginBuiltin(response, region, profile, instanceID, endpoint, runPlugin)
}

func runStdioSessionLifecycle(ctx context.Context, sessionID string, startPlugin func() error, terminateSession func(context.Context, string) error) error {
	pluginDone := make(chan error, 1)
	go func() { pluginDone <- startPlugin() }()

	var pluginErr error
	canceled := false
	select {
	case pluginErr = <-pluginDone:
	case <-ctx.Done():
		canceled = true
	}

	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	terminateErr := terminateSession(cleanupCtx, sessionID)

	if canceled {
		select {
		case pluginErr = <-pluginDone:
		case <-cleanupCtx.Done():
			return errors.Join(ctx.Err(), terminateErr, errors.New("timed out waiting for SSH session to stop"))
		}
		return errors.Join(ctx.Err(), pluginErr, terminateErr)
	}
	return errors.Join(pluginErr, terminateErr)
}
