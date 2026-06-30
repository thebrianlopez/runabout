---
schema_version: 1
task: epic-231-aws-doctor-validation
agent: runabout-agent
epic_path: /Users/brian/code/personal/docs/epics/PERSONAL_20260630T034817Z_runabout_EPIC-231_AWSCredentialResolution_DoctorValidation.md
dispatched_at: 20260630T034817Z
status: completed
claimed_at: 20260630T035039Z
completed_at: 20260630T035039Z
milestones: [M1, M2]
model: pi
producer: workspace-agent
---

# Task: EPIC-231 - Doctor AWS Credential Validation

Implement F3 from the AWS Credential Resolution FDD.

**SEQUENCING: This dispatch depends on EPIC-230 (AWSConfig struct + factory wiring). If EPIC-230 is not yet complete, wait for it before starting.**

## TDD Reference

Read the full TDD at: `/Users/brian/code/personal/docs/design/PERSONAL_20260630T032937Z_Runabout_AWSCredentialResolution_DoctorValidation_TDD.md`

## What to implement

1. **M1 - Contract tests (fail first):**
   - Write CT-1 through CT-6 + RG-1/RG-2 in `cmd/linkari/cmd_doctor_test.go`
   - Use mock STS and SM clients - no real AWS calls
   - Tests should compile but fail

2. **M2 - Implementation:**
   - Extend `aws_credentials` check in `cmd/linkari/cmd_doctor.go`
   - Add `sts.GetCallerIdentity` call to report IAM ARN
   - Detect credential source type (IMDS, profile, env, web-identity)
   - Add `ListSecrets MaxResults=1` SM access probe
   - Wire `aws_no_credentials` and `aws_sm_access_denied` error messages with remediation hints
   - Run `go test ./cmd/linkari/...` - all tests must pass

## Doctor output format

```
# success
aws_credentials: resolved via shared-credentials-file (profile: brianonpoint)

# no credentials
aws_credentials: no credentials found - set [aws] profile, role_arn, or AWS_ACCESS_KEY_ID

# SM access denied
aws_credentials: resolved via shared-credentials-file (profile: brianonpoint), but Secrets Manager access denied - check IAM policy
```

## Security: RG-1

Doctor output must NEVER contain ASIA, AKIA, or any AWS secret key material.

## Completion Protocol

1. Commit all changes to git
2. Push to remote: `git push origin main`
3. Run: `dispatch-complete .claude-dispatch/epic-231-aws-doctor-validation.md`

## Response

