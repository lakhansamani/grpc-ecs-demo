# A deliberately small, deliberately cheap Postgres for the demo.
#
# COST, so nobody is surprised (us-east-1, on-demand):
#   db.t4g.micro Single-AZ   $0.016/hr        = ~$11.68/month
#   20 GiB gp3 (the minimum) $0.115/GB-month  =   ~$2.30/month
#   backups (retention 0)                     =        $0
#   ------------------------------------------------------------
#   TOTAL                    ~$0.019/hr       = ~$14.00/month
#                            a 3-hour demo    =     ~$0.06
#
# Multi-AZ would double the instance line, so it stays off. db.t4g.micro is the
# cheapest current-generation class; t4g is Graviton, which matches the ARM64
# tasks.
#
# Three settings exist purely so `terraform destroy` actually works and stops
# billing. Getting any of them wrong leaves you paying after the talk:
#   skip_final_snapshot     - otherwise destroy leaves a snapshot you pay for
#   deletion_protection     - otherwise destroy fails outright
#   backup_retention_period - otherwise you accrue backup storage
#
# NOT production settings. A real deployment wants Multi-AZ, backups, deletion
# protection ON, and a maintenance window you chose.

variable "name" { type = string }
variable "vpc_id" { type = string }
variable "subnet_ids" {
  type        = list(string)
  description = "At least two subnets, in different AZs - RDS requires it even for Single-AZ."
}
variable "client_security_group_id" {
  type        = string
  description = "The ECS tasks' security group. Only this may reach the database."
}

variable "instance_class" {
  type    = string
  default = "db.t4g.micro"
}
variable "allocated_storage" {
  type        = number
  default     = 20
  description = "20 GiB is the gp3 minimum; asking for less does not make it cheaper."
}
variable "engine_version" {
  type        = string
  default     = "16"
  description = "Major version only, so AWS picks an available minor instead of failing the apply."
}
# One database PER SERVICE, on ONE instance. Logical isolation between
# services - no shared tables, no cross-service joins by accident, and each one
# can be dumped or restored on its own - while you still only pay for a single
# db.t4g.micro.
#
# Terraform does NOT create these: CREATE DATABASE is SQL, and the AWS provider
# only speaks the AWS API. The instance comes up with just the `postgres`
# maintenance database, and each service creates its own at boot. See
# internal/platform/store/ensuredb.go.
variable "databases" {
  type        = list(string)
  description = "One database name per service. A DSN secret is produced for each."
  default     = ["userd", "productsd", "orderd"]

  validation {
    condition     = length(var.databases) == length(toset(var.databases))
    error_message = "Database names must be unique."
  }
}
variable "username" {
  type    = string
  default = "ecom_app"
}
variable "tags" {
  type    = map(string)
  default = {}
}

# RDS rejects several characters in a master password, and the DSN this ends up
# in is URL-encoded, so the safest thing is to exclude the awkward ones here
# rather than escape them later.
resource "random_password" "db" {
  length           = 32
  special          = true
  override_special = "-_"
}

resource "aws_db_subnet_group" "this" {
  name       = var.name
  subnet_ids = var.subnet_ids
  tags       = merge(var.tags, { Name = var.name })
}

# A dedicated group, so the only ingress rule on the database is "the tasks".
resource "aws_security_group" "db" {
  name        = "${var.name}-db"
  description = "RDS postgres for ${var.name}"
  vpc_id      = var.vpc_id
  tags        = merge(var.tags, { Name = "${var.name}-db" })
}

resource "aws_vpc_security_group_ingress_rule" "from_tasks" {
  security_group_id            = aws_security_group.db.id
  referenced_security_group_id = var.client_security_group_id
  from_port                    = 5432
  to_port                      = 5432
  ip_protocol                  = "tcp"
  description                  = "postgres from the ECS tasks only"
}

resource "aws_db_instance" "this" {
  identifier = var.name

  engine         = "postgres"
  engine_version = var.engine_version
  instance_class = var.instance_class

  # No db_name on purpose: the instance comes up with only the `postgres`
  # maintenance database, and each service creates its own from there. Setting
  # db_name here would create exactly one, which is the thing we are avoiding.
  username = var.username
  password = random_password.db.result

  allocated_storage = var.allocated_storage
  storage_type      = "gp3"
  storage_encrypted = true

  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.db.id]

  # The subnets are public (there is no NAT Gateway in this VPC), but the
  # database must not be. Reachable from inside the VPC only.
  publicly_accessible = false
  multi_az            = false

  # Teardown settings - see the header comment.
  backup_retention_period  = 0
  skip_final_snapshot      = true
  delete_automated_backups = true
  deletion_protection      = false

  # Cost: both of these bill separately once enabled.
  performance_insights_enabled = false
  monitoring_interval          = 0

  auto_minor_version_upgrade = true
  apply_immediately          = true

  tags = merge(var.tags, { Name = var.name })
}

# One DSN secret per database.
#
# A connection string holds a password, so it is a SECRET, never an environment
# variable. The ECS execution role resolves it at task start and injects it as
# DB_URL; the value never appears in a task definition, in a `terraform output`,
# or on anybody's screen.
resource "aws_secretsmanager_secret" "db_url" {
  for_each                = toset(var.databases)
  name                    = "${var.name}-${each.key}-db-url"
  description             = "Postgres DSN for ${each.key} on ${var.name}"
  recovery_window_in_days = 0 # so destroy really deletes it and frees the name
  tags                    = var.tags
}

resource "aws_secretsmanager_secret_version" "db_url" {
  for_each  = toset(var.databases)
  secret_id = aws_secretsmanager_secret.db_url[each.key].id
  secret_string = format(
    "postgres://%s:%s@%s/%s?sslmode=require",
    var.username,
    urlencode(random_password.db.result),
    aws_db_instance.this.endpoint,
    each.key,
  )
}

output "db_url_secret_arns" {
  value       = { for k, v in aws_secretsmanager_secret.db_url : k => v.arn }
  description = "database name -> secret ARN. Inject as DB_URL in that service's task definition."
}
output "endpoint" { value = aws_db_instance.this.endpoint }
output "security_group_id" { value = aws_security_group.db.id }
