#!/usr/bin/env fish

# Fetch detailed PR information for all merged PRs

set OUTPUT_FILE output/pr_details_full.json
echo "[" > $OUTPUT_FILE

set FIRST true

# infra-terraform PRs
for pr in 6910 6940 6942 6958 6960 6970 6979 6980 7029 7031
    if test "$FIRST" = "true"
        set FIRST false
    else
        echo "," >> $OUTPUT_FILE
    end
    gh pr view $pr --repo your-org/infra-terraform --json number,title,state,body,author,createdAt,mergedAt,mergedBy,additions,deletions,changedFiles,commits,reviews,labels >> $OUTPUT_FILE
end

# infra-helm PRs
for pr in 7998 7999 8000 8013 8016 8017 8018 8029 8047 8080
    echo "," >> $OUTPUT_FILE
    gh pr view $pr --repo your-org/infra-helm --json number,title,state,body,author,createdAt,mergedAt,mergedBy,additions,deletions,changedFiles,commits,reviews,labels >> $OUTPUT_FILE
end

echo "]" >> $OUTPUT_FILE

echo "✅ Detailed PR data saved to $OUTPUT_FILE"
