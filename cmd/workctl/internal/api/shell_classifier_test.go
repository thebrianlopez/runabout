package api

import (
	"testing"
)

func TestClassifyCategory(t *testing.T) {
	tests := []struct {
		binary string
		want   string
	}{
		{"kubectl", CategoryKubernetes},
		{"k", CategoryKubernetes},
		{"k9s", CategoryKubernetes},
		{"helm", CategoryKubernetes},
		{"argocd", CategoryKubernetes},
		{"terraform", CategoryTerraform},
		{"tf", CategoryTerraform},
		{"terragrunt", CategoryTerraform},
		{"aws", CategoryAWS},
		{"awsv2", CategoryAWS},
		{"git", CategoryGit},
		{"gh", CategoryGit},
		{"gcm", CategoryGit},
		{"docker", CategoryDocker},
		{"docker-compose", CategoryDocker},
		{"podman", CategoryDocker},
		{"python", CategoryGeneral},
		{"make", CategoryGeneral},
		{"fish", CategoryGeneral},
		{"", CategoryGeneral},
	}
	for _, tt := range tests {
		t.Run(tt.binary, func(t *testing.T) {
			if got := classifyCategory(tt.binary); got != tt.want {
				t.Errorf("classifyCategory(%q) = %q, want %q", tt.binary, got, tt.want)
			}
		})
	}
}

func TestIsInfraCommand(t *testing.T) {
	tests := []struct {
		binary string
		want   bool
	}{
		{"kubectl", true},
		{"k", true},
		{"helm", true},
		{"argocd", true},
		{"terraform", true},
		{"tf", true},
		{"terragrunt", true},
		{"aws", true},
		{"awsv2", true},
		{"git", false},
		{"gh", false},
		{"docker", false},
		{"python", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.binary, func(t *testing.T) {
			if got := isInfraCommand(tt.binary); got != tt.want {
				t.Errorf("isInfraCommand(%q) = %v, want %v", tt.binary, got, tt.want)
			}
		})
	}
}

func TestIsDeployCommand(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{"kubectl apply -f manifest.yaml", true},
		{"terraform apply -auto-approve", true},
		{"helm upgrade --install svc ./chart", true},
		{"helm install my-release ./chart", true},
		{"helm uninstall my-release", true},
		{"git push origin main", true},
		{"git merge feature-branch", true},
		{"argocd app sync my-app", true},
		{"docker deploy mystack", true},
		{"kubectl get pods", false},
		{"terraform plan", false},
		{"terraform fmt", false},
		{"git status", false},
		{"git log --oneline", false},
		{"ls -la", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			if got := isDeployCommand(tt.cmd); got != tt.want {
				t.Errorf("isDeployCommand(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
	}
}

func TestRedactSensitive(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// No sensitive content — unchanged
		{"kubectl get pods", "kubectl get pods"},
		{"git status", "git status"},
		{"ls -la", "ls -la"},
		// TOKEN/password/secret/key with = separator
		{"export TOKEN=abc123", "export TOKEN=[REDACTED]"},
		{"export token=abc123", "export token=[REDACTED]"},
		{"curl --password=secretpass url", "curl --password=[REDACTED] url"},
		{"SECRET=myvalue command", "SECRET=[REDACTED] command"},
		// key: value (space+colon separator)
		{"echo key: myvalue", "echo key: [REDACTED]"},
		// key with = inside a larger flag
		{"aws --access-key=AKIA123", "aws --access-key=[REDACTED]"},
		// value that's a URL-style token
		{"gh auth login --token=ghp_xyz789", "gh auth login --token=[REDACTED]"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := redactSensitive(tt.input); got != tt.want {
				t.Errorf("redactSensitive(%q)\n  got  %q\n  want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractBinary(t *testing.T) {
	tests := []struct {
		cmd  string
		want string
	}{
		{"kubectl get pods", "kubectl"},
		{"git push origin main", "git"},
		{"AWS_PROFILE=prod terraform plan", "terraform"},
		{"AWS_PROFILE=prod AWS_REGION=us-east-2 aws s3 ls", "aws"},
		{"", ""},
		{"  ls  -la  ", "ls"},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			if got := extractBinary(tt.cmd); got != tt.want {
				t.Errorf("extractBinary(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}
