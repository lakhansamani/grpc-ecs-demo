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
  region = "ap-south-1"
}
