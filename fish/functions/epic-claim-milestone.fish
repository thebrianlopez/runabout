function epic-claim-milestone --description 'Atomically claim a milestone before executing it'
    if test (count $argv) -lt 3
        echo "Usage: epic-claim-milestone <epic-path> <milestone> <agent-id>"
        echo "Example: epic-claim-milestone docs/epics/DEVTOOLS_*.md M1 fish-config-agent"
        return 1
    end

    set -l epic_path $argv[1]
    set -l milestone $argv[2]
    set -l agent_id $argv[3]

    if not test -f $epic_path
        echo "Epic not found: $epic_path"
        return 1
    end

    # Check current status of this milestone row
    set -l row (grep "| $milestone |" $epic_path | head -1)

    if test -z "$row"
        echo "⚠️  Milestone $milestone not found in "(basename $epic_path)
        return 1
    end

    if string match -q '*In Progress*' $row
        echo "⚠️  $milestone already In Progress — skipping claim"
        return 1
    end

    if string match -q '*Complete*' $row
        echo "⚠️  $milestone already Complete — skipping"
        return 1
    end

    # Claim: Pending → **In Progress** (agent_id)
    # Uses Python literal str.replace() — no regex, safe with | in table cells
    python3 -c "
import sys
path = sys.argv[1]
milestone = sys.argv[2]
agent_id = sys.argv[3]
lines = open(path).readlines()
out = []
for line in lines:
    if ('| ' + milestone + ' |') in line and ' Pending |' in line:
        line = line.replace(' Pending |', ' **In Progress** (' + agent_id + ') |', 1)
    out.append(line)
open(path, 'w').writelines(out)
" $epic_path $milestone $agent_id

    if test $status -eq 0
        echo "✅ Claimed $milestone for $agent_id"
        return 0
    else
        echo "❌ Failed to claim $milestone"
        return 1
    end
end
