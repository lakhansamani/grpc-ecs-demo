# The JWT signing secret.
#
# This is the concrete reason the demo needs Secrets Manager rather than a
# hand-wave: identityd runs as THREE tasks, and if they do not all hold the
# same secret then a token minted by one fails verification on another. The
# demo would break intermittently and look exactly like a load-balancing bug.
#
# It is injected through the task definition's `secrets` block, so the value
# never enters the image, the task definition, or git.

variable "name" { type = string }
variable "generate" {
  type        = bool
  default     = true
  description = "Generate a random secret. Set false and pass `value` to supply your own."
}
variable "value" {
  type      = string
  default   = ""
  sensitive = true
}
variable "tags" {
  type    = map(string)
  default = {}
}

resource "random_password" "jwt" {
  count   = var.generate ? 1 : 0
  length  = 48
  special = false # keeps it copy-pasteable on stage
}

resource "aws_secretsmanager_secret" "jwt" {
  name                    = var.name
  description             = "HS256 signing secret shared by all identityd tasks"
  recovery_window_in_days = 0 # destroy immediately, so re-applying a demo works
  tags                    = var.tags
}

resource "aws_secretsmanager_secret_version" "jwt" {
  secret_id     = aws_secretsmanager_secret.jwt.id
  secret_string = var.generate ? random_password.jwt[0].result : var.value
}

output "arn" { value = aws_secretsmanager_secret.jwt.arn }
output "name" { value = aws_secretsmanager_secret.jwt.name }
