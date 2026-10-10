terraform {
  required_version = ">= 1.9"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.67"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

# ============================================================================
# THE ONLY FILE THAT DIFFERS FROM envs/local.
#
# No endpoints block, no skips, no fake credentials. Real credentials come from
# the environment or a named profile. That is the entire difference between
# "running locally" and "running on AWS" for this stack.
# ============================================================================
provider "aws" {
  region = var.aws_region

  # Leave empty to use whatever AWS_PROFILE / credentials the shell has.
  # Set it (in terraform.tfvars, or -var aws_profile=...) when you want the
  # account pinned IN THE CONFIG, so a forgotten `export` cannot send an apply
  # at the wrong account. See variables in main.tf.
  profile = var.aws_profile != "" ? var.aws_profile : null
}
