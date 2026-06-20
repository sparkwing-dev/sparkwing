output "endpoint" {
  description = "Database endpoint hostname."
  value       = local.endpoint
}

output "port" {
  description = "Database port."
  value       = local.port
}

output "database_name" {
  description = "Initial database name."
  value       = var.database_name
}

output "security_group_id" {
  description = "Security group guarding the database. Reference it from runner hosts that need access."
  value       = aws_security_group.this.id
}

output "dsn_secret_arn" {
  description = "Secrets Manager ARN holding the full connection string. Read it into SPARKWING_PG_URL on each runner."
  value       = aws_secretsmanager_secret.dsn.arn
}

output "dsn_secret_name" {
  description = "Secrets Manager name holding the full connection string."
  value       = aws_secretsmanager_secret.dsn.name
}

output "connection_string" {
  description = "Full PostgreSQL connection string for SPARKWING_PG_URL. Marked sensitive: it carries the master password."
  value       = local.dsn
  sensitive   = true
}
