module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "19.15.3"

  cluster_name    = var.cluster_name
  cluster_version = "1.27"
  vpc_id          = module.vpc.vpc_id
  subnet_ids      = module.vpc.private_subnets

  cluster_endpoint_public_access = true
  cluster_endpoint_private_access = false

  eks_managed_node_groups = {
    general = {
      name           = "general-purpose"
      instance_types = ["t3.small"]
      min_size       = 1
      max_size       = 4
      desired_size   = 2
    }
    workers = {
      name           = "job-workers"
      instance_types = ["t3.small"]
      min_size       = 1
      max_size       = 10
      desired_size   = 2
      labels = {
        "node-role" = "worker"
      }
    }
  }
} 