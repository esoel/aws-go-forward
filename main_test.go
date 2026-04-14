package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

type fakeEC2Client struct {
	output   *ec2.DescribeInstancesOutput
	err      error
	gotInput *ec2.DescribeInstancesInput
}

func (f *fakeEC2Client) DescribeInstances(_ context.Context, input *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.gotInput = input
	if f.err != nil {
		return nil, f.err
	}
	return f.output, nil
}

type fakeSSMClient struct {
	output   *ssm.StartSessionOutput
	err      error
	gotInput *ssm.StartSessionInput
}

func (f *fakeSSMClient) StartSession(_ context.Context, input *ssm.StartSessionInput, _ ...func(*ssm.Options)) (*ssm.StartSessionOutput, error) {
	f.gotInput = input
	if f.err != nil {
		return nil, f.err
	}
	return f.output, nil
}

func TestLoadConfigFromFile(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "settings.ini")
	content := strings.Join([]string{
		"[settings]",
		"profile = default",
		"region = us-east-1",
		"instance_name = my-ec2-instance",
		"local_port = 3306",
		"remote_host = db.internal",
		"remote_port = 3306",
		"use_builtin = true",
	}, "\n")

	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	cfg, err := loadConfigFromFile(configPath)
	if err != nil {
		t.Fatalf("loadConfigFromFile() unexpected error: %v", err)
	}

	if cfg.Profile != "default" {
		t.Fatalf("Profile = %q, want %q", cfg.Profile, "default")
	}
	if cfg.Region != "us-east-1" {
		t.Fatalf("Region = %q, want %q", cfg.Region, "us-east-1")
	}
	if cfg.InstanceName != "my-ec2-instance" {
		t.Fatalf("InstanceName = %q, want %q", cfg.InstanceName, "my-ec2-instance")
	}
	if cfg.LocalPort != 3306 {
		t.Fatalf("LocalPort = %d, want %d", cfg.LocalPort, 3306)
	}
	if cfg.RemoteHost != "db.internal" {
		t.Fatalf("RemoteHost = %q, want %q", cfg.RemoteHost, "db.internal")
	}
	if cfg.RemotePort != 3306 {
		t.Fatalf("RemotePort = %d, want %d", cfg.RemotePort, 3306)
	}
	if !cfg.UseBuiltin {
		t.Fatal("UseBuiltin = false, want true")
	}
}

func TestLoadConfigFromFileMissingFile(t *testing.T) {
	t.Parallel()

	_, err := loadConfigFromFile(filepath.Join(t.TempDir(), "does-not-exist.ini"))
	if err == nil {
		t.Fatal("expected error for missing config file, got nil")
	}
}

func TestLoadConfigFromFileMissingSettingsSection(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "settings.ini")
	content := strings.Join([]string{
		"[other]",
		"profile = default",
	}, "\n")

	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	_, err := loadConfigFromFile(configPath)
	if !errors.Is(err, ErrMissingSettingsSection) {
		t.Fatalf("expected %v, got %v", ErrMissingSettingsSection, err)
	}
}

func TestLoadConfigFromFileMalformedINI(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "settings.ini")
	content := "[settings\nprofile = default"

	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	_, err := loadConfigFromFile(configPath)
	if err == nil {
		t.Fatal("expected parse error for malformed INI, got nil")
	}
}

func TestLoadConfigFromFileInvalidValueType(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "settings.ini")
	content := strings.Join([]string{
		"[settings]",
		"profile = default",
		"region = us-east-1",
		"instance_name = my-ec2-instance",
		"local_port = not-a-number",
		"remote_host = db.internal",
		"remote_port = 3306",
	}, "\n")

	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	_, err := loadConfigFromFile(configPath)
	if err == nil {
		t.Fatal("expected parse error for invalid local_port type, got nil")
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	valid := Config{
		Profile:      "default",
		Region:       "us-east-1",
		InstanceName: "bastion",
		LocalPort:    3306,
		RemoteHost:   "db.internal",
		RemotePort:   3306,
	}

	tests := []struct {
		name    string
		cfg     Config
		wantErr error
	}{
		{name: "valid", cfg: valid},
		{name: "missing profile", cfg: Config{Region: valid.Region, InstanceName: valid.InstanceName, LocalPort: valid.LocalPort, RemoteHost: valid.RemoteHost, RemotePort: valid.RemotePort}, wantErr: ErrMissingProfile},
		{name: "whitespace profile", cfg: Config{Profile: "   ", Region: valid.Region, InstanceName: valid.InstanceName, LocalPort: valid.LocalPort, RemoteHost: valid.RemoteHost, RemotePort: valid.RemotePort}, wantErr: ErrMissingProfile},
		{name: "missing region", cfg: Config{Profile: valid.Profile, InstanceName: valid.InstanceName, LocalPort: valid.LocalPort, RemoteHost: valid.RemoteHost, RemotePort: valid.RemotePort}, wantErr: ErrMissingRegion},
		{name: "missing instance", cfg: Config{Profile: valid.Profile, Region: valid.Region, LocalPort: valid.LocalPort, RemoteHost: valid.RemoteHost, RemotePort: valid.RemotePort}, wantErr: ErrMissingInstanceName},
		{name: "missing local port", cfg: Config{Profile: valid.Profile, Region: valid.Region, InstanceName: valid.InstanceName, RemoteHost: valid.RemoteHost, RemotePort: valid.RemotePort}, wantErr: ErrMissingLocalPort},
		{name: "invalid local port low", cfg: Config{Profile: valid.Profile, Region: valid.Region, InstanceName: valid.InstanceName, LocalPort: -1, RemoteHost: valid.RemoteHost, RemotePort: valid.RemotePort}, wantErr: ErrInvalidLocalPort},
		{name: "invalid local port high", cfg: Config{Profile: valid.Profile, Region: valid.Region, InstanceName: valid.InstanceName, LocalPort: 70000, RemoteHost: valid.RemoteHost, RemotePort: valid.RemotePort}, wantErr: ErrInvalidLocalPort},
		{name: "missing remote host", cfg: Config{Profile: valid.Profile, Region: valid.Region, InstanceName: valid.InstanceName, LocalPort: valid.LocalPort, RemotePort: valid.RemotePort}, wantErr: ErrMissingRemoteHost},
		{name: "whitespace remote host", cfg: Config{Profile: valid.Profile, Region: valid.Region, InstanceName: valid.InstanceName, LocalPort: valid.LocalPort, RemoteHost: " \t ", RemotePort: valid.RemotePort}, wantErr: ErrMissingRemoteHost},
		{name: "missing remote port", cfg: Config{Profile: valid.Profile, Region: valid.Region, InstanceName: valid.InstanceName, LocalPort: valid.LocalPort, RemoteHost: valid.RemoteHost}, wantErr: ErrMissingRemotePort},
		{name: "invalid remote port low", cfg: Config{Profile: valid.Profile, Region: valid.Region, InstanceName: valid.InstanceName, LocalPort: valid.LocalPort, RemoteHost: valid.RemoteHost, RemotePort: -1}, wantErr: ErrInvalidRemotePort},
		{name: "invalid remote port high", cfg: Config{Profile: valid.Profile, Region: valid.Region, InstanceName: valid.InstanceName, LocalPort: valid.LocalPort, RemoteHost: valid.RemoteHost, RemotePort: 70000}, wantErr: ErrInvalidRemotePort},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if tt.wantErr == nil && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestMergeConfigWithCLIOverrides(t *testing.T) {
	t.Parallel()

	base := Config{
		Profile:      "profile-from-config",
		Region:       "us-east-1",
		InstanceName: "instance-from-config",
		LocalPort:    3306,
		RemoteHost:   "db-from-config.internal",
		RemotePort:   3306,
		UseBuiltin:   true,
	}

	cli := Config{
		Profile:      "profile-from-cli",
		Region:       "eu-west-1",
		InstanceName: "instance-from-cli",
		LocalPort:    5432,
		RemoteHost:   "db-from-cli.internal",
		RemotePort:   5432,
	}

	tests := []struct {
		name     string
		setFlags map[string]bool
		want     Config
	}{
		{
			name:     "no explicit CLI flags uses config baseline",
			setFlags: map[string]bool{},
			want:     base,
		},
		{
			name:     "explicit subset overrides only those fields",
			setFlags: map[string]bool{"profile": true, "local-port": true},
			want: Config{
				Profile:      "profile-from-cli",
				Region:       "us-east-1",
				InstanceName: "instance-from-config",
				LocalPort:    5432,
				RemoteHost:   "db-from-config.internal",
				RemotePort:   3306,
				UseBuiltin:   true,
			},
		},
		{
			name:     "all config-related flags override baseline",
			setFlags: map[string]bool{"profile": true, "region": true, "instance-name": true, "local-port": true, "remote-host": true, "remote-port": true},
			want: Config{
				Profile:      "profile-from-cli",
				Region:       "eu-west-1",
				InstanceName: "instance-from-cli",
				LocalPort:    5432,
				RemoteHost:   "db-from-cli.internal",
				RemotePort:   5432,
				UseBuiltin:   true,
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := mergeConfigWithCLIOverrides(base, cli, tt.setFlags)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("mergeConfigWithCLIOverrides() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestConfigPrecedenceWithParsedFlags(t *testing.T) {
	t.Parallel()

	fs := flag.NewFlagSet("aws-go-forward", flag.ContinueOnError)

	var configFile string
	var cliCfg Config

	fs.StringVar(&configFile, "config", "", "Path to configuration file in INI format (optional)")
	fs.StringVar(&cliCfg.Profile, "profile", "", "AWS profile name")
	fs.StringVar(&cliCfg.Region, "region", "", "AWS region")
	fs.StringVar(&cliCfg.InstanceName, "instance-name", "", "Name of the instance used for forwarding")
	fs.IntVar(&cliCfg.LocalPort, "local-port", 0, "Local port")
	fs.StringVar(&cliCfg.RemoteHost, "remote-host", "", "Remote host")
	fs.IntVar(&cliCfg.RemotePort, "remote-port", 0, "Remote port")

	err := fs.Parse([]string{"--config", "cfg.ini", "--profile", "cli-profile", "--local-port", "15432"})
	if err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	base := Config{
		Profile:      "cfg-profile",
		Region:       "us-east-1",
		InstanceName: "cfg-instance",
		LocalPort:    3306,
		RemoteHost:   "cfg-host",
		RemotePort:   3306,
	}

	got := mergeConfigWithCLIOverrides(base, cliCfg, collectSetFlags(fs))
	want := Config{
		Profile:      "cli-profile",
		Region:       "us-east-1",
		InstanceName: "cfg-instance",
		LocalPort:    15432,
		RemoteHost:   "cfg-host",
		RemotePort:   3306,
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged config = %+v, want %+v", got, want)
	}
}

func TestGetInstanceID(t *testing.T) {
	t.Parallel()

	t.Run("returns running instance and builds expected filter", func(t *testing.T) {
		t.Parallel()

		client := &fakeEC2Client{
			output: &ec2.DescribeInstancesOutput{
				Reservations: []ec2types.Reservation{
					{
						Instances: []ec2types.Instance{
							{InstanceId: aws.String("i-stopped"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopped}},
							{InstanceId: aws.String("i-running"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}},
						},
					},
				},
			},
		}

		got, err := getInstanceID(client, "bastion")
		if err != nil {
			t.Fatalf("getInstanceID() unexpected error: %v", err)
		}
		if got != "i-running" {
			t.Fatalf("instance id = %q, want %q", got, "i-running")
		}
		if client.gotInput == nil {
			t.Fatal("DescribeInstances input was not captured")
		}
		if len(client.gotInput.Filters) != 1 {
			t.Fatalf("filters length = %d, want 1", len(client.gotInput.Filters))
		}
		f := client.gotInput.Filters[0]
		if aws.ToString(f.Name) != "tag:Name" {
			t.Fatalf("filter name = %q, want %q", aws.ToString(f.Name), "tag:Name")
		}
		if len(f.Values) != 1 || f.Values[0] != "bastion" {
			t.Fatalf("filter values = %v, want [bastion]", f.Values)
		}
	})

	t.Run("returns error when no running instance exists", func(t *testing.T) {
		t.Parallel()

		client := &fakeEC2Client{
			output: &ec2.DescribeInstancesOutput{
				Reservations: []ec2types.Reservation{{
					Instances: []ec2types.Instance{{
						InstanceId: aws.String("i-stopped"),
						State:      &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopped},
					}},
				}},
			},
		}

		_, err := getInstanceID(client, "bastion")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "No running aws instances found") {
			t.Fatalf("error = %q, expected missing-running-instance message", err.Error())
		}
	})

	t.Run("propagates API error", func(t *testing.T) {
		t.Parallel()

		wantErr := errors.New("boom")
		client := &fakeEC2Client{err: wantErr}

		_, err := getInstanceID(client, "bastion")
		if !errors.Is(err, wantErr) {
			t.Fatalf("expected wrapped error %v, got %v", wantErr, err)
		}
	})
}

func TestStartPortForwarding(t *testing.T) {
	t.Parallel()

	t.Run("builds expected StartSession request", func(t *testing.T) {
		t.Parallel()

		wantOutput := &ssm.StartSessionOutput{SessionId: aws.String("session-123")}
		client := &fakeSSMClient{output: wantOutput}

		got, err := startPortForwarding(client, "i-123", "db.internal", 3306, 3306)
		if err != nil {
			t.Fatalf("startPortForwarding() unexpected error: %v", err)
		}
		if got != wantOutput {
			t.Fatal("startPortForwarding() did not return API output")
		}
		if client.gotInput == nil {
			t.Fatal("StartSession input was not captured")
		}
		if aws.ToString(client.gotInput.Target) != "i-123" {
			t.Fatalf("target = %q, want %q", aws.ToString(client.gotInput.Target), "i-123")
		}
		if aws.ToString(client.gotInput.DocumentName) != "AWS-StartPortForwardingSessionToRemoteHost" {
			t.Fatalf("document name = %q, want %q", aws.ToString(client.gotInput.DocumentName), "AWS-StartPortForwardingSessionToRemoteHost")
		}
		if gotHost := client.gotInput.Parameters["host"]; len(gotHost) != 1 || gotHost[0] != "db.internal" {
			t.Fatalf("host parameter = %v, want [db.internal]", gotHost)
		}
		if gotLocalPort := client.gotInput.Parameters["localPortNumber"]; len(gotLocalPort) != 1 || gotLocalPort[0] != "3306" {
			t.Fatalf("localPortNumber parameter = %v, want [3306]", gotLocalPort)
		}
		if gotPort := client.gotInput.Parameters["portNumber"]; len(gotPort) != 1 || gotPort[0] != "3306" {
			t.Fatalf("portNumber parameter = %v, want [3306]", gotPort)
		}
	})

	t.Run("propagates API error", func(t *testing.T) {
		t.Parallel()

		wantErr := errors.New("ssm down")
		client := &fakeSSMClient{err: wantErr}

		_, err := startPortForwarding(client, "i-123", "db.internal", 3306, 3306)
		if !errors.Is(err, wantErr) {
			t.Fatalf("expected wrapped error %v, got %v", wantErr, err)
		}
	})
}

func TestKeepAliveStopsWhenSignaled(t *testing.T) {
	t.Parallel()

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		KeepAlive(65535, stop)
		close(done)
	}()

	close(stop)

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("KeepAlive did not stop after stop channel closed")
	}
}

func TestRunSessionLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("cancellation terminates session and stops keepalive", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		allowPluginExit := make(chan struct{})
		pluginExited := make(chan struct{})
		keepAliveStopped := make(chan struct{})
		terminateCalled := make(chan struct{}, 1)

		startPlugin := func() error {
			defer close(pluginExited)
			<-allowPluginExit
			return nil
		}
		terminateSession := func(_ context.Context, sessionID string) error {
			if sessionID != "session-123" {
				t.Fatalf("sessionID = %q, want %q", sessionID, "session-123")
			}
			select {
			case terminateCalled <- struct{}{}:
			default:
			}
			close(allowPluginExit)
			return nil
		}
		keepAliveFn := func(_ int, stopChan <-chan struct{}) {
			<-stopChan
			close(keepAliveStopped)
		}

		done := make(chan error, 1)
		go func() {
			done <- runSessionLifecycle(ctx, 3306, "session-123", startPlugin, terminateSession, keepAliveFn)
		}()

		cancel()

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("runSessionLifecycle() unexpected error: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("runSessionLifecycle() did not return after cancellation")
		}

		select {
		case <-terminateCalled:
		default:
			t.Fatal("terminateSession was not called")
		}

		select {
		case <-keepAliveStopped:
		default:
			t.Fatal("keepalive did not stop")
		}

		select {
		case <-pluginExited:
		default:
			t.Fatal("plugin did not exit")
		}
	})

	t.Run("plugin error is returned and triggers termination", func(t *testing.T) {
		t.Parallel()

		wantErr := errors.New("plugin failed")
		keepAliveStopped := make(chan struct{})
		terminateCalled := make(chan struct{}, 1)

		startPlugin := func() error {
			return wantErr
		}
		terminateSession := func(_ context.Context, sessionID string) error {
			if sessionID != "session-123" {
				t.Fatalf("sessionID = %q, want %q", sessionID, "session-123")
			}
			terminateCalled <- struct{}{}
			return nil
		}
		keepAliveFn := func(_ int, stopChan <-chan struct{}) {
			<-stopChan
			close(keepAliveStopped)
		}

		err := runSessionLifecycle(context.Background(), 3306, "session-123", startPlugin, terminateSession, keepAliveFn)
		if !errors.Is(err, wantErr) {
			t.Fatalf("expected %v, got %v", wantErr, err)
		}

		select {
		case <-terminateCalled:
		default:
			t.Fatal("terminateSession was not called")
		}

		select {
		case <-keepAliveStopped:
		default:
			t.Fatal("keepalive did not stop")
		}
	})

	t.Run("terminate session failure is returned", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		terminateErr := errors.New("terminate failed")
		allowPluginExit := make(chan struct{})

		startPlugin := func() error {
			<-allowPluginExit
			return nil
		}
		terminateSession := func(_ context.Context, _ string) error {
			close(allowPluginExit)
			return terminateErr
		}
		keepAliveFn := func(_ int, stopChan <-chan struct{}) {
			<-stopChan
		}

		done := make(chan error, 1)
		go func() {
			done <- runSessionLifecycle(ctx, 3306, "session-123", startPlugin, terminateSession, keepAliveFn)
		}()

		cancel()

		select {
		case err := <-done:
			if !errors.Is(err, terminateErr) {
				t.Fatalf("expected %v, got %v", terminateErr, err)
			}
		case <-time.After(time.Second):
			t.Fatal("runSessionLifecycle() did not return")
		}
	})
}
