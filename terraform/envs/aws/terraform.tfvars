aws_region    = "us-east-1"
user_image    = "272639014758.dkr.ecr.us-east-1.amazonaws.com/userd:0.1.0"
product_image = "272639014758.dkr.ecr.us-east-1.amazonaws.com/productsd:0.1.0"
order_image   = "272639014758.dkr.ecr.us-east-1.amazonaws.com/orderd:0.1.0"
gateway_image = "272639014758.dkr.ecr.us-east-1.amazonaws.com/gatewayd:0.1.0"

# IPv4 only: the security group rule uses cidr_ipv4.
operator_ingress_cidrs = ["122.183.32.131/32"]
