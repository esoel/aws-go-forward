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
	validByID := Config{
		Profile:    "default",
		Region:     "us-east-1",
		InstanceID: "i-1234567890",
		LocalPort:  3306,
		RemoteHost: "db.internal",
		RemotePort: 3306,
	}

	tests := []struct {
		name    string
		cfg     Config
		wantErr error
	}{
		{name: "valid", cfg: valid},
		{name: "valid with instance id", cfg: validByID},
		{name: "missing profile", cfg: Config{Region: valid.Region, InstanceName: valid.InstanceName, LocalPort: valid.LocalPort, RemoteHost: valid.RemoteHost, RemotePort: valid.RemotePort}, wantErr: ErrMissingProfile},
		{name: "whitespace profile", cfg: Config{Profile: "   ", Region: valid.Region, InstanceName: valid.InstanceName, LocalPort: valid.LocalPort, RemoteHost: valid.RemoteHost, RemotePort: valid.RemotePort}, wantErr: ErrMissingProfile},
		{name: "missing region", cfg: Config{Profile: valid.Profile, InstanceName: valid.InstanceName, LocalPort: valid.LocalPort, RemoteHost: valid.RemoteHost, RemotePort: valid.RemotePort}, wantErr: ErrMissingRegion},
		{name: "missing instance selector", cfg: Config{Profile: valid.Profile, Region: valid.Region, LocalPort: valid.LocalPort, RemoteHost: valid.RemoteHost, RemotePort: valid.RemotePort}, wantErr: ErrMissingInstanceSelector},
		{name: "both instance selectors set", cfg: Config{Profile: valid.Profile, Region: valid.Region, InstanceName: valid.InstanceName, InstanceID: "i-1234567890", LocalPort: valid.LocalPort, RemoteHost: valid.RemoteHost, RemotePort: valid.RemotePort}, wantErr: ErrConflictingInstanceSelectors},
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
		InstanceID:   "",
		LocalPort:    3306,
		RemoteHost:   "db-from-config.internal",
		RemotePort:   3306,
	}

	cli := Config{
		Profile:      "profile-from-cli",
		Region:       "eu-west-1",
		InstanceName: "instance-from-cli",
		InstanceID:   "i-from-cli",
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
				InstanceID:   "",
				LocalPort:    5432,
				RemoteHost:   "db-from-config.internal",
				RemotePort:   3306,
			},
		},
		{
			name:     "all config-related flags override baseline",
			setFlags: map[string]bool{"profile": true, "region": true, "instance-name": true, "local-port": true, "remote-host": true, "remote-port": true},
			want: Config{
				Profile:      "profile-from-cli",
				Region:       "eu-west-1",
				InstanceName: "instance-from-cli",
				InstanceID:   "",
				LocalPort:    5432,
				RemoteHost:   "db-from-cli.internal",
				RemotePort:   5432,
			},
		},
		{
			name:     "instance-id override clears config instance-name",
			setFlags: map[string]bool{"instance-id": true},
			want: Config{
				Profile:      "profile-from-config",
				Region:       "us-east-1",
				InstanceName: "",
				InstanceID:   "i-from-cli",
				LocalPort:    3306,
				RemoteHost:   "db-from-config.internal",
				RemotePort:   3306,
			},
		},
		{
			name: "instance-name override clears config instance-id",
			setFlags: map[string]bool{
				"instance-name": true,
			},
			want: Config{
				Profile:      "profile-from-config",
				Region:       "us-east-1",
				InstanceName: "instance-from-cli",
				InstanceID:   "",
				LocalPort:    3306,
				RemoteHost:   "db-from-config.internal",
				RemotePort:   3306,
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
	fs.StringVar(&cliCfg.InstanceID, "instance-id", "", "Instance ID used for forwarding")
	fs.IntVar(&cliCfg.LocalPort, "local-port", 0, "Local port")
	fs.StringVar(&cliCfg.RemoteHost, "remote-host", "", "Remote host")
	fs.IntVar(&cliCfg.RemotePort, "remote-port", 0, "Remote port")

	err := fs.Parse([]string{"--config", "cfg.ini", "--profile", "cli-profile", "--local-port", "15432", "--instance-id", "i-cli"})
	if err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	base := Config{
		Profile:      "cfg-profile",
		Region:       "us-east-1",
		InstanceName: "cfg-instance",
		InstanceID:   "",
		LocalPort:    3306,
		RemoteHost:   "cfg-host",
		RemotePort:   3306,
	}

	got := mergeConfigWithCLIOverrides(base, cliCfg, collectSetFlags(fs))
	want := Config{
		Profile:      "cli-profile",
		Region:       "us-east-1",
		InstanceName: "",
		InstanceID:   "i-cli",
		LocalPort:    15432,
		RemoteHost:   "cfg-host",
		RemotePort:   3306,
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged config = %+v, want %+v", got, want)
	}
}

func TestResolveInstanceIDByName(t *testing.T) {
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

		got, err := getInstanceIDByName(context.Background(), client, "bastion", false, func(_ int) (int, error) {
			return 0, nil
		})
		if err != nil {
			t.Fatalf("getInstanceIDByName() unexpected error: %v", err)
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

	t.Run("returns error when no reservations exist", func(t *testing.T) {
		t.Parallel()

		client := &fakeEC2Client{
			output: &ec2.DescribeInstancesOutput{
				Reservations: []ec2types.Reservation{},
			},
		}

		_, err := getInstanceIDByName(context.Background(), client, "bastion", false, func(_ int) (int, error) {
			return 0, nil
		})
		if !errors.Is(err, ErrNoRunningInstances) {
			t.Fatalf("expected %v, got %v", ErrNoRunningInstances, err)
		}
	})

	t.Run("returns error for nil instance state", func(t *testing.T) {
		t.Parallel()

		client := &fakeEC2Client{
			output: &ec2.DescribeInstancesOutput{
				Reservations: []ec2types.Reservation{{
					Instances: []ec2types.Instance{{
						InstanceId: aws.String("i-unknown"),
						State:      nil,
					}},
				}},
			},
		}

		_, err := getInstanceIDByName(context.Background(), client, "bastion", false, func(_ int) (int, error) {
			return 0, nil
		})
		if !errors.Is(err, ErrInvalidInstanceState) {
			t.Fatalf("expected %v, got %v", ErrInvalidInstanceState, err)
		}
	})

	t.Run("returns error for running instance with nil instance id", func(t *testing.T) {
		t.Parallel()

		client := &fakeEC2Client{
			output: &ec2.DescribeInstancesOutput{
				Reservations: []ec2types.Reservation{{
					Instances: []ec2types.Instance{{
						InstanceId: nil,
						State:      &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
					}},
				}},
			},
		}

		_, err := getInstanceIDByName(context.Background(), client, "bastion", false, func(_ int) (int, error) {
			return 0, nil
		})
		if !errors.Is(err, ErrMissingInstanceID) {
			t.Fatalf("expected %v, got %v", ErrMissingInstanceID, err)
		}
	})

	t.Run("skips malformed entries when valid running instance exists", func(t *testing.T) {
		t.Parallel()

		client := &fakeEC2Client{
			output: &ec2.DescribeInstancesOutput{
				Reservations: []ec2types.Reservation{
					{
						Instances: []ec2types.Instance{
							{InstanceId: aws.String("i-bad-state"), State: nil},
							{InstanceId: nil, State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}},
							{InstanceId: aws.String("i-good"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}},
						},
					},
				},
			},
		}

		got, err := getInstanceIDByName(context.Background(), client, "bastion", false, func(_ int) (int, error) {
			return 0, nil
		})
		if err != nil {
			t.Fatalf("getInstanceIDByName() unexpected error: %v", err)
		}
		if got != "i-good" {
			t.Fatalf("instance id = %q, want %q", got, "i-good")
		}
	})

	t.Run("handles multiple reservations with one running match", func(t *testing.T) {
		t.Parallel()

		client := &fakeEC2Client{
			output: &ec2.DescribeInstancesOutput{
				Reservations: []ec2types.Reservation{
					{
						Instances: []ec2types.Instance{
							{InstanceId: aws.String("i-stopped-1"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopped}},
						},
					},
					{
						Instances: []ec2types.Instance{
							{InstanceId: aws.String("i-running-1"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}},
						},
					},
				},
			},
		}

		got, err := getInstanceIDByName(context.Background(), client, "bastion", false, func(_ int) (int, error) {
			return 0, nil
		})
		if err != nil {
			t.Fatalf("getInstanceIDByName() unexpected error: %v", err)
		}
		if got != "i-running-1" {
			t.Fatalf("instance id = %q, want %q", got, "i-running-1")
		}
	})

	t.Run("errors on multiple running matches without any", func(t *testing.T) {
		t.Parallel()

		client := &fakeEC2Client{
			output: &ec2.DescribeInstancesOutput{
				Reservations: []ec2types.Reservation{
					{
						Instances: []ec2types.Instance{
							{InstanceId: aws.String("i-running-1"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}},
							{InstanceId: aws.String("i-running-2"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}},
						},
					},
				},
			},
		}

		_, err := getInstanceIDByName(context.Background(), client, "bastion", false, func(_ int) (int, error) {
			return 0, nil
		})
		if !errors.Is(err, ErrMultipleRunningInstances) {
			t.Fatalf("expected %v, got %v", ErrMultipleRunningInstances, err)
		}
	})

	t.Run("allows multiple running matches when any is set", func(t *testing.T) {
		t.Parallel()

		client := &fakeEC2Client{
			output: &ec2.DescribeInstancesOutput{
				Reservations: []ec2types.Reservation{
					{
						Instances: []ec2types.Instance{
							{InstanceId: aws.String("i-running-1"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}},
							{InstanceId: aws.String("i-running-2"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}},
						},
					},
				},
			},
		}
		chooserCalled := false

		got, err := getInstanceIDByName(context.Background(), client, "bastion", true, func(n int) (int, error) {
			chooserCalled = true
			if n != 2 {
				t.Fatalf("chooser n = %d, want 2", n)
			}
			return 1, nil
		})
		if err != nil {
			t.Fatalf("getInstanceIDByName() unexpected error: %v", err)
		}
		if !chooserCalled {
			t.Fatal("chooser was not called")
		}
		if got != "i-running-2" {
			t.Fatalf("instance id = %q, want %q", got, "i-running-2")
		}
	})

	t.Run("propagates API error", func(t *testing.T) {
		t.Parallel()

		wantErr := errors.New("boom")
		client := &fakeEC2Client{err: wantErr}

		_, err := getInstanceIDByName(context.Background(), client, "bastion", false, func(_ int) (int, error) {
			return 0, nil
		})
		if !errors.Is(err, wantErr) {
			t.Fatalf("expected wrapped error %v, got %v", wantErr, err)
		}
	})
}

func TestGetInstanceIDByID(t *testing.T) {
	t.Parallel()

	t.Run("returns running instance and builds expected instance-id query", func(t *testing.T) {
		t.Parallel()

		client := &fakeEC2Client{
			output: &ec2.DescribeInstancesOutput{
				Reservations: []ec2types.Reservation{
					{
						Instances: []ec2types.Instance{
							{InstanceId: aws.String("i-target"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}},
						},
					},
				},
			},
		}

		got, err := getInstanceIDByID(context.Background(), client, "i-target")
		if err != nil {
			t.Fatalf("getInstanceIDByID() unexpected error: %v", err)
		}
		if got != "i-target" {
			t.Fatalf("instance id = %q, want %q", got, "i-target")
		}
		if client.gotInput == nil {
			t.Fatal("DescribeInstances input was not captured")
		}
		if len(client.gotInput.InstanceIds) != 1 || client.gotInput.InstanceIds[0] != "i-target" {
			t.Fatalf("instance IDs query = %v, want [i-target]", client.gotInput.InstanceIds)
		}
	})

	t.Run("returns not found when instance does not exist", func(t *testing.T) {
		t.Parallel()

		client := &fakeEC2Client{
			output: &ec2.DescribeInstancesOutput{
				Reservations: []ec2types.Reservation{},
			},
		}

		_, err := getInstanceIDByID(context.Background(), client, "i-target")
		if !errors.Is(err, ErrInstanceNotFound) {
			t.Fatalf("expected %v, got %v", ErrInstanceNotFound, err)
		}
	})

	t.Run("returns not running when instance is stopped", func(t *testing.T) {
		t.Parallel()

		client := &fakeEC2Client{
			output: &ec2.DescribeInstancesOutput{
				Reservations: []ec2types.Reservation{
					{
						Instances: []ec2types.Instance{
							{InstanceId: aws.String("i-target"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopped}},
						},
					},
				},
			},
		}

		_, err := getInstanceIDByID(context.Background(), client, "i-target")
		if !errors.Is(err, ErrInstanceNotRunning) {
			t.Fatalf("expected %v, got %v", ErrInstanceNotRunning, err)
		}
	})

	t.Run("returns error for nil instance state", func(t *testing.T) {
		t.Parallel()

		client := &fakeEC2Client{
			output: &ec2.DescribeInstancesOutput{
				Reservations: []ec2types.Reservation{
					{
						Instances: []ec2types.Instance{
							{InstanceId: aws.String("i-target"), State: nil},
						},
					},
				},
			},
		}

		_, err := getInstanceIDByID(context.Background(), client, "i-target")
		if !errors.Is(err, ErrInvalidInstanceState) {
			t.Fatalf("expected %v, got %v", ErrInvalidInstanceState, err)
		}
	})

	t.Run("returns error for nil instance id", func(t *testing.T) {
		t.Parallel()

		client := &fakeEC2Client{
			output: &ec2.DescribeInstancesOutput{
				Reservations: []ec2types.Reservation{
					{
						Instances: []ec2types.Instance{
							{InstanceId: nil, State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}},
						},
					},
				},
			},
		}

		_, err := getInstanceIDByID(context.Background(), client, "i-target")
		if !errors.Is(err, ErrMissingInstanceID) {
			t.Fatalf("expected %v, got %v", ErrMissingInstanceID, err)
		}
	})

	t.Run("propagates API error", func(t *testing.T) {
		t.Parallel()

		wantErr := errors.New("boom")
		client := &fakeEC2Client{err: wantErr}

		_, err := getInstanceIDByID(context.Background(), client, "i-target")
		if !errors.Is(err, wantErr) {
			t.Fatalf("expected wrapped error %v, got %v", wantErr, err)
		}
	})
}

func TestValidateSelectionOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      Config
		allowAny bool
		wantErr  error
	}{
		{
			name:     "any with instance id is invalid",
			cfg:      Config{InstanceID: "i-1234567890"},
			allowAny: true,
			wantErr:  ErrAnyRequiresInstanceName,
		},
		{
			name:     "any with instance name is valid",
			cfg:      Config{InstanceName: "bastion"},
			allowAny: true,
			wantErr:  nil,
		},
		{
			name:     "instance name without any is valid",
			cfg:      Config{InstanceName: "bastion"},
			allowAny: false,
			wantErr:  nil,
		},
		{
			name:     "instance id without any is valid",
			cfg:      Config{InstanceID: "i-1234567890"},
			allowAny: false,
			wantErr:  nil,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateSelectionOptions(tt.cfg, tt.allowAny)
			if tt.wantErr == nil && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestResolveInstanceID(t *testing.T) {
	t.Parallel()

	t.Run("uses instance id path when instance id is set", func(t *testing.T) {
		t.Parallel()

		client := &fakeEC2Client{
			output: &ec2.DescribeInstancesOutput{
				Reservations: []ec2types.Reservation{
					{
						Instances: []ec2types.Instance{
							{InstanceId: aws.String("i-target"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}},
						},
					},
				},
			},
		}

		got, err := resolveInstanceID(context.Background(), client, Config{InstanceID: "i-target"}, false)
		if err != nil {
			t.Fatalf("resolveInstanceID() unexpected error: %v", err)
		}
		if got != "i-target" {
			t.Fatalf("instance id = %q, want %q", got, "i-target")
		}
		if client.gotInput == nil {
			t.Fatal("DescribeInstances input was not captured")
		}
		if len(client.gotInput.InstanceIds) != 1 || client.gotInput.InstanceIds[0] != "i-target" {
			t.Fatalf("instance IDs query = %v, want [i-target]", client.gotInput.InstanceIds)
		}
	})

	t.Run("uses instance name path when instance id is empty", func(t *testing.T) {
		t.Parallel()

		client := &fakeEC2Client{
			output: &ec2.DescribeInstancesOutput{
				Reservations: []ec2types.Reservation{
					{
						Instances: []ec2types.Instance{
							{InstanceId: aws.String("i-running"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}},
						},
					},
				},
			},
		}

		got, err := resolveInstanceID(context.Background(), client, Config{InstanceName: "bastion"}, false)
		if err != nil {
			t.Fatalf("resolveInstanceID() unexpected error: %v", err)
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
}

func TestStartPortForwarding(t *testing.T) {
	t.Parallel()

	t.Run("builds expected StartSession request", func(t *testing.T) {
		t.Parallel()

		wantOutput := &ssm.StartSessionOutput{SessionId: aws.String("session-123")}
		client := &fakeSSMClient{output: wantOutput}

		got, err := startPortForwarding(context.Background(), client, "i-123", "db.internal", 3306, 3306)
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

		_, err := startPortForwarding(context.Background(), client, "i-123", "db.internal", 3306, 3306)
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
