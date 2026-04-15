# aws-go-forward

**`aws-go-forward`** is a lightweight utility for setting up TCP port forwarding through AWS Systems Manager (SSM). It forwards a port from your local machine to a remote host via an EC2 instance managed by SSM, similar to SSH port forwarding but without requiring SSH access. The tool embeds the AWS Session Manager Plugin in Go, so it does not depend on external binaries, which makes it well suited for automation and secure infrastructure environments.

## Features

- Pure Go implementation — no shelling out to the session-manager-plugin binary
- Port forwarding via SSM without exposing SSH or custom ports
- Optional `.ini` configuration support
- Built-in keep-alive to prevent session timeout
- Cross-platform builds supported

---

## Installation

### Install from binary (recommended)

Download a release archive from:

- [Latest release](https://github.com/esoel/aws-go-forward/releases/latest)
- [All releases](https://github.com/esoel/aws-go-forward/releases)

Each archive is named:

- `aws-go-forward-<version>-<os>-<arch>.tar.gz` (Linux/macOS/FreeBSD)
- `aws-go-forward-<version>-<os>-<arch>.zip` (Windows)

Each archive extracts to a top-level folder with the same base name and includes:

- `aws-go-forward` (or `aws-go-forward.exe`)
- `README.md` (the repository README)

Download the archive and matching `checksums.txt` from the selected release, verify the archive checksum, extract it, and move the extracted binary into a directory on your `PATH` (for example `/usr/local/bin` on Unix-like systems, or any PATH directory on Windows). Then run `aws-go-forward --help` to confirm installation.

### Install from source

Requires Go 1.24+.

Build and install from the repository root:

```bash
git clone https://github.com/esoel/aws-go-forward.git
cd aws-go-forward
make
make install
```

This builds the binary and installs it to `/usr/local/bin` (override with `INSTALL_DIR` if needed).

### Build from source for other targets

```bash
make linux-arm64
make darwin-arm64
make windows-amd64
```

or build all supported targets:

```bash
make cross
```

---

## Runtime Prerequisites

- AWS credentials configured (`~/.aws/credentials`) and allowed to use SSM/EC2 APIs
- EC2 instance with:
  - `AmazonSSMManagedInstanceCore` IAM policy
  - A unique `Name` tag (if you select by `--instance-name`) or a known EC2 instance ID (if you select by `--instance-id`)
  - SSM agent installed and running
- The target endpoint you want to forward to (`remote_host:remote_port`) must be network-reachable from the selected EC2 instance, whether that target runs on the instance itself or on another host (for example RDS/private service in the same VPC)

---

##  Usage

### CLI flags
```bash
aws-go-forward --help
Usage of aws-go-forward:
  -any
        Allow selecting a random running instance when multiple instances match --instance-name
  -config string
        Path to configuration file in INI format (optional)
  -instance-id string
        Instance ID used for forwarding
  -instance-name string
        Name of the instance used for forwarding
  -local-port int
        Local port
  -profile string
        AWS profile name
  -region string
        AWS region
  -remote-host string
        Remote host
  -remote-port int
        Remote port
```

```bash
aws-go-forward \
  --profile default \
  --region us-east-1 \
  --instance-name my-ec2-instance \
  --local-port 3306 \
  --remote-host my-rds.internal \
  --remote-port 3306
```

Use exactly one selector: `--instance-name` or `--instance-id`.

When using `--instance-name`, if multiple running instances match:
- default behavior: fail with an ambiguity error
- with `--any`: select one running match at random

### INI configuration

Create a file like:

```ini
[settings]
profile = default
region = us-east-1
instance_name = my-ec2-instance
# Or use instance_id instead of instance_name
# instance_id = i-0123456789abcdef0
local_port = 3306
remote_host = my-rds.internal
remote_port = 3306
```

Then run:

```bash
aws-go-forward --config mysettings.ini
```

When both `--config` and CLI flags are provided, the config file is used as the baseline and explicitly provided CLI flags override those values.

---

##  Testing

You can spin up test infrastructure with Terraform under `integration_setup/`, this will use your active AWS credentials, in the us-east-1 region, in the default vpc. To costumise use:

```bash
export AWS_PROFILE=test
export TF_VAR_region=eu-west-1
export TF_VAR_vpc_id=<vpc_id>	
```

### Apply

```bash
make integration-up
```

This creates:

- A t4g.nano EC2 instance with SSM enabled
- A MariaDB RDS instance
- Networking between the two

Terraform will output the exact command to run `aws-go-forward` locally.
---

###  Example

Once the test setup is deployed, setup the SSM forwarding and connect to the database using the 2 commands printed on your shell in 2 separate shells.

---
### Cleanup

```bash
make integration-down
```

Destroys the above resources.

---

##  Build Targets

- `make` — build for your current system
- `make install` — install to `/usr/local/bin`
- `make <os>-<arch>` — cross-compile (e.g. `make windows-amd64`)
- `make cross` — build all configured cross-platform binaries
- `make clean` — remove build artifacts from `build/`
- `make test` — run Go unit tests (`go test ./...`)
- `make fmt` — format Go code (`go fmt ./...`)
- `make vet` — run static checks (`go vet ./...`)
- `make check` — run `fmt`, `vet`, and `test`
- `make integration-up` — apply terraform test environment
- `make integration-down` — destroy test environment

Build outputs are written to `build/` for both host and cross-compilation targets.

---

##   Project Layout

- `main.go` – Main utility
- `Makefile` – Build and test helpers
- `integration_setup/` – Terraform environment for verification


---

##  License

MIT
