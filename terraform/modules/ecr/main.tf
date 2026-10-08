variable "names" { type = list(string) }
variable "tags" {
  type    = map(string)
  default = {}
}

resource "aws_ecr_repository" "this" {
  for_each             = toset(var.names)
  name                 = each.value
  image_tag_mutability = "MUTABLE" # a demo retags :0.1.0 constantly
  force_delete         = true      # so terraform destroy does not strand images
  image_scanning_configuration {
    scan_on_push = false
  }
  tags = merge(var.tags, { Name = each.value })
}

output "repository_urls" {
  value = { for k, r in aws_ecr_repository.this : k => r.repository_url }
}
