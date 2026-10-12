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
  description = "Security group guarding the database. Reference it from the controller hosts that need access."
  value       = aws_security_group.this.id
}

output "dsn_secret_arn" {
  description = "Secrets Manager ARN holding the full connection string. Mount it as the controller's pg-url credential."
  value       = aws_secretsmanager_secret.dsn.arn
}

output "dsn_secret_name" {
  description = "Secrets Manager name holding the full connection string."
  value       = aws_secretsmanager_secret.dsn.name
}

output "connection_string" {
  description = "Full PostgreSQL connection string for the controller's pg-url credential. Marked sensitive: it carries the master password."
  value       = local.dsn
  sensitive   = true
}
