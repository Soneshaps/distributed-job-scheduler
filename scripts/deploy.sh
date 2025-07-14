#!/usr/bin/env bash
# One-command deployment script for the job scheduler
# Dependencies: awscli, docker, terraform, kubectl, helm, kustomize
# Usage: ./scripts/deploy.sh

set -euo pipefail

# Find the project root
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
PROJECT_ROOT="$( cd "$SCRIPT_DIR/.." && pwd )"
cd "$PROJECT_ROOT"

# Load environment variables
if [[ ! -f ".env" ]]; then
  echo "Error: .env file not found. Copy from .env.example first." >&2
  exit 1
fi
source ".env"

# Auto-detect AWS account if not set
AWS_PROFILE=${AWS_PROFILE:-default}
if [[ -z "${AWS_ACCOUNT_ID:-}" || "$AWS_ACCOUNT_ID" == "123456789012" ]]; then
  echo "Auto-detecting AWS account ID..."
  AWS_ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text --profile "$AWS_PROFILE")
fi

# Validate required environment variables
REQUIRED_VARS=(AWS_REGION AWS_ACCOUNT_ID EKS_CLUSTER_NAME DOMAIN_NAME)
for var in "${REQUIRED_VARS[@]}"; do
  if [[ -z "${!var:-}" ]]; then
    echo "Error: $var is not set in .env" >&2
    exit 1
  fi
done

# Set up variables
ECR_REGISTRY="$AWS_ACCOUNT_ID.dkr.ecr.$AWS_REGION.amazonaws.com"
K8S_OVERLAY="${KUSTOMIZE_OVERLAY:-$PROJECT_ROOT/terraform/kubernetes/overlays/production}"
IMAGE_TAG=${IMAGE_TAG:-latest}

# Figure out which services to build
SERVICES=(api worker simulator publisher homepage)
SERVICES_TO_BUILD=()
for service in "${SERVICES[@]}"; do
  if [[ -f "$PROJECT_ROOT/deployments/docker/$service/Dockerfile" ]]; then
    SERVICES_TO_BUILD+=("$service")
  else
    echo "Warning: Skipping $service - no Dockerfile found"
  fi
done

# Print section headers
print_step() {
  echo -e "\n\033[1;34m>>> $*\033[0m"
}

# Build and push Docker images
print_step "Building and pushing Docker images"
aws ecr get-login-password --region "$AWS_REGION" --profile "$AWS_PROFILE" | \
  docker login --username AWS --password-stdin "$ECR_REGISTRY"

# Create ECR repos if they don't exist
create_ecr_repo() {
  local repo_name="$1"
  if ! aws ecr describe-repositories --repository-names "$repo_name" --region "$AWS_REGION" --profile "$AWS_PROFILE" >/dev/null 2>&1; then
    echo "Creating ECR repository: $repo_name"
    aws ecr create-repository --repository-name "$repo_name" --region "$AWS_REGION" --profile "$AWS_PROFILE" >/dev/null
  fi
}

# Build and push each service
for service in "${SERVICES_TO_BUILD[@]}"; do
  repo_name="job-scheduler-$service"
  create_ecr_repo "$repo_name"
  
  echo "Building $repo_name:$IMAGE_TAG"
  docker buildx build --platform linux/amd64 \
    -t "$ECR_REGISTRY/$repo_name:$IMAGE_TAG" \
    -f "$PROJECT_ROOT/deployments/docker/$service/Dockerfile" "$PROJECT_ROOT" \
    --push
done

# Deploy infrastructure with Terraform
print_step "Deploying infrastructure with Terraform"
cd "$PROJECT_ROOT/terraform"
terraform init -upgrade -input=false
terraform apply -auto-approve -input=false
cd "$PROJECT_ROOT"

# Configure kubectl
print_step "Configuring kubectl for EKS cluster"
aws eks update-kubeconfig --region "$AWS_REGION" --name "$EKS_CLUSTER_NAME" --profile "$AWS_PROFILE"

# Install KEDA if not already installed
print_step "Setting up KEDA"
if ! helm ls -A | grep -q "^keda"; then
  helm repo add kedacore https://kedacore.github.io/charts
  helm repo update
  helm install keda kedacore/keda --namespace keda --create-namespace
else
  helm upgrade keda kedacore/keda --namespace keda
fi

# Update kustomization with our image tags
print_step "Updating Kubernetes manifests with image tags"
cd "$K8S_OVERLAY"
for service in "${SERVICES_TO_BUILD[@]}"; do
  placeholder="placeholder-${service}-image"
  actual_image="$ECR_REGISTRY/job-scheduler-$service"
  kustomize edit set image "$placeholder=$actual_image:$IMAGE_TAG"
done
cd "$PROJECT_ROOT"

# Deploy to Kubernetes
print_step "Deploying to Kubernetes"
kubectl apply -k "$PROJECT_ROOT/terraform/kubernetes/base"
kubectl apply -k "$K8S_OVERLAY"

# Wait for deployment to be ready
print_step "Waiting for API deployment to be ready"
kubectl -n job-scheduler rollout status deploy/api --timeout=5m

# Get the load balancer hostname
API_HOSTNAME="$(kubectl get ingress -n job-scheduler api -o jsonpath='{.status.loadBalancer.ingress[0].hostname}' 2>/dev/null || true)"

echo -e "\n\033[1;32m✓ Deployment complete!\033[0m"
if [[ -n "$API_HOSTNAME" ]]; then
  echo "Set up DNS: Point $DOMAIN_NAME to $API_HOSTNAME"
else
  echo "Load balancer not ready yet. Check with: kubectl get ingress -n job-scheduler"
fi 