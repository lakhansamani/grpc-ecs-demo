# Two roles, and the distinction matters on stage:
#
#   EXECUTION role - used by the ECS agent BEFORE your code runs: pull the
#                    image, resolve `secrets` from Secrets Manager, create log
#                    streams. This is what makes JWT_SECRET injection work.
#   TASK role      - the credentials YOUR process gets. Empty here, because
#                    the deployment calls no AWS APIs at runtime (the
#                    explanation provider is the template, not Bedrock).
#
# Keeping the task role empty is the point: no API keys anywhere, and no
# permissions granted "just in case".

variable "name_prefix" { type = string }
variable "secret_arns" {
  type    = list(string)
  default = []
}
variable "tags" {
  type    = map(string)
  default = {}
}

data "aws_iam_policy_document" "assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "execution" {
  name               = "${var.name_prefix}-execution"
  assume_role_policy = data.aws_iam_policy_document.assume.json
  tags               = var.tags
}

resource "aws_iam_role_policy_attachment" "execution_managed" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

# Scoped to the exact secrets this deployment injects, not secretsmanager:*.
data "aws_iam_policy_document" "read_secrets" {
  count = length(var.secret_arns) == 0 ? 0 : 1
  statement {
    actions   = ["secretsmanager:GetSecretValue"]
    resources = var.secret_arns
  }
}

resource "aws_iam_role_policy" "execution_secrets" {
  count  = length(var.secret_arns) == 0 ? 0 : 1
  name   = "read-injected-secrets"
  role   = aws_iam_role.execution.id
  policy = data.aws_iam_policy_document.read_secrets[0].json
}

resource "aws_iam_role" "task" {
  name               = "${var.name_prefix}-task"
  assume_role_policy = data.aws_iam_policy_document.assume.json
  tags               = var.tags
}

output "execution_role_arn" { value = aws_iam_role.execution.arn }
output "task_role_arn" { value = aws_iam_role.task.arn }
