module "iam_assumable_role_lb" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-assumable-role-with-oidc"
  version = "5.30.0"

  create_role                  = true
  role_name                    = "aws-load-balancer-controller-role"
  provider_url                 = replace(module.eks.cluster_oidc_issuer_url, "https://", "")
  role_policy_arns             = ["arn:aws:iam::aws:policy/AmazonEKSLoadBalancingPolicy"]
  oidc_fully_qualified_subjects = [
    "system:serviceaccount:kube-system:aws-load-balancer-controller"
  ]
} 