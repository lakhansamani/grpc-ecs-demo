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
# THE ONLY FILE THAT DIFFERS FROM envs/aws.
#
# Fake credentials, the validation skips, and an endpoints block that sends
# every AWS API call to the local emulator instead of to AWS. The stack and
# modules above it cannot tell the difference.
# ============================================================================
provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true
  skip_region_validation      = true
  s3_use_path_style           = true

  endpoints {
    ec2              = "http://localhost:4566"
    ecs              = "http://localhost:4566"
    ecr              = "http://localhost:4566"
    elbv2            = "http://localhost:4566"
    iam              = "http://localhost:4566"
    logs             = "http://localhost:4566"
    secretsmanager   = "http://localhost:4566"
    servicediscovery = "http://localhost:4566"
    route53          = "http://localhost:4566"
    ssm              = "http://localhost:4566"
    sts              = "http://localhost:4566"
  }
}
