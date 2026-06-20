variable "name" {
  description = "Name prefix applied to every resource this module creates."
  type        = string
  default     = "sparkwing"
}

variable "engine" {
  description = <<-EOT
    Which database to provision. "rds" creates a single PostgreSQL RDS
    instance (cheapest at idle, fixed size). "aurora-serverless-v2"
    creates an Aurora PostgreSQL cluster that scales capacity with load.
  EOT
  type        = string
  default     = "rds"

  validation {
    condition     = contains(["rds", "aurora-serverless-v2"], var.engine)
    error_message = "engine must be one of: rds, aurora-serverless-v2."
  }
}

variable "vpc_id" {
  description = "VPC the database is placed in. The runners that connect must be able to reach this VPC."
  type        = string
}

variable "subnet_ids" {
  description = <<-EOT
    Private subnet IDs for the DB subnet group. Provide at least two in
    different availability zones; RDS and Aurora both require it. Use
    private subnets: the database is not publicly accessible by default.
  EOT
  type        = list(string)

  validation {
    condition     = length(var.subnet_ids) >= 2
    error_message = "subnet_ids must list at least two subnets in different availability zones."
  }
}

variable "allowed_security_group_ids" {
  description = "Security groups allowed to reach the database on the PostgreSQL port. Set these to the runner hosts' security groups."
  type        = list(string)
  default     = []
}

variable "allowed_cidr_blocks" {
  description = "CIDR blocks allowed to reach the database on the PostgreSQL port. An alternative to allowed_security_group_ids for hosts without a security group."
  type        = list(string)
  default     = []
}

variable "database_name" {
  description = "Name of the initial database the runner connects to."
  type        = string
  default     = "sparkwing"
}

variable "master_username" {
  description = "Master username for the database."
  type        = string
  default     = "sparkwing"
}

variable "engine_version" {
  description = "PostgreSQL engine version. RDS accepts a major-only value (e.g. \"16\") and selects the latest minor; Aurora wants a full version (e.g. \"16.4\")."
  type        = string
  default     = "16.4"
}

variable "instance_class" {
  description = "RDS instance class. Used only when engine = \"rds\"."
  type        = string
  default     = "db.t4g.micro"
}

variable "allocated_storage" {
  description = "RDS storage in GiB. Used only when engine = \"rds\"."
  type        = number
  default     = 20
}

variable "serverless_min_capacity" {
  description = "Aurora Serverless v2 minimum capacity in ACUs. Used only when engine = \"aurora-serverless-v2\"."
  type        = number
  default     = 0.5
}

variable "serverless_max_capacity" {
  description = "Aurora Serverless v2 maximum capacity in ACUs. Used only when engine = \"aurora-serverless-v2\"."
  type        = number
  default     = 4
}

variable "multi_az" {
  description = "Run a standby in a second availability zone for failover. Used only when engine = \"rds\"; Aurora replicates across AZs by design."
  type        = bool
  default     = false
}

variable "backup_retention_days" {
  description = "Number of days to retain automated backups."
  type        = number
  default     = 7
}

variable "deletion_protection" {
  description = "Block accidental deletion of the database. Disable before a teardown."
  type        = bool
  default     = true
}

variable "skip_final_snapshot" {
  description = "Skip the final snapshot on destroy. Leave false for real data; set true for throwaway stacks."
  type        = bool
  default     = false
}

variable "tags" {
  description = "Tags applied to every resource this module creates."
  type        = map(string)
  default     = {}
}
