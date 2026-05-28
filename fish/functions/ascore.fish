function ascore --description 'AI usage scorer — local JSONL (Anti-Pattern Risk + Operational Maturity)'
    # Usage: ascore [--local] [--days N] [--user USERNAME]
    #   --local   default mode; score from local JSONL telemetry
    #   --days N  analysis window in days (default: 7)
    #   --user U  target user (default: current user)
    argparse 'local' 'days=' 'user=' -- $argv
    or return 1

    set -l days 7
    if set -q _flag_days
        set days $_flag_days
    end

    set -l target_user $USER
    if set -q _flag_user
        set target_user $_flag_user
    end

    set -l scorer (dirname (status filename))/ascore_scorer.py
    if not test -f $scorer
        echo "ascore: scorer not found at $scorer" >&2
        return 1
    end

    set -l score_tmp (mktemp /tmp/ascore_XXXXXX.json)

    python3 $scorer $days $target_user $score_tmp

    if test -f $score_tmp
        set -l meta_json (cat $score_tmp)
        rm -f $score_tmp
        if test "$meta_json" != "{}"
            emit_jsonl \
                --layer fish \
                --event-type score_run \
                --command "ascore --local" \
                --metadata-json $meta_json
        end
    end
end
