# epic-claim-discovery.fish — EPIC-072 M6: Discovery-phase ownership marker
# Marks a Discovery Report section as actively being investigated.
#
# Usage:
#   epic-claim-discovery <epic-path> <agent-id>
#
# Example:
#   epic-claim-discovery docs/epics/EPIC-072.md fish-config-agent

function epic-claim-discovery --description "Mark a discovery report section as actively investigating"
    if test (count $argv) -lt 2
        echo "Usage: epic-claim-discovery <epic-path> <agent-id>"
        return 1
    end

    set -l epic_path $argv[1]
    set -l agent_id $argv[2]

    if not test -f "$epic_path"
        echo "Epic not found: $epic_path"
        return 1
    end

    set -l timestamp (date -u +%Y%m%dT%H%M%SZ)

    python3 -c '
import sys

path = sys.argv[1]
agent_id = sys.argv[2]
timestamp = sys.argv[3]

content = open(path).read()
pending = "_Pending — agent will write back findings here._"
investigating = "_Investigating — " + agent_id + " started " + timestamp + "_"

# Find the section for this agent and replace the pending marker
section_header = "### Discovery Report: " + agent_id
if section_header not in content:
    print("Section not found: " + section_header)
    sys.exit(1)

if pending not in content:
    print("No pending marker found — already claimed or completed")
    sys.exit(1)

# Replace only the first pending marker after this agent section header
idx = content.index(section_header)
pending_idx = content.index(pending, idx)
content = content[:pending_idx] + investigating + content[pending_idx + len(pending):]
open(path, "w").write(content)
print("ok")
' "$epic_path" "$agent_id" "$timestamp"

    if test $status -eq 0
        echo "✅ Claimed discovery for $agent_id in "(basename "$epic_path")
    else
        echo "❌ Failed to claim discovery"
        return 1
    end
end
