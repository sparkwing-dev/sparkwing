# Plan-only harness for the mode3-postgres module. The AWS provider is
# configured with mock credentials and every validation skip flag, so
# `terraform plan` enumerates the module's resource graph offline with
# no AWS account. It asserts the count-gated engine branches and the
# networking wiring without standing up real infrastructure. Drive the
# engine knob with `-var engine=rds` or `-var engine=aurora-serverless-v2`.

terraform {
  required_version = ">= 1.5"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0"
    }
  }
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "mock"
  secret_key                  = "mock"
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true
  skip_region_validation      = true
}

variable "engine" {
  type    = string
  default = "rds"
}

module "db" {
  source = "../.."

  name   = "sparkwing-plan-test"
  engine = var.engine

  vpc_id     = "vpc-00000000000000000"
  subnet_ids = ["subnet-00000000000000001", "subnet-00000000000000002"]

  allowed_security_group_ids = ["sg-00000000000000001"]
}
