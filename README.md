# aws-go-forward

**`aws-go-forward`** is a lightweight utility to forward a local TCP port to a remote host (such as an RDS instance) through an EC2 instance using AWS Session Manager (SSM). It embeds the AWS Session Manager Plugin in Go and requires no external binaries, making it ideal for automation and secure infrastructure setups.

## Features

- Pure Go implementation — no shelling out to the session-manager-plugin binary
- Port forwarding via SSM without exposing SSH or custom ports
- Optional `.ini` configuration support
- Built-in keep-alive to prevent session timeout
- Cross-platform builds supported

---

## Prerequisites

- Go 1.20+ installed
- AWS credentials configured (`~/.aws/credentials`)
- EC2 instance with:
  - `AmazonSSMManagedInstanceCore` IAM policy
  - Tag `Name=<instance-name>`
  - SSM agent installed and running
- Your target (e.g. RDS) must be reachable from the EC2 instance

---

##  Installation

### Build for your system

```bash
make
make install
```

This builds the binary and installs it to `/usr/local/bin` (override with `INSTALL_DIR` if needed).

### Cross-compile

```bash
make linux-arm64
make darwin-arm64
make windows-amd64
```

---

##  Usage

### CLI flags

```bash
aws-go-forward \
  --profile default \
  --region us-east-1 \
  --instance-name my-ec2-instance \
  --local-port 3306 \
  --remote-host my-rds.internal \
  --remote-port 3306
```

### INI configuration

Create a file like:

```ini
[settings]
profile = default
region = us-east-1
instanceName = my-ec2-instance
localPort = 3306
remoteHost = my-rds.internal
remotePort = 3306
```

Then run:

```bash
aws-go-forward --config mysettings.ini
```

---

##  Testing

You can spin up test infrastructure with Terraform under `test_setup/`:

### Apply

```bash
make test
```

This creates:

- A t4g.nano EC2 instance with SSM enabled
- A MariaDB RDS instance
- Networking between the two

Terraform will output the exact command to run `aws-go-forward` locally.

### Cleanup

```bash
make clean-test
```

Destroys the above resources.

---

##  Build Targets

- `make` — build for your current system
- `make install` — install to `/usr/local/bin`
- `make <os>-<arch>` — cross-compile (e.g. `make windows-amd64`)
- `make test` — apply terraform test environment
- `make clean-test` — destroy test environment

---

##  Example

Once the test setup is deployed, connect locally:

```bash
aws-go-forward --profile default --instance-name smallest-ec2-instance --local-port 3306 --remote-host terraform-...rds.amazonaws.com --remote-port 3306 --region us-east-1
```

Then connect to RDS:

```bash
mysql -h 127.0.0.1 -P 3306 -u admin -p
```

---

##   Project Layout

- `main.go` – Main utility
- `Makefile` – Build and test helpers
- `test_setup/` – Terraform environment for verification


---

##  License

MIT
