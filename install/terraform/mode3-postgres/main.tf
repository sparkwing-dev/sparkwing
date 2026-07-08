locals {
  is_aurora = var.engine == "aurora-serverless-v2"
  port      = 5432

  endpoint = local.is_aurora ? aws_rds_cluster.this[0].endpoint : aws_db_instance.this[0].address

  dsn = format(
    "postgres://%s:%s@%s:%d/%s?sslmode=require",
    var.master_username,
    random_password.master.result,
    local.endpoint,
    local.port,
    var.database_name,
  )
}

resource "random_password" "master" {
  length  = 40
  special = false
}

resource "aws_db_subnet_group" "this" {
  name       = var.name
  subnet_ids = var.subnet_ids
  tags       = var.tags
}

resource "aws_security_group" "this" {
  name        = "${var.name}-db"
  description = "PostgreSQL access for sparkwing Mode 3 runners"
  vpc_id      = var.vpc_id
  tags        = var.tags

  dynamic "ingress" {
    for_each = length(var.allowed_security_group_ids) > 0 ? [1] : []
    content {
      description     = "PostgreSQL from runner security groups"
      from_port       = local.port
      to_port         = local.port
      protocol        = "tcp"
      security_groups = var.allowed_security_group_ids
    }
  }

  dynamic "ingress" {
    for_each = length(var.allowed_cidr_blocks) > 0 ? [1] : []
    content {
      description = "PostgreSQL from allowed CIDR blocks"
      from_port   = local.port
      to_port     = local.port
      protocol    = "tcp"
      cidr_blocks = var.allowed_cidr_blocks
    }
  }

  egress {
    description = "All outbound"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_db_instance" "this" {
  count = local.is_aurora ? 0 : 1

  identifier     = var.name
  engine         = "postgres"
  engine_version = var.engine_version
  instance_class = var.instance_class

  allocated_storage = var.allocated_storage
  storage_encrypted = true

  db_name  = var.database_name
  username = var.master_username
  password = random_password.master.result
  port     = local.port

  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.this.id]
  publicly_accessible    = false
  multi_az               = var.multi_az

  backup_retention_period   = var.backup_retention_days
  deletion_protection       = var.deletion_protection
  skip_final_snapshot       = var.skip_final_snapshot
  final_snapshot_identifier = var.skip_final_snapshot ? null : "${var.name}-final"

  tags = var.tags
}

resource "aws_rds_cluster" "this" {
  count = local.is_aurora ? 1 : 0

  cluster_identifier = var.name
  engine             = "aurora-postgresql"
  engine_mode        = "provisioned"
  engine_version     = var.engine_version

  database_name   = var.database_name
  master_username = var.master_username
  master_password = random_password.master.result
  port            = local.port

  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.this.id]
  storage_encrypted      = true

  backup_retention_period   = var.backup_retention_days
  deletion_protection       = var.deletion_protection
  skip_final_snapshot       = var.skip_final_snapshot
  final_snapshot_identifier = var.skip_final_snapshot ? null : "${var.name}-final"

  serverlessv2_scaling_configuration {
    min_capacity = var.serverless_min_capacity
    max_capacity = var.serverless_max_capacity
  }

  tags = var.tags
}

resource "aws_rds_cluster_instance" "this" {
  count = local.is_aurora ? 1 : 0

  identifier          = "${var.name}-1"
  cluster_identifier  = aws_rds_cluster.this[0].id
  engine              = aws_rds_cluster.this[0].engine
  engine_version      = aws_rds_cluster.this[0].engine_version
  instance_class      = "db.serverless"
  publicly_accessible = false

  tags = var.tags
}

resource "aws_secretsmanager_secret" "dsn" {
  name        = "${var.name}/postgres-dsn"
  description = "PostgreSQL connection string for sparkwing Mode 3 (SPARKWING_PG_URL)."
  tags        = var.tags
}

resource "aws_secretsmanager_secret_version" "dsn" {
  secret_id     = aws_secretsmanager_secret.dsn.id
  secret_string = local.dsn
}
