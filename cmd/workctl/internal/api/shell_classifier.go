package api

import (
	"regexp"
	"strings"
)

// Shell command category constants.
const (
	CategoryKubernetes = "kubernetes"
	CategoryTerraform  = "terraform"
	CategoryAWS        = "aws"
	CategoryGit        = "git"
	CategoryDocker     = "docker"
	CategoryGeneral    = "general"
)

// categoryMap maps known binary names to categories.
var categoryMap = map[string]string{
	"kubectl":        CategoryKubernetes,
	"k":              CategoryKubernetes,
	"k9s":            CategoryKubernetes,
	"helm":           CategoryKubernetes,
	"argocd":         CategoryKubernetes,
	"terraform":      CategoryTerraform,
	"tf":             CategoryTerraform,
	"terragrunt":     CategoryTerraform,
	"aws":            CategoryAWS,
	"awsv2":          CategoryAWS,
	"git":            CategoryGit,
	"gh":             CategoryGit,
	"gcm":            CategoryGit,
	"docker":         CategoryDocker,
	"docker-compose": CategoryDocker,
	"podman":         CategoryDocker,
}

// infraBinaries is the set of binaries considered infrastructure-related.
var infraBinaries = map[string]bool{
	"kubectl": true, "k": true, "helm": true, "argocd": true,
	"terraform": true, "tf": true, "terragrunt": true,
	"aws": true, "awsv2": true,
}

// deployVerbPattern matches deploy-indicating verbs in command text.
var deployVerbPattern = regexp.MustCompile(`\b(apply|push|upgrade|release|merge|sync|deploy|install|uninstall)\b`)

// sensitivePattern matches sensitive key=value / key: value patterns for redaction.
// Uses a word boundary to avoid matching mid-word (e.g. "keyring" is not matched).
var sensitivePattern = regexp.MustCompile(`(?i)(\b(?:password|token|secret|key)[\s=:]+)\S+`)

// classifyCategory returns the shell category for a binary name.
func classifyCategory(binary string) string {
	if cat, ok := categoryMap[binary]; ok {
		return cat
	}
	return CategoryGeneral
}

// isInfraCommand returns true when the binary is infrastructure-related.
func isInfraCommand(binary string) bool {
	return infraBinaries[binary]
}

// isDeployCommand returns true when the command text contains deploy-indicating verbs.
func isDeployCommand(cmd string) bool {
	return deployVerbPattern.MatchString(cmd)
}

// redactSensitive replaces sensitive values in command text with [REDACTED].
// Matches patterns like: TOKEN=abc123, --password=foo, secret: bar
func redactSensitive(cmd string) string {
	return sensitivePattern.ReplaceAllString(cmd, "${1}[REDACTED]")
}

// extractBinary returns the first non-env-var token of a command (the executable name).
// Env var assignments (KEY=value) are skipped.
func extractBinary(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	parts := strings.Fields(cmd)
	for _, p := range parts {
		if !strings.Contains(p, "=") {
			return p
		}
	}
	return parts[0]
}
