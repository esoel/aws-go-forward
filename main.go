package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
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
	LocalPort    int    `ini:"local_port"`
	RemoteHost   string `ini:"remote_host"`
	RemotePort   int    `ini:"remote_port"`
	UseBuiltin   bool   `ini:"use_builtin"`
}

func (c Config) Validate() error {
	if c.Profile == "" || c.Region == "" || c.InstanceName == "" ||
		c.LocalPort == 0 || c.RemoteHost == "" || c.RemotePort == 0 {
		return errors.New("missing parameters")
	}
	return nil
}

func loadConfigFromFile(configFile string) (*Config, error) {
	cfg := &Config{}
	iniCfg, err := ini.Load(configFile)
	if err != nil {
		return nil, err
	}
	section := iniCfg.Section("settings")
	err = section.MapTo(cfg)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func createAWSSession(profile, region string) (aws.Config, error) {
	return config.LoadDefaultConfig(context.TODO(),
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

func getInstanceID(client ec2DescribeInstancesAPI, instanceName string) (string, error) {
	input := &ec2.DescribeInstancesInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("tag:Name"),
				Values: []string{instanceName},
			},
		},
	}
	output, err := client.DescribeInstances(context.TODO(), input)
	if err != nil {
		return "", err
	}

	for _, reservation := range output.Reservations {
		for _, instance := range reservation.Instances {
			if instance.State.Name == types.InstanceStateNameRunning {
				return *instance.InstanceId, nil
			}
		}
	}
	return "", fmt.Errorf("No running aws instances found.")
}

func startPortForwarding(client ssmStartSessionAPI, instanceID, remoteHost string, localPort, remotePort int) (*ssm.StartSessionOutput, error) {
	input := &ssm.StartSessionInput{
		Target:       aws.String(instanceID),
		DocumentName: aws.String("AWS-StartPortForwardingSessionToRemoteHost"),
		Parameters: map[string][]string{
			"localPortNumber": {fmt.Sprintf("%d", localPort)},
			"host":            {remoteHost},
			"portNumber":      {fmt.Sprintf("%d", remotePort)},
		},
	}
	return client.StartSession(context.TODO(), input)
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
	var cfg Config

	flag.StringVar(&configFile, "config", "", "Path to configuration file in INI format (optional)")
	flag.StringVar(&cfg.Profile, "profile", "", "AWS profile name")
	flag.StringVar(&cfg.Region, "region", "", "AWS region")
	flag.StringVar(&cfg.InstanceName, "instance-name", "", "Name of the instance used for forwarding")
	flag.IntVar(&cfg.LocalPort, "local-port", 0, "Local port")
	flag.StringVar(&cfg.RemoteHost, "remote-host", "", "Remote host")
	flag.IntVar(&cfg.RemotePort, "remote-port", 0, "Remote port")
	flag.Parse()

	if configFile != "" {
		fileCfg, err := loadConfigFromFile(configFile)
		if err != nil {
			log.Fatalf("Failed to load configuration file: %v", err)
		}
		cfg = *fileCfg
	}

	if err := cfg.Validate(); err != nil {
		log.Fatal("Missing parameters. Use --help for more information.")
	}

	awsCfg, err := createAWSSession(cfg.Profile, cfg.Region)
	if err != nil {
		log.Fatalf("Failed to create AWS session: %v", err)
	}

	ec2Client := ec2.NewFromConfig(awsCfg)
	instanceID, err := getInstanceID(ec2Client, cfg.InstanceName)
	if err != nil {
		log.Fatalf("Failed to get instance ID: %v", err)
	}

	ssmClient := ssm.NewFromConfig(awsCfg)
	sessionResponse, err := startPortForwarding(ssmClient, instanceID, cfg.RemoteHost, cfg.LocalPort, cfg.RemotePort)
	if err != nil {
		log.Fatalf("Failed to start port forwarding: %v", err)
	}

	fmt.Println("Port forwarding session started.\nPress Ctrl-C to terminate.")

	ssmEndpoint := fmt.Sprintf("https://ssm.%s.amazonaws.com", cfg.Region)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
