# terraform/iam_cicd.tf

data "aws_iam_policy_document" "github_oidc_assume_role" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [module.eks.oidc_provider_arn]
    }

    # IMPORTANT: Restrict this to your GitHub repository
    # Replace "your-github-org/your-repo-name" with your actual repo
    condition {
      test     = "StringLike"
      variable = "${replace(module.eks.oidc_provider, "https://", "")}:sub"
      values   = ["repo:your-github-org/your-repo-name:ref:refs/heads/main"]
    }
  }
}

resource "aws_iam_role" "cicd_github_actions" {
  name               = "${var.cluster_name}-cicd-github-actions-role"
  assume_role_policy = data.aws_iam_policy_document.github_oidc_assume_role.json
}

# Define the permissions our CI/CD pipeline needs

data "aws_iam_policy_document" "cicd_permissions" {
  statement {
    effect  = "Allow"
    actions = [
      "ecr:GetAuthorizationToken",
      "ecr:BatchCheckLayerAvailability",
      "ecr:CompleteLayerUpload",
      "ecr:InitiateLayerUpload",
      "ecr:PutImage",
      "ecr:UploadLayerPart"
    ]
    # Grant access to all ECR repositories in the account.
    # You could restrict this further to specific repository ARNs.
    resources = ["*"]
  }

  statement {
    effect    = "Allow"
    actions   = [
      "eks:DescribeCluster"
    ]
    resources = [module.eks.cluster_arn]
  }
}

resource "aws_iam_policy" "cicd_permissions" {
  name   = "${var.cluster_name}-cicd-github-actions-policy"
  policy = data.aws_iam_policy_document.cicd_permissions.json
}

resource "aws_iam_role_policy_attachment" "cicd_attach" {
  role       = aws_iam_role.cicd_github_actions.name
  policy_arn = aws_iam_policy.cicd_permissions.arn
} 