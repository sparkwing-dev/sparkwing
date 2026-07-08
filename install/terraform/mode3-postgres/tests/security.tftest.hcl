# Security-posture assertions for the mode3-postgres module, run offline
# via `terraform test` with mocked providers (no AWS account, no API call).
#
# bin/check-terraform.sh asserts the resource SET per engine knob; this
# asserts the security-critical attribute VALUES. A future edit could flip
# publicly_accessible, drop encryption, or widen the security group without
# changing the resource count, and the set assertion would not catch it.
# These runs do. Mocking the random provider too makes the generated DSN a
# known value at plan time, so the connection string's sslmode is asserted.

mock_provider "aws" {}
mock_provider "random" {}

variables {
  name       = "sparkwing-test"
  vpc_id     = "vpc-00000000000000000"
  subnet_ids = ["subnet-00000000000000001", "subnet-00000000000000002"]
}

run "rds_security_posture" {
  # apply (offline, against the mocks) so the generated DSN is a known
  # value and its sslmode can be asserted. No AWS resource is created.
  command = apply

  variables {
    engine                     = "rds"
    allowed_security_group_ids = ["sg-00000000000000001"]
  }

  assert {
    condition     = aws_db_instance.this[0].publicly_accessible == false
    error_message = "RDS instance must not be publicly accessible"
  }

  assert {
    condition     = aws_db_instance.this[0].storage_encrypted == true
    error_message = "RDS storage must be encrypted at rest"
  }

  assert {
    condition     = length(aws_security_group.this.ingress) == 1
    error_message = "security group must expose exactly one ingress rule"
  }

  assert {
    condition     = one(aws_security_group.this.ingress).from_port == 5432 && one(aws_security_group.this.ingress).to_port == 5432
    error_message = "ingress must be restricted to the PostgreSQL port 5432"
  }

  assert {
    condition     = one(aws_security_group.this.ingress).protocol == "tcp"
    error_message = "ingress protocol must be tcp, not all-protocols (-1)"
  }

  assert {
    condition     = try(length(one(aws_security_group.this.ingress).cidr_blocks), 0) == 0
    error_message = "ingress sourced from security groups must open no CIDR"
  }

  assert {
    condition     = strcontains(aws_secretsmanager_secret_version.dsn.secret_string, "sslmode=require")
    error_message = "connection string must require TLS (sslmode=require)"
  }
}

run "aurora_security_posture" {
  command = apply

  variables {
    engine                     = "aurora-serverless-v2"
    allowed_security_group_ids = ["sg-00000000000000001"]
  }

  assert {
    condition     = aws_rds_cluster_instance.this[0].publicly_accessible == false
    error_message = "Aurora cluster instance must not be publicly accessible"
  }

  assert {
    condition     = aws_rds_cluster.this[0].storage_encrypted == true
    error_message = "Aurora cluster storage must be encrypted at rest"
  }

  assert {
    condition     = length(aws_security_group.this.ingress) == 1
    error_message = "security group must expose exactly one ingress rule"
  }

  assert {
    condition     = one(aws_security_group.this.ingress).from_port == 5432 && one(aws_security_group.this.ingress).to_port == 5432
    error_message = "ingress must be restricted to the PostgreSQL port 5432"
  }

  assert {
    condition     = one(aws_security_group.this.ingress).protocol == "tcp"
    error_message = "ingress protocol must be tcp, not all-protocols (-1)"
  }

  assert {
    condition     = try(length(one(aws_security_group.this.ingress).cidr_blocks), 0) == 0
    error_message = "ingress sourced from security groups must open no CIDR"
  }

  assert {
    condition     = strcontains(aws_secretsmanager_secret_version.dsn.secret_string, "sslmode=require")
    error_message = "connection string must require TLS (sslmode=require)"
  }
}

run "cidr_ingress_is_port_only_and_not_world_open" {
  command = plan

  variables {
    engine              = "rds"
    allowed_cidr_blocks = ["10.0.0.0/16"]
  }

  assert {
    condition     = one(aws_security_group.this.ingress).from_port == 5432 && one(aws_security_group.this.ingress).to_port == 5432 && one(aws_security_group.this.ingress).protocol == "tcp"
    error_message = "CIDR ingress must be restricted to the PostgreSQL port over tcp"
  }

  assert {
    condition     = !contains(one(aws_security_group.this.ingress).cidr_blocks, "0.0.0.0/0")
    error_message = "ingress must never admit the whole internet (0.0.0.0/0)"
  }
}
