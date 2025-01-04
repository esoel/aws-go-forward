package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/session-manager-plugin/src/sessionmanagerplugin/session"
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
	return "", fmt.Errorf("no running instances found")
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

func startSessionManagerPlugin(response *ssm.StartSessionOutput, region, profile, instanceID string) (*os.Process, error) {
	pluginData, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(
		"session-manager-plugin",
		string(pluginData),
		region,
		"StartSession",
		profile,
		fmt.Sprintf(`{"Target":"%s"}`, instanceID),
		fmt.Sprintf("https://ssm.%s.amazonaws.com", region),
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

	sessionManagerProcess, err = startSessionManagerPlugin(sessionResponse, cfg.Region, cfg.Profile, instanceID)
	if err != nil {
		log.Fatalf("Failed to start Session Manager Plugin: %v", err)
	}

	fmt.Printf("Session Manager Plugin process started with PID: %d\n", sessionManagerProcess.Pid)

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	cleanup()
}
