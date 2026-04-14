package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/session-manager-plugin/src/sessionmanagerplugin/session"
	_ "github.com/aws/session-manager-plugin/src/sessionmanagerplugin/session"
	_ "github.com/aws/session-manager-plugin/src/sessionmanagerplugin/session/portsession"
	"gopkg.in/ini.v1"
)

type Config struct {
	Profile      string `ini:"profile"`
	Region       string `ini:"region"`
	InstanceName string `ini:"instance_name"`
	InstanceID   string `ini:"instance_id"`
	LocalPort    int    `ini:"local_port"`
	RemoteHost   string `ini:"remote_host"`
	RemotePort   int    `ini:"remote_port"`
}

var (
	ErrMissingSettingsSection       = errors.New("missing [settings] section")
	ErrMissingProfile               = errors.New("missing profile")
	ErrMissingRegion                = errors.New("missing region")
	ErrMissingInstanceSelector      = errors.New("missing instance selector")
	ErrConflictingInstanceSelectors = errors.New("instance name and instance id are mutually exclusive")
	ErrAnyRequiresInstanceName      = errors.New("any mode requires instance name selection")
	ErrMissingLocalPort             = errors.New("missing local port")
	ErrInvalidLocalPort             = errors.New("invalid local port")
	ErrMissingRemoteHost            = errors.New("missing remote host")
	ErrMissingRemotePort            = errors.New("missing remote port")
	ErrInvalidRemotePort            = errors.New("invalid remote port")
	ErrNoRunningInstances           = errors.New("no running instances found")
	ErrMultipleRunningInstances     = errors.New("multiple running instances found")
	ErrInvalidInstanceState         = errors.New("instance has nil state")
	ErrMissingInstanceID            = errors.New("instance has nil id")
	ErrInstanceNotFound             = errors.New("instance not found")
	ErrInstanceNotRunning           = errors.New("instance is not running")
)

func (c Config) Validate() error {
	if strings.TrimSpace(c.Profile) == "" {
		return ErrMissingProfile
	}
	if strings.TrimSpace(c.Region) == "" {
		return ErrMissingRegion
	}
	instanceName := strings.TrimSpace(c.InstanceName)
	instanceID := strings.TrimSpace(c.InstanceID)
	if instanceName == "" && instanceID == "" {
		return ErrMissingInstanceSelector
	}
	if instanceName != "" && instanceID != "" {
		return ErrConflictingInstanceSelectors
	}
	if c.LocalPort == 0 {
		return ErrMissingLocalPort
	}
	if c.LocalPort < 1 || c.LocalPort > 65535 {
		return ErrInvalidLocalPort
	}
	if strings.TrimSpace(c.RemoteHost) == "" {
		return ErrMissingRemoteHost
	}
	if c.RemotePort == 0 {
		return ErrMissingRemotePort
	}
	if c.RemotePort < 1 || c.RemotePort > 65535 {
		return ErrInvalidRemotePort
	}
	return nil
}

func loadConfigFromFile(configFile string) (*Config, error) {
	cfg := &Config{}
	iniCfg, err := ini.Load(configFile)
	if err != nil {
		return nil, err
	}
	if !iniCfg.HasSection("settings") {
		return nil, ErrMissingSettingsSection
	}
	section := iniCfg.Section("settings")
	// Backward compatibility: ignore deprecated setting.
	if section.HasKey("use_builtin") {
		section.DeleteKey("use_builtin")
	}
	err = section.StrictMapTo(cfg)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func collectSetFlags(fs *flag.FlagSet) map[string]bool {
	setFlags := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		setFlags[f.Name] = true
	})
	return setFlags
}

func mergeConfigWithCLIOverrides(base, cli Config, setFlags map[string]bool) Config {
	merged := base

	if setFlags["profile"] {
		merged.Profile = cli.Profile
	}
	if setFlags["region"] {
		merged.Region = cli.Region
	}
	if setFlags["instance-name"] {
		merged.InstanceName = cli.InstanceName
		merged.InstanceID = ""
	}
	if setFlags["instance-id"] {
		merged.InstanceID = cli.InstanceID
		merged.InstanceName = ""
	}
	if setFlags["local-port"] {
		merged.LocalPort = cli.LocalPort
	}
	if setFlags["remote-host"] {
		merged.RemoteHost = cli.RemoteHost
	}
	if setFlags["remote-port"] {
		merged.RemotePort = cli.RemotePort
	}

	return merged
}

func createAWSSession(ctx context.Context, profile, region string) (aws.Config, error) {
	return config.LoadDefaultConfig(ctx,
		config.WithSharedConfigProfile(profile),
		config.WithRegion(region),
	)
}

type ec2DescribeInstancesAPI interface {
	DescribeInstances(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

type ssmStartSessionAPI interface {
	StartSession(ctx context.Context, params *ssm.StartSessionInput, optFns ...func(*ssm.Options)) (*ssm.StartSessionOutput, error)
}

type ssmTerminateSessionAPI interface {
	TerminateSession(ctx context.Context, params *ssm.TerminateSessionInput, optFns ...func(*ssm.Options)) (*ssm.TerminateSessionOutput, error)
}

func randomIndex(n int) (int, error) {
	if n <= 0 {
		return 0, fmt.Errorf("cannot choose random index from %d candidates", n)
	}
	chooser := rand.New(rand.NewSource(time.Now().UnixNano()))
	return chooser.Intn(n), nil
}

func validateSelectionOptions(cfg Config, allowAny bool) error {
	if allowAny && strings.TrimSpace(cfg.InstanceName) == "" {
		return ErrAnyRequiresInstanceName
	}
	return nil
}

func getInstanceIDByName(ctx context.Context, client ec2DescribeInstancesAPI, instanceName string, allowAny bool, chooseIndex func(int) (int, error)) (string, error) {
	input := &ec2.DescribeInstancesInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("tag:Name"),
				Values: []string{instanceName},
			},
		},
	}
	output, err := client.DescribeInstances(ctx, input)
	if err != nil {
		return "", err
	}

	runningIDs := make([]string, 0)
	var firstMalformedErr error
	for _, reservation := range output.Reservations {
		for _, instance := range reservation.Instances {
			if instance.State == nil {
				if firstMalformedErr == nil {
					firstMalformedErr = fmt.Errorf("%w for instance name %q", ErrInvalidInstanceState, instanceName)
				}
				continue
			}
			if instance.State.Name != types.InstanceStateNameRunning {
				continue
			}
			if instance.InstanceId == nil || strings.TrimSpace(*instance.InstanceId) == "" {
				if firstMalformedErr == nil {
					firstMalformedErr = fmt.Errorf("%w for instance name %q", ErrMissingInstanceID, instanceName)
				}
				continue
			}
			runningIDs = append(runningIDs, *instance.InstanceId)
		}
	}

	switch len(runningIDs) {
	case 0:
		if firstMalformedErr != nil {
			return "", firstMalformedErr
		}
		return "", fmt.Errorf("%w for instance name %q", ErrNoRunningInstances, instanceName)
	case 1:
		return runningIDs[0], nil
	default:
		if !allowAny {
			return "", fmt.Errorf("%w for instance name %q (%d matches)", ErrMultipleRunningInstances, instanceName, len(runningIDs))
		}

		idx, err := chooseIndex(len(runningIDs))
		if err != nil {
			return "", fmt.Errorf("failed to choose instance among %d matches: %w", len(runningIDs), err)
		}
		if idx < 0 || idx >= len(runningIDs) {
			return "", fmt.Errorf("random selector returned out-of-range index %d for %d matches", idx, len(runningIDs))
		}
		return runningIDs[idx], nil
	}
}

func getInstanceIDByID(ctx context.Context, client ec2DescribeInstancesAPI, instanceID string) (string, error) {
	input := &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}
	output, err := client.DescribeInstances(ctx, input)
	if err != nil {
		return "", err
	}

	found := false
	for _, reservation := range output.Reservations {
		for _, instance := range reservation.Instances {
			if instance.InstanceId == nil || strings.TrimSpace(*instance.InstanceId) == "" {
				return "", fmt.Errorf("%w while resolving instance id %q", ErrMissingInstanceID, instanceID)
			}
			if *instance.InstanceId != instanceID {
				continue
			}
			found = true
			if instance.State == nil {
				return "", fmt.Errorf("%w for instance id %q", ErrInvalidInstanceState, instanceID)
			}
			if instance.State.Name == types.InstanceStateNameRunning {
				return instanceID, nil
			}
		}
	}

	if found {
		return "", fmt.Errorf("%w: %q", ErrInstanceNotRunning, instanceID)
	}
	return "", fmt.Errorf("%w: %q", ErrInstanceNotFound, instanceID)
}

func resolveInstanceID(ctx context.Context, client ec2DescribeInstancesAPI, cfg Config, allowAny bool) (string, error) {
	if strings.TrimSpace(cfg.InstanceID) != "" {
		return getInstanceIDByID(ctx, client, cfg.InstanceID)
	}
	return getInstanceIDByName(ctx, client, cfg.InstanceName, allowAny, randomIndex)
}

func startPortForwarding(ctx context.Context, client ssmStartSessionAPI, instanceID, remoteHost string, localPort, remotePort int) (*ssm.StartSessionOutput, error) {
	input := &ssm.StartSessionInput{
		Target:       aws.String(instanceID),
		DocumentName: aws.String("AWS-StartPortForwardingSessionToRemoteHost"),
		Parameters: map[string][]string{
			"localPortNumber": {fmt.Sprintf("%d", localPort)},
			"host":            {remoteHost},
			"portNumber":      {fmt.Sprintf("%d", remotePort)},
		},
	}
	return client.StartSession(ctx, input)
}

func terminatePortForwardingSession(ctx context.Context, client ssmTerminateSessionAPI, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	_, err := client.TerminateSession(ctx, &ssm.TerminateSessionInput{
		SessionId: aws.String(sessionID),
	})
	return err
}

func runSessionLifecycle(
	ctx context.Context,
	localPort int,
	sessionID string,
	startPlugin func() error,
	terminateSession func(context.Context, string) error,
	keepAliveFn func(int, <-chan struct{}),
) error {
	stopChan := make(chan struct{})
	pluginErrCh := make(chan error, 1)
	keepAliveDone := make(chan struct{})

	go func() {
		defer close(keepAliveDone)
		keepAliveFn(localPort, stopChan)
	}()
	go func() {
		pluginErrCh <- startPlugin()
	}()

	var (
		pluginErr error
		ctxDone   bool
	)

	select {
	case pluginErr = <-pluginErrCh:
	case <-ctx.Done():
		ctxDone = true
	}

	close(stopChan)
	select {
	case <-keepAliveDone:
	case <-time.After(time.Second):
		return errors.New("timed out waiting for keep-alive to stop")
	}

	shouldTerminate := sessionID != "" && (ctxDone || pluginErr != nil)
	if shouldTerminate {
		if err := terminateSession(context.Background(), sessionID); err != nil {
			if pluginErr != nil {
				return errors.Join(pluginErr, err)
			}
			return err
		}
	}

	if ctxDone {
		select {
		case postCancelPluginErr := <-pluginErrCh:
			if postCancelPluginErr != nil {
				pluginErr = postCancelPluginErr
			}
		case <-time.After(5 * time.Second):
			return errors.New("timed out waiting for session plugin to stop")
		}
	}

	return pluginErr
}

func startSessionManagerPluginBuiltin(response *ssm.StartSessionOutput, region, profile, instanceID string, ssmEndpoint string) error {
	pluginData, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("failed to marshal session response: %w", err)
	}
	args := []string{
		"aws-go-forward", // Executable name (ignored)
		string(pluginData),
		region,
		"StartSession",
		profile,
		fmt.Sprintf(`{"Target":"%s"}`, instanceID),
		ssmEndpoint,
	}

	// Buffer to capture output
	var output bytes.Buffer

	session.ValidateInputAndStartSession(args, &output)

	if len(output.Bytes()) > 0 {
		fmt.Printf("Session Manager Output: %s\n", output.String())
	}

	return nil
}

func KeepAlive(localPort int, stopChan <-chan struct{}) {
	ticker := time.NewTicker(30 * time.Second) // Adjust interval as needed
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Connect to the local port and send a simple query
			conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
			if err != nil {
				fmt.Printf("Keep-alive failed to connect: %v\n", err)
				continue
			}
			_, err = conn.Write([]byte("\n")) // Minimal keep-alive packet
			if err != nil {
				fmt.Printf("Error sending keep-alive packet: %v\n", err)
			} else {
				fmt.Printf(".")
			}
			conn.Close()
		case <-stopChan:
			// Stop the keep-alive goroutine
			fmt.Println("Stopping keep-alive routine")
			return
		}
	}
}

func main() {
	var configFile string
	var allowAny bool
	var cliCfg Config
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	flag.StringVar(&configFile, "config", "", "Path to configuration file in INI format (optional)")
	flag.StringVar(&cliCfg.Profile, "profile", "", "AWS profile name")
	flag.StringVar(&cliCfg.Region, "region", "", "AWS region")
	flag.StringVar(&cliCfg.InstanceName, "instance-name", "", "Name of the instance used for forwarding")
	flag.StringVar(&cliCfg.InstanceID, "instance-id", "", "Instance ID used for forwarding")
	flag.BoolVar(&allowAny, "any", false, "Allow selecting a random running instance when multiple instances match --instance-name")
	flag.IntVar(&cliCfg.LocalPort, "local-port", 0, "Local port")
	flag.StringVar(&cliCfg.RemoteHost, "remote-host", "", "Remote host")
	flag.IntVar(&cliCfg.RemotePort, "remote-port", 0, "Remote port")
	flag.Parse()

	setFlags := collectSetFlags(flag.CommandLine)
	cfg := cliCfg

	if configFile != "" {
		fileCfg, err := loadConfigFromFile(configFile)
		if err != nil {
			log.Fatalf("Failed to load configuration file: %v", err)
		}
		cfg = mergeConfigWithCLIOverrides(*fileCfg, cliCfg, setFlags)
	}

	if err := cfg.Validate(); err != nil {
		log.Fatalf("Invalid configuration: %v. Use --help for more information.", err)
	}
	if err := validateSelectionOptions(cfg, allowAny); err != nil {
		log.Fatalf("Invalid selection options: %v. Use --help for more information.", err)
	}

	awsCfg, err := createAWSSession(ctx, cfg.Profile, cfg.Region)
	if err != nil {
		log.Fatalf("Failed to create AWS session: %v", err)
	}

	ec2Client := ec2.NewFromConfig(awsCfg)
	instanceID, err := resolveInstanceID(ctx, ec2Client, cfg, allowAny)
	if err != nil {
		log.Fatalf("Failed to get instance ID: %v", err)
	}

	ssmClient := ssm.NewFromConfig(awsCfg)
	sessionResponse, err := startPortForwarding(ctx, ssmClient, instanceID, cfg.RemoteHost, cfg.LocalPort, cfg.RemotePort)
	if err != nil {
		log.Fatalf("Failed to start port forwarding: %v", err)
	}

	fmt.Println("Port forwarding session started.\nPress Ctrl-C to terminate.")

	ssmEndpoint := fmt.Sprintf("https://ssm.%s.amazonaws.com", cfg.Region)

	err = runSessionLifecycle(
		ctx,
		cfg.LocalPort,
		aws.ToString(sessionResponse.SessionId),
		func() error {
			return startSessionManagerPluginBuiltin(sessionResponse, cfg.Region, cfg.Profile, instanceID, ssmEndpoint)
		},
		func(ctx context.Context, sessionID string) error {
			return terminatePortForwardingSession(ctx, ssmClient, sessionID)
		},
		KeepAlive,
	)
	if err != nil {
		log.Fatalf("Session failed: %v", err)
	}
}
