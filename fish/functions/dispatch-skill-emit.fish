function dispatch-skill-emit --description 'Emit dispatch_skill_invoked telemetry'
    argparse 'skill=' 'agent=' 'mode=' 'found=' 'processed=' 'argument=' -- $argv
    or return 0
    set -q _flag_found; or set _flag_found 0
    set -q _flag_processed; or set _flag_processed 0
    set -q _flag_argument; or set _flag_argument ''
    emit_jsonl --layer orchestration --event-type dispatch_skill_invoked \
        --command "/$_flag_skill" \
        --metadata-json (printf '{"skill":"%s","agent":"%s","mode":"%s","triggers_found":%s,"triggers_processed":%s,"argument":"%s"}' \
            $_flag_skill $_flag_agent $_flag_mode $_flag_found $_flag_processed "$_flag_argument")
end
