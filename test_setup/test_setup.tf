provider "aws" {
  region = var.region
}
variable "region" {
  description = "AWS region to deploy resources in"
  type        = string
  default     = "us-east-1"
}

variable "vpc_id" {
  description = "Optional VPC ID. If not provided, the default VPC will be used."
  type        = string
  default     = ""
}

# Fetch the default VPC
data "aws_vpc" "default" {
  count   = var.vpc_id == "" ? 1 : 0
  default = true
}

locals {
  selected_vpc_id = var.vpc_id != "" ? var.vpc_id : data.aws_vpc.default[0].id
}

# Fetch all subnets in the selected VPC
data "aws_subnets" "selected" {
  filter {
    name   = "vpc-id"
    values = [local.selected_vpc_id]
  }
}
# Use the first subnet in the list
locals {
  selected_subnet_id = data.aws_subnets.selected.ids[0]
}

# Security Group for RDS
resource "aws_security_group" "rds_sg" {
  name        = "rds-security-group"
  description = "Allow EC2 instance to access RDS"
  vpc_id      = local.selected_vpc_id

  ingress {
    from_port       = 3306
    to_port         = 3306
    protocol        = "tcp"
    security_groups = [aws_security_group.ec2_sg.id]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name = "rds-sg"
  }
}

# Security Group for EC2
resource "aws_security_group" "ec2_sg" {
  name        = "ec2-security-group"
  description = "Allow EC2 instance to access internal resources"
  vpc_id      = local.selected_vpc_id

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name = "ec2-sg"
  }
}
# IAM Role for EC2 with SSM permissions
resource "aws_iam_role" "ssm_role" {
  name = "ec2-ssm-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Principal = {
          Service = "ec2.amazonaws.com"
        }
        Action = "sts:AssumeRole"
      }
    ]
  })
}

# Attach the SSM policy to the IAM role
resource "aws_iam_role_policy_attachment" "ssm_policy_attachment" {
  role       = aws_iam_role.ssm_role.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

# Create an IAM instance profile for EC2
resource "aws_iam_instance_profile" "ssm_instance_profile" {
  name = "ec2-ssm-instance-profile"
  role = aws_iam_role.ssm_role.name
}

# RDS Instance
resource "aws_db_instance" "mariadb" {
  allocated_storage      = 20
  engine                 = "mariadb"
  engine_version         = "10.6.14"
  instance_class         = "db.t4g.micro"
  username               = "admin"
  password               = "admin123" # Use a secure password in production
  vpc_security_group_ids = [aws_security_group.rds_sg.id]

  skip_final_snapshot = true

  tags = {
    Name = "mariadb-instance"
  }
}

# Fetch an Amazon Linux 2 ARM64-compatible AMI
data "aws_ami" "amazon_linux_2_arm64" {
  most_recent = true

  filter {
    name   = "name"
    values = ["amzn2-ami-hvm-*-arm64-gp2"]
  }

  filter {
    name   = "architecture"
    values = ["arm64"]
  }

  owners = ["137112412989"] # Amazon's official AMI owner ID
}

# Use the dynamically fetched AMI for the EC2 instance
resource "aws_instance" "ec2" {
  ami                    = data.aws_ami.amazon_linux_2_arm64.id
  instance_type          = "t4g.nano" # ARM64-compatible instance type
  subnet_id              = local.selected_subnet_id
  vpc_security_group_ids = [aws_security_group.ec2_sg.id]
  iam_instance_profile   = aws_iam_instance_profile.ssm_instance_profile.name

  root_block_device {
    delete_on_termination = true
  }

  tags = {
    Name = "smallest-ec2-instance"
  }

  user_data = <<-EOF
    #!/bin/bash
    yum install -y amazon-ssm-agent
    systemctl enable amazon-ssm-agent
    systemctl start amazon-ssm-agent
  EOF
}

output "aws_go_forward_command" {
  value       = "aws-go-forward --profile default --instance-name ${aws_instance.ec2.tags["Name"]} --local-port 3306 --remote-host ${aws_db_instance.mariadb.address} --remote-port 3306 --region us-east-1 -b"
  description = "Run this command on your computer to connect to the RDS instance through the EC2 instance using aws-go-forward"
}
