package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
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

var sessionManagerProcess *os.Process

type Config struct {
	Profile      string
	Region       string
	InstanceName string
	LocalPort    int
	RemoteHost   string
	RemotePort   int
	UseBuiltin   bool
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

func getInstanceID(client *ec2.Client, instanceName string) (string, error) {
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

func startPortForwarding(client *ssm.Client, instanceID, remoteHost string, localPort, remotePort int) (*ssm.StartSessionOutput, error) {
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

func startSessionManagerPluginBuiltin(response *ssm.StartSessionOutput, region, profile, instanceID string, ssmEndpoint string) error {
	pluginData, err := json.Marshal(response)
	if err != nil {
		log.Fatalf("Failed to marshal response: %v", err)
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

	// Call the ValidateInputAndStartSession function
	session.ValidateInputAndStartSession(args, &output)

	// Check for errors in the output (if any)
	if len(output.Bytes()) > 0 {
		fmt.Printf("Session Manager Output: %s\n", output.String())
	}

	return nil
}

func startSessionManagerPluginExternal(response *ssm.StartSessionOutput, region, profile, instanceID string, ssmEndpoint string) (*os.Process, error) {
	pluginData, err := json.Marshal(response)
	if err != nil {
		log.Fatalf("Failed to marshal response: %v", err)
	}

	cmd := exec.Command(
		"session-manager-plugin",
		string(pluginData),
		region,
		"StartSession",
		profile,
		fmt.Sprintf(`{"Target":"%s"}`, instanceID),
		ssmEndpoint,
	)

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err = cmd.Start()
	if err != nil {
		return nil, err
	}

	return cmd.Process, nil
}

func cleanup() {
	if sessionManagerProcess != nil {
		_ = sessionManagerProcess.Kill()
		fmt.Println("Session Manager Plugin process terminated.")
	}
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
				fmt.Println("Keep-alive packet sent successfully")
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

	flag.StringVar(&configFile, "config", "", "Path to configuration file in INI format")
	flag.StringVar(&cfg.Profile, "profile", "", "AWS profile name")
	flag.StringVar(&cfg.Region, "region", "", "AWS region")
	flag.StringVar(&cfg.InstanceName, "instance-name", "", "Name of the instance used for forwarding")
	flag.IntVar(&cfg.LocalPort, "local-port", 0, "Local port")
	flag.StringVar(&cfg.RemoteHost, "remote-host", "", "Remote host")
	flag.IntVar(&cfg.RemotePort, "remote-port", 0, "Remote port")
	flag.BoolVar(&cfg.UseBuiltin, "b", false, "Use builtin session manager plugin instead of external binary")
	flag.Parse()

	if configFile != "" {
		fileCfg, err := loadConfigFromFile(configFile)
		if err != nil {
			log.Fatalf("Failed to load configuration file: %v", err)
		}
		cfg = *fileCfg
	}

	if cfg.Profile == "" || cfg.Region == "" || cfg.InstanceName == "" ||
		cfg.LocalPort == 0 || cfg.RemoteHost == "" || cfg.RemotePort == 0 {
		log.Fatal("All parameters are required. Use --help for more information.")
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

	fmt.Println("Port forwarding session started")

	ssmEndpoint := fmt.Sprintf("https://ssm.%s.amazonaws.com", cfg.Region)

	stopChan := make(chan struct{})

	// Start keep-alive goroutine
	go KeepAlive(cfg.LocalPort, stopChan)

	if cfg.UseBuiltin {
		err = startSessionManagerPluginBuiltin(sessionResponse, cfg.Region, cfg.Profile, instanceID, ssmEndpoint)
		if err != nil {
			log.Fatalf("Failed to start Session Manager Plugin builtin: %v", err)
		}
	} else {
		sessionManagerProcess, err = startSessionManagerPluginExternal(sessionResponse, cfg.Region, cfg.Profile, instanceID, ssmEndpoint)
		if err != nil {
			log.Fatalf("Failed to start Session Manager Plugin: %v", err)
		}
		fmt.Printf("Session Manager Plugin process started with PID: %d\n", sessionManagerProcess.Pid)
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	// Stop keep-alive goroutine
	close(stopChan)

	cleanup()
}
